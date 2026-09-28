package query

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type configuredDataSourceCatalog struct {
	DataSources          []database.DataSource            `json:"dataSources"`
	Assignments          []configuredDataSourceAssignment `json:"assignments"`
	DefaultDataSourceIDs []string                         `json:"-"`
}

type configuredDataSourceAssignment struct {
	OrganizationID string   `json:"organizationId"`
	Signal         string   `json:"signal"`
	DataSourceIDs  []string `json:"dataSourceIds"`
}

type catalogGroups[T any] struct {
	Unified []T `json:"unified"`
	Traces  []T `json:"traces"`
	Logs    []T `json:"logs"`
}

type configuredAssignmentGroup struct {
	OrganizationID string   `json:"organizationId"`
	DataSourceIDs  []string `json:"dataSourceIds"`
}

type configuredDataSourceCatalogInput struct {
	DataSources catalogGroups[database.DataSource]       `json:"dataSources"`
	Assignments catalogGroups[configuredAssignmentGroup] `json:"assignments"`
}

const mockDataSourceID = "mock"

// RoutedStore resolves an organization's signal datasource from PostgreSQL for
// every operation. Pools are reused by immutable connection configuration.
type RoutedStore struct {
	control *gorm.DB
	signal  string
	mu      *sync.Mutex
	pools   map[string]*clickHouseStore
}

func NewRoutedStores(control *gorm.DB) (*RoutedStore, *RoutedStore) {
	pools := make(map[string]*clickHouseStore)
	mu := &sync.Mutex{}
	return &RoutedStore{control: control, signal: "traces", pools: pools, mu: mu},
		&RoutedStore{control: control, signal: "logs", pools: pools, mu: mu}
}

func (s *RoutedStore) Query(ctx context.Context, statement string, args ...any) ([]map[string]any, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok || principal.OrganizationID == "" {
		return nil, errors.New("missing organization context")
	}
	store, err := s.resolve(ctx, principal.OrganizationID, DataSourceFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return store.Query(ctx, statement, args...)
}

func (s *RoutedStore) resolve(ctx context.Context, organizationID, selectedID string) (*clickHouseStore, error) {
	sources := []database.DataSource{}
	query := s.control.WithContext(ctx).Table(database.DataSource{}.TableName()+" AS source").
		Select("source.*").
		Joins("JOIN telemetry_organization_data_sources AS assignment ON assignment.data_source_id = source.id").
		Where("assignment.organization_id = ? AND assignment.signal = ?", organizationID, s.signal)
	if selectedID != "" {
		query = query.Where("source.id = ?", selectedID)
	}
	if err := query.Order("source.id").Limit(2).Find(&sources).Error; err != nil {
		return nil, fmt.Errorf("resolve %s datasource: %w", s.signal, err)
	}
	if len(sources) == 0 {
		if selectedID != "" {
			return nil, fmt.Errorf("datasource %q is not assigned to organization signal %s", selectedID, s.signal)
		}
		return nil, fmt.Errorf("no %s datasource assignment", s.signal)
	}
	if selectedID == "" && len(sources) > 1 {
		return nil, fmt.Errorf("select one of the organization's %s datasources", s.signal)
	}
	return s.pool(sources[0])
}

func (s *RoutedStore) pool(source database.DataSource) (*clickHouseStore, error) {
	if !strings.HasPrefix(source.PasswordEnv, "CLICKHOUSE_") {
		return nil, errors.New("datasource password environment variable must start with CLICKHOUSE_")
	}
	password, ok := os.LookupEnv(source.PasswordEnv)
	if !ok {
		return nil, fmt.Errorf("datasource credential %s is unavailable", source.PasswordEnv)
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t", source.Address, source.Database, source.Username, source.PasswordEnv, source.Secure)
	s.mu.Lock()
	defer s.mu.Unlock()
	if store := s.pools[key]; store != nil {
		return store, nil
	}
	options := &clickhouse.Options{
		Addr: []string{source.Address}, Auth: clickhouse.Auth{Database: source.Database, Username: source.Username, Password: password},
		DialTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
	}
	if source.Secure {
		host, _, err := net.SplitHostPort(source.Address)
		if err != nil {
			return nil, fmt.Errorf("invalid secure datasource address: %w", err)
		}
		options.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	store := openOptions(options)
	s.pools[key] = store
	return store, nil
}

func (s *RoutedStore) Ping(ctx context.Context) error {
	return s.eachStore(ctx, func(store *clickHouseStore) error { return store.Ping(ctx) })
}

func (s *RoutedStore) Probe(ctx context.Context, statement string) error {
	return s.eachStore(ctx, func(store *clickHouseStore) error {
		_, err := store.Query(ctx, statement)
		return err
	})
}

func (s *RoutedStore) eachStore(ctx context.Context, check func(*clickHouseStore) error) error {
	sources := []database.DataSource{}
	if err := s.control.WithContext(ctx).Table(database.DataSource{}.TableName()+" AS source").
		Distinct("source.*").
		Joins("JOIN telemetry_organization_data_sources AS assignment ON assignment.data_source_id = source.id").
		Where("assignment.signal = ?", s.signal).Order("source.id").Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("no %s datasource assignments", s.signal)
	}
	for _, source := range sources {
		store, err := s.pool(source)
		if err != nil {
			return err
		}
		if err := check(store); err != nil {
			return err
		}
	}
	return nil
}

func (s *RoutedStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for key, store := range s.pools {
		if err := store.Close(); err != nil && first == nil {
			first = err
		}
		delete(s.pools, key)
	}
	return first
}

// BootstrapDataSources imports grouped catalog or legacy environment settings as
// GitOps-managed datasource records without persisting passwords.
func BootstrapDataSources(ctx context.Context, control *gorm.DB, organizationIDs []string) error {
	return database.WithControlLock(ctx, control, func(tx *gorm.DB) error {
		if err := tx.Where("managed_by = ?", "config").Delete(&database.OrganizationDataSource{}).Error; err != nil {
			return err
		}
		if raw := os.Getenv("CLICKHOUSE_CATALOG_JSON"); raw != "" {
			catalog, err := parseDataSourceCatalog(raw)
			if err != nil {
				return err
			}
			if len(catalog.Assignments) == 0 {
				sort.Strings(organizationIDs)
				organizationIDs = uniqueStrings(organizationIDs)
				for _, organizationID := range organizationIDs {
					for _, signal := range []string{"traces", "logs"} {
						catalog.Assignments = append(catalog.Assignments, configuredDataSourceAssignment{
							OrganizationID: organizationID,
							Signal:         signal,
							DataSourceIDs:  append([]string(nil), catalog.DefaultDataSourceIDs...),
						})
					}
				}
			}
			for _, source := range catalog.DataSources {
				if err := upsertConfiguredDataSource(tx, source); err != nil {
					return err
				}
			}
			for _, configured := range catalog.Assignments {
				var organizationCount int64
				if err := tx.Model(&database.Organization{}).Where("id = ?", configured.OrganizationID).Count(&organizationCount).Error; err != nil {
					return err
				}
				if organizationCount != 1 {
					return fmt.Errorf("catalog assignment references unknown organization %q", configured.OrganizationID)
				}
				var databaseManaged int64
				if err := tx.Model(&database.OrganizationDataSource{}).
					Where("organization_id = ? AND signal = ? AND managed_by <> ?", configured.OrganizationID, configured.Signal, "config").
					Count(&databaseManaged).Error; err != nil {
					return err
				}
				if databaseManaged > 0 {
					continue
				}
				for _, sourceID := range configured.DataSourceIDs {
					assignment := database.OrganizationDataSource{OrganizationID: configured.OrganizationID, Signal: configured.Signal, DataSourceID: sourceID, ManagedBy: "config"}
					if err := tx.Create(&assignment).Error; err != nil {
						return err
					}
				}
			}
			return nil
		}
		sources := map[string]database.DataSource{}
		for _, signal := range []string{"traces", "logs"} {
			prefix := strings.ToUpper(signal) + "_"
			options := connectionOptions(prefix)
			passwordEnv := "CLICKHOUSE_PASSWORD"
			if _, ok := os.LookupEnv("CLICKHOUSE_" + prefix + "PASSWORD"); ok {
				passwordEnv = "CLICKHOUSE_" + prefix + "PASSWORD"
			}
			id := "config-" + signal
			sources[signal] = database.DataSource{
				ID: id, Address: options.Addr[0], Database: options.Auth.Database,
				Username: options.Auth.Username, PasswordEnv: passwordEnv,
				Secure: options.TLS != nil, ManagedBy: "config",
			}
		}
		for _, source := range sources {
			if err := upsertConfiguredDataSource(tx, source); err != nil {
				return err
			}
		}
		sort.Strings(organizationIDs)
		organizationIDs = uniqueStrings(organizationIDs)
		for _, organizationID := range organizationIDs {
			for _, signal := range []string{"traces", "logs"} {
				var existing database.OrganizationDataSource
				err := tx.Where("organization_id = ? AND signal = ?", organizationID, signal).Take(&existing).Error
				if err == nil {
					continue
				}
				if err != nil && err != gorm.ErrRecordNotFound {
					return err
				}
				assignment := database.OrganizationDataSource{OrganizationID: organizationID, Signal: signal, DataSourceID: sources[signal].ID, ManagedBy: "config"}
				if err := tx.Create(&assignment).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// BootstrapMockDataSource makes the in-memory backend selectable for every
// configured organization when OAuth still supplies the control plane.
func BootstrapMockDataSource(ctx context.Context, control *gorm.DB, organizationIDs []string) error {
	return database.WithControlLock(ctx, control, func(tx *gorm.DB) error {
		return bootstrapMockDataSource(tx, organizationIDs)
	})
}

func bootstrapMockDataSource(tx *gorm.DB, organizationIDs []string) error {
	if err := tx.Where("managed_by = ?", "config").Delete(&database.OrganizationDataSource{}).Error; err != nil {
		return err
	}
	source := database.DataSource{
		ID: mockDataSourceID, Address: "In-memory mock", Database: "synthetic",
		Username: "mock", PasswordEnv: "CLICKHOUSE_MOCK_UNUSED",
		ManagedBy: "config",
	}
	if err := upsertConfiguredDataSource(tx, source); err != nil {
		return err
	}
	sort.Strings(organizationIDs)
	organizationIDs = uniqueStrings(organizationIDs)
	for _, organizationID := range organizationIDs {
		for _, signal := range []string{"traces", "logs"} {
			assignment := database.OrganizationDataSource{
				OrganizationID: organizationID, Signal: signal,
				DataSourceID: mockDataSourceID, ManagedBy: "config",
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&assignment).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func upsertConfiguredDataSource(tx *gorm.DB, source database.DataSource) error {
	var existing database.DataSource
	err := tx.Where("id = ?", source.ID).Take(&existing).Error
	if err == nil && existing.ManagedBy != "config" {
		return nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"address", "database", "username", "password_env", "secure", "managed_by"})}).Create(&source).Error
}

func parseDataSourceCatalog(raw string) (configuredDataSourceCatalog, error) {
	var input configuredDataSourceCatalogInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return configuredDataSourceCatalog{}, fmt.Errorf("invalid CLICKHOUSE_CATALOG_JSON: %w", err)
	}
	catalog := configuredDataSourceCatalog{}
	allowedSignals := map[string]map[string]bool{}
	appendSources := func(signal string, configured []database.DataSource) {
		for _, source := range configured {
			catalog.DataSources = append(catalog.DataSources, source)
			if signal == "unified" {
				catalog.DefaultDataSourceIDs = append(catalog.DefaultDataSourceIDs, source.ID)
			}
			if allowedSignals[source.ID] == nil {
				allowedSignals[source.ID] = map[string]bool{}
			}
			if signal == "unified" {
				allowedSignals[source.ID]["traces"] = true
				allowedSignals[source.ID]["logs"] = true
			} else {
				allowedSignals[source.ID][signal] = true
			}
		}
	}
	appendSources("unified", input.DataSources.Unified)
	appendSources("traces", input.DataSources.Traces)
	appendSources("logs", input.DataSources.Logs)
	appendAssignments := func(signal string, configured []configuredAssignmentGroup) {
		for _, assignment := range configured {
			catalog.Assignments = append(catalog.Assignments, configuredDataSourceAssignment{
				OrganizationID: assignment.OrganizationID,
				Signal:         signal,
				DataSourceIDs:  assignment.DataSourceIDs,
			})
		}
	}
	for _, signal := range []string{"traces", "logs"} {
		appendAssignments(signal, input.Assignments.Unified)
	}
	appendAssignments("traces", input.Assignments.Traces)
	appendAssignments("logs", input.Assignments.Logs)
	if len(catalog.DataSources) == 0 {
		return catalog, errors.New("catalog requires at least one datasource")
	}
	if len(catalog.Assignments) == 0 && len(catalog.DefaultDataSourceIDs) == 0 {
		return catalog, errors.New("catalog without explicit assignments requires a unified datasource")
	}
	sources := make(map[string]bool, len(catalog.DataSources))
	for i := range catalog.DataSources {
		source := &catalog.DataSources[i]
		source.ManagedBy = "config"
		if source.ID == "" || source.Address == "" || source.Database == "" || source.Username == "" || !strings.HasPrefix(source.PasswordEnv, "CLICKHOUSE_") {
			return catalog, fmt.Errorf("catalog datasources require id, address, database, username, and a CLICKHOUSE_* passwordEnv")
		}
		if sources[source.ID] {
			return catalog, fmt.Errorf("duplicate catalog datasource %q", source.ID)
		}
		sources[source.ID] = true
	}
	seenAssignments := map[string]bool{}
	for _, assignment := range catalog.Assignments {
		key := assignment.OrganizationID + "\x00" + assignment.Signal
		if assignment.OrganizationID == "" || (assignment.Signal != "traces" && assignment.Signal != "logs") || len(assignment.DataSourceIDs) == 0 || seenAssignments[key] {
			return catalog, fmt.Errorf("invalid or duplicate catalog assignment for organization %q signal %q", assignment.OrganizationID, assignment.Signal)
		}
		seenAssignments[key] = true
		selected := map[string]bool{}
		for _, sourceID := range assignment.DataSourceIDs {
			if !sources[sourceID] || !allowedSignals[sourceID][assignment.Signal] || selected[sourceID] {
				return catalog, fmt.Errorf("invalid or duplicate datasource %q in catalog assignment", sourceID)
			}
			selected[sourceID] = true
		}
	}
	return catalog, nil
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
