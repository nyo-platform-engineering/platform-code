package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"aat/aggregator/internal/model"
	"gorm.io/gorm"
)

// EnableCitus delegates placement and online rebalancing to Citus. Source state,
// fencing and outbox records remain local to the coordinator; ingestion still
// commits them atomically with the distributed canonical write (Citus 2PC).
func EnableCitus(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(403100)").Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL statement_timeout = '60s'").Error; err != nil {
			return err
		}
		var workers int64
		if err := tx.Raw(`SELECT count(*) FROM pg_dist_node WHERE groupid > 0 AND noderole = 'primary' AND isactive AND shouldhaveshards`).Scan(&workers).Error; err != nil {
			return err
		}
		if workers < 3 {
			return errors.New("Citus requires at least three worker groups; start the storage formation first")
		}
		var distributed bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM pg_dist_partition WHERE logicalrelid = 'hazard_events'::regclass AND partmethod = 'h')`).Scan(&distributed).Error; err != nil {
			return err
		}
		if !distributed {
			if err := tx.Exec("SET LOCAL citus.shard_count = 24").Error; err != nil {
				return err
			}
			if err := tx.Exec(`SELECT create_distributed_table('hazard_events', 'source_ref_id')`).Error; err != nil {
				return err
			}
			// Citus retains the old coordinator rows after distributing a populated table.
			if err := tx.Exec(`SELECT truncate_local_data_after_distributing_table('hazard_events')`).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`UPDATE hazard_outboxes SET payload = '{}'::jsonb WHERE published_at IS NOT NULL AND payload <> '{}'::jsonb`).Error
	})
}

// Each member is pinned to a real Kubernetes node in the storage chart. Patroni
// supplies the live role and replication health; this file contains no secrets.
type CitusMember struct {
	Unit  string `json:"unit"`
	Node  string `json:"node"`
	Group int    `json:"group"`
	Host  string `json:"host"`
	API   string `json:"api"`
}

func ReadCitusMembers(path string) ([]CitusMember, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("CITUS_MEMBERS_FILE must reference the storage inventory ConfigMap")
	}
	var members []CitusMember
	if err = json.Unmarshal(raw, &members); err != nil {
		return nil, errors.New("invalid Citus member inventory")
	}
	groups := map[int]map[string]bool{}
	names := map[string]bool{}
	counts := map[int]int{}
	for _, m := range members {
		u, err := url.Parse(m.API)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || m.Unit == "" || m.Node == "" || m.Host == "" || m.Group < 1 || names[m.Unit] {
			return nil, errors.New("invalid Citus member")
		}
		names[m.Unit] = true
		counts[m.Group]++
		if groups[m.Group] == nil {
			groups[m.Group] = map[string]bool{}
		}
		groups[m.Group][m.Node] = true
	}
	if len(groups) < 3 {
		return nil, errors.New("Citus needs three worker groups")
	}
	perNode := map[string]int{}
	for group, nodes := range groups {
		if counts[group] != 2 || len(nodes) != 2 {
			return nil, errors.New("each Citus worker group needs two distinct nodes")
		}
		for node := range nodes {
			perNode[node]++
		}
	}
	for _, count := range perNode {
		if count == len(groups) {
			return nil, errors.New("one node would contain the entire dataset")
		}
	}
	return members, nil
}

type Citus struct {
	*Postgres
	members []CitusMember
	http    *http.Client
}

func NewCitus(db *gorm.DB, members []CitusMember) *Citus {
	return &Citus{Postgres: NewPostgres(db, Identity{Unit: "citus"}), members: members, http: &http.Client{Timeout: 400 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// A failed distributed SQL read never becomes a complete empty response. Citus
// cannot safely return a globally ordered subset when a worker is unavailable:
// preserve the cursor and mark the result partial until Patroni restores it.
func (c *Citus) unavailable(ctx context.Context) Availability {
	units := []string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, member := range c.members {
		wg.Add(1)
		go func(m CitusMember) {
			defer wg.Done()
			if _, ok := c.memberRole(ctx, m); !ok {
				mu.Lock()
				units = append(units, m.Unit)
				mu.Unlock()
			}
		}(member)
	}
	wg.Wait()
	if len(units) == 0 {
		units = append(units, "citus-query")
	}
	sort.Strings(units)
	return Availability{Partial: true, UnavailableUnits: units}
}

func (c *Citus) memberRole(ctx context.Context, m CitusMember) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(m.API, "/")+"/patroni", nil)
	if err != nil {
		return "", false
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", false
	}
	defer res.Body.Close()
	var status struct {
		State, Role      string
		ReplicationState string `json:"replication_state"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&status) != nil || status.State != "running" {
		return "", false
	}
	if status.Role == "primary" || status.Role == "master" {
		return "primary", true
	}
	if status.Role == "replica" && status.ReplicationState == "streaming" {
		return "replica", true
	}
	return "", false
}

func (c *Citus) Events(ctx context.Context, q EventQuery) (EventPage, error) {
	page, err := c.Postgres.Events(ctx, q)
	if err == nil && page.Partial {
		page.Availability = c.unavailable(ctx)
	}
	return page, err
}

func (c *Citus) Checksum(ctx context.Context, source string) (Checksum, error) {
	result, err := c.Postgres.Checksum(ctx, source)
	if err == nil && result.Partial {
		result.Availability = c.unavailable(ctx)
	}
	return result, err
}

func (c *Citus) Page(ctx context.Context, q HazardQuery) (HazardPage, error) {
	page, err := c.Postgres.Page(ctx, q)
	if err == nil && page.Partial {
		page.Availability = c.unavailable(ctx)
	}
	return page, err
}

func (c *Citus) Locations(ctx context.Context, id string) (Locations, error) {
	result := Locations{HazardID: id, Locations: []Location{}}
	var event model.HazardEvent
	err := c.db.WithContext(ctx).Select("id, source_ref_id").Take(&event, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, ErrNotFound
	}
	if err != nil {
		result.Partial = true
		return result, nil
	}
	var groups []struct {
		GroupID  int
		NodeName string
	}
	err = c.db.WithContext(ctx).Raw(`SELECT DISTINCT n.groupid AS group_id, n.nodename AS node_name FROM pg_dist_placement p JOIN pg_dist_node n USING (groupid) WHERE n.noderole = 'primary' AND p.shardid = get_shard_id_for_distribution_column('hazard_events', ?::text)`, event.Ref).Scan(&groups).Error
	if err != nil {
		result.Partial = true
		return result, nil
	}
	wanted := map[int]string{}
	for _, group := range groups {
		wanted[group.GroupID] = group.NodeName
	}
	for _, m := range c.members {
		if wanted[m.Group] == "" {
			continue
		}
		role, ok := c.memberRole(ctx, m)
		if !ok {
			result.Partial = true
			continue
		}
		// A standby may be streaming yet still catching up. Only advertise copies
		// which Patroni reports as having no replication lag.
		if role == "primary" && m.Host != wanted[m.Group] {
			result.Partial = true
			continue
		}
		if role == "replica" {
			req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(m.API, "/")+"/replica?lag=0", nil)
			res, err := c.http.Do(req)
			if err != nil {
				result.Partial = true
				continue
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				result.Partial = true
				continue
			}
		}
		result.Locations = append(result.Locations, Location{Node: m.Node, Unit: m.Unit, Role: role})
	}
	if len(result.Locations) == 0 {
		result.Partial = true
	}
	sort.Slice(result.Locations, func(i, j int) bool { return result.Locations[i].Unit < result.Locations[j].Unit })
	return result, nil
}

var _ Reader = (*Citus)(nil)
var _ Pager = (*Citus)(nil)
