package controller

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// satelliteHazards applies feed events in stream order and returns the fires they changed.
// Every live fire's ref is FireRef(key) of one of its own detections (its anchor), which
// keeps refs unique: merges keep the fire with the earliest detection, and on a split
// only the part still holding the anchor keeps the ref.
//
// The decisions are the same as applying the events one SQL statement at a time (the
// reference in satellite_reference_test.go must give identical tables), but the batch
// talks to the database a fixed number of times instead of a few times per event:
//   - one query loads every stored fire member near any detection of the batch, and the
//     neighbour search then runs in memory against those rows and the batch's own;
//   - new and changed detections, and fire changes, are kept in memory and written in
//     bulk; whatever reads the tables (a merge's earliest members, a split, the events)
//     first flushes them, so every read sees exactly what it saw before;
//   - the touched fires' events are built from three queries, not three per fire.
func (ctrl *Controller) satelliteHazards(tx *gorm.DB, records []model.Record) ([]model.HazardEvent, error) {
	events := make([]mapper.SatelliteEvent, 0, len(records))
	keys := make([]string, 0, len(records))
	for _, record := range records {
		event, err := mapper.Satellite(record)
		if err != nil {
			return nil, Invalid{err}
		}
		events = append(events, event)
		keys = append(keys, event.Detection.Key)
	}

	// Fire membership spans many rows, so satellite batches apply one at a time.
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('SATELIT', 0))").Error; err != nil {
		return nil, err
	}

	var newest sql.NullTime
	if err := tx.Raw("SELECT max(detected_at) FROM satellite_detections").Scan(&newest).Error; err != nil {
		return nil, err
	}
	c := newClusterer(tx)
	for start := 0; start < len(keys); start += 1000 {
		var stored []model.SatelliteDetection
		if err := tx.Where("detection_key IN ?", keys[start:min(start+1000, len(keys))]).Find(&stored).Error; err != nil {
			return nil, err
		}
		for i := range stored {
			c.add(&stored[i])
		}
	}
	if err := c.loadNeighbours(events); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	for _, event := range events {
		d := event.Detection
		row, found := c.rows[d.Key]
		if !found && event.Kind == mapper.SatelliteUpdated {
			// The feed only updates detections it sent before; without the row
			// there is nothing to change, so the update is logged and ignored.
			slog.Warn("satellite update for unknown detection ignored", "key", d.Key)
			continue
		}
		if !found {
			row = &model.SatelliteDetection{
				Key: d.Key, Satellite: d.Satellite, DetectedAt: d.DetectedAt, Latitude: d.Latitude,
				Longitude: d.Longitude, Confidence: d.Confidence, FRP: d.FRP, ReceivedAt: now,
			}
			if event.Kind == mapper.SatelliteRetracted {
				row.Retracted = true
			} else {
				row.Late = newest.Valid && d.DetectedAt.Before(newest.Time.Add(-ctrl.satelliteLateness()))
				if !newest.Valid || d.DetectedAt.After(newest.Time) {
					newest = sql.NullTime{Time: d.DetectedAt, Valid: true}
				}
			}
			// Joined before it is stored, as with one statement per event: the new
			// detection is not its own neighbour.
			if eligible(row) {
				if err := c.join(row); err != nil {
					return nil, err
				}
			}
			c.add(row)
			c.pending[row.Key] = row
			continue
		}

		// Late is decided once on arrival; a retraction is permanent.
		previous := row.FireRef
		if event.Kind == mapper.SatelliteRetracted {
			row.Retracted = true
		} else {
			// An update carries only the fields that changed.
			if event.SetConfidence {
				row.Confidence = d.Confidence
			}
			if event.SetFRP {
				row.FRP = d.FRP
			}
		}
		switch {
		case previous != nil && eligible(row):
			c.touched[*previous] = true
		case previous != nil:
			row.FireRef = nil
		case eligible(row):
			if err := c.join(row); err != nil {
				return nil, err
			}
		}
		c.pending[row.Key] = row
		if previous != nil && row.FireRef == nil {
			if err := c.split(*previous); err != nil {
				return nil, err
			}
		}
	}
	return c.hazards()
}

// latBucket groups detections by latitude for the in-memory neighbour search; a
// neighbour box (±LinkDistanceKm/110 degrees) spans at most three buckets.
const latBucket = 0.01

func bucketOf(lat float64) int { return int(math.Floor(lat / latBucket)) }

// fireChange is what the batch did to one fire, applied in one statement at the end.
// recordFire inserts a fire or revives it (formed OR new formed, merged_into cleared);
// a merge marks the survivor formed and the others merged_into. Formed never goes
// back to false, so ORing every flag and keeping the last merged_into gives the same
// row as running the statements in order.
type fireChange struct {
	insert    bool   // a recordFire: create the fire if it does not exist yet
	anchorKey string // the first recordFire's anchor (later ones keep the stored anchor)
	formed    bool
	setMerged bool
	mergedTo  *string
}

// clusterer keeps fire state in the database, so the resulting events are the same
// however the stream is split into batches. Within a batch it works on one in-memory
// copy per detection it has seen (rows), kept in step with every statement it runs.
type clusterer struct {
	tx      *gorm.DB
	touched map[string]bool // fires whose HazardEvent must be recomputed
	rows    map[string]*model.SatelliteDetection
	buckets map[int][]*model.SatelliteDetection
	pending map[string]*model.SatelliteDetection // created or changed, not yet written
	fires   map[string]*fireChange
}

func newClusterer(tx *gorm.DB) *clusterer {
	return &clusterer{tx: tx, touched: map[string]bool{}, rows: map[string]*model.SatelliteDetection{},
		buckets: map[int][]*model.SatelliteDetection{}, pending: map[string]*model.SatelliteDetection{},
		fires: map[string]*fireChange{}}
}

// add makes row the batch's copy of its detection (the first copy wins).
func (c *clusterer) add(row *model.SatelliteDetection) {
	if _, ok := c.rows[row.Key]; ok {
		return
	}
	c.rows[row.Key] = row
	b := bucketOf(row.Latitude)
	c.buckets[b] = append(c.buckets[b], row)
}

// loadNeighbours fetches, in one query, every stored fire member inside the neighbour
// box and time window of any detection in the batch. Those are the only stored rows
// a neighbour query in this batch could return: a stored row without a fire only
// gets one by being in the batch itself, and merges or splits only move rows that
// already have one.
func (c *clusterer) loadNeighbours(events []mapper.SatelliteEvent) error {
	const columns = 6
	for start := 0; start < len(events); start += 500 {
		chunk := events[start:min(start+500, len(events))]
		values := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)*columns)
		for i, event := range chunk {
			d := event.Detection
			latMin, latMax, lonMin, lonMax := mapper.NeighborBox(d.Latitude, d.Longitude)
			if i == 0 {
				values[i] = "(?::double precision, ?::double precision, ?::double precision, ?::double precision, ?::timestamptz, ?::timestamptz)"
			} else {
				values[i] = "(?, ?, ?, ?, ?, ?)"
			}
			args = append(args, latMin, latMax, lonMin, lonMax, d.DetectedAt.Add(-mapper.LinkWindow), d.DetectedAt.Add(mapper.LinkWindow))
		}
		// LATERAL keeps the per-box index range scans of a per-event query (a plain
		// EXISTS over the boxes plans as one full scan checking every box), in one round
		// trip. Rows found by several boxes are deduplicated by add.
		var stored []model.SatelliteDetection
		query := `SELECT s.* FROM (VALUES ` + strings.Join(values, ", ") + `) AS b(lat_min, lat_max, lon_min, lon_max, t_min, t_max)
			CROSS JOIN LATERAL (SELECT * FROM satellite_detections d
			  WHERE d.fire_ref IS NOT NULL AND d.detected_at BETWEEN b.t_min AND b.t_max
			    AND d.latitude BETWEEN b.lat_min AND b.lat_max AND d.longitude BETWEEN b.lon_min AND b.lon_max) s`
		if err := c.tx.Raw(query, args...).Scan(&stored).Error; err != nil {
			return err
		}
		for i := range stored {
			c.add(&stored[i])
		}
	}
	return nil
}

// candidates is the in-memory form of
//
//	fire_ref IS NOT NULL AND detection_key <> key AND detected_at BETWEEN t-W AND t+W
//	AND latitude BETWEEN latMin AND latMax AND longitude BETWEEN lonMin AND lonMax
//
// over the rows the database holds at this point of the batch.
func (c *clusterer) candidates(row *model.SatelliteDetection) []*model.SatelliteDetection {
	latMin, latMax, lonMin, lonMax := mapper.NeighborBox(row.Latitude, row.Longitude)
	from, to := row.DetectedAt.Add(-mapper.LinkWindow), row.DetectedAt.Add(mapper.LinkWindow)
	var found []*model.SatelliteDetection
	for b := bucketOf(latMin); b <= bucketOf(latMax); b++ {
		for _, other := range c.buckets[b] {
			if other.FireRef == nil || other.Key == row.Key || other.DetectedAt.Before(from) || other.DetectedAt.After(to) ||
				other.Latitude < latMin || other.Latitude > latMax || other.Longitude < lonMin || other.Longitude > lonMax {
				continue
			}
			found = append(found, other)
		}
	}
	return found
}

// flushRows writes every created or changed detection in one upsert per 500 rows.
func (c *clusterer) flushRows() error {
	if len(c.pending) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.pending))
	for key := range c.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]model.SatelliteDetection, len(keys))
	for i, key := range keys {
		rows[i] = *c.pending[key]
	}
	if err := c.tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "detection_key"}}, UpdateAll: true}).
		CreateInBatches(rows, 500).Error; err != nil {
		return err
	}
	c.pending = map[string]*model.SatelliteDetection{}
	return nil
}

// flushFires applies every fire change: inserts and revivals in one upsert, the
// merges' changes to fires that already existed in one UPDATE.
func (c *clusterer) flushFires() error {
	if len(c.fires) == 0 {
		return nil
	}
	refs := make([]string, 0, len(c.fires))
	for ref := range c.fires {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var inserts []model.SatelliteFire
	var updates []string
	var args []any
	for _, ref := range refs {
		change := c.fires[ref]
		if change.insert {
			// recordFire clears merged_into; a later merge may have set it again.
			inserts = append(inserts, model.SatelliteFire{Ref: ref, AnchorKey: change.anchorKey, Formed: change.formed, MergedInto: change.mergedTo})
			continue
		}
		if len(updates) == 0 {
			updates = append(updates, "(?::text, ?::boolean, ?::boolean, ?::text)")
		} else {
			updates = append(updates, "(?, ?, ?, ?)")
		}
		args = append(args, ref, change.formed, change.setMerged, change.mergedTo)
	}
	if len(inserts) > 0 {
		if err := c.tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "ref"}},
			DoUpdates: clause.Set{
				{Column: clause.Column{Name: "formed"}, Value: gorm.Expr("satellite_fires.formed OR excluded.formed")},
				{Column: clause.Column{Name: "merged_into"}, Value: gorm.Expr("excluded.merged_into")},
			},
		}).CreateInBatches(inserts, 500).Error; err != nil {
			return err
		}
	}
	if len(updates) > 0 {
		// Only merges touch a fire without recordFire, and they only touch fires that
		// detections already point to, so these rows exist.
		if err := c.tx.Exec(`UPDATE satellite_fires f SET formed = f.formed OR v.formed,
			merged_into = CASE WHEN v.set_merged THEN v.merged_into ELSE f.merged_into END
			FROM (VALUES `+strings.Join(updates, ", ")+`) AS v(ref, formed, set_merged, merged_into)
			WHERE f.ref = v.ref`, args...).Error; err != nil {
			return err
		}
	}
	c.fires = map[string]*fireChange{}
	return nil
}

func (c *clusterer) fire(ref string) *fireChange {
	change := c.fires[ref]
	if change == nil {
		change = &fireChange{}
		c.fires[ref] = change
	}
	return change
}

// recordFire creates or revives a fire. A revived fire is no longer merged into another.
func (c *clusterer) recordFire(ref, anchorKey string, formed bool) {
	c.touched[ref] = true
	change := c.fire(ref)
	if !change.insert {
		change.insert, change.anchorKey = true, anchorKey
	}
	change.formed = change.formed || formed
	change.setMerged, change.mergedTo = true, nil
}

// relabel moves every detection of fire from to fire to, in the table and in memory.
func (c *clusterer) relabel(from, to string) error {
	if err := c.tx.Model(&model.SatelliteDetection{}).Where("fire_ref = ?", from).Update("fire_ref", to).Error; err != nil {
		return err
	}
	for _, row := range c.rows {
		if row.FireRef != nil && *row.FireRef == from {
			ref := to
			row.FireRef = &ref
		}
	}
	return nil
}

func (c *clusterer) members(ref string) ([]model.SatelliteDetection, error) {
	if err := c.flushRows(); err != nil {
		return nil, err
	}
	var rows []model.SatelliteDetection
	err := c.tx.Where("fire_ref = ?", ref).Order("detected_at, detection_key").Find(&rows).Error
	return rows, err
}

// join attaches an eligible detection to every fire it links to, merging them.
func (c *clusterer) join(row *model.SatelliteDetection) error {
	self := toDetection(*row)
	linked := map[string]bool{}
	for _, candidate := range c.candidates(row) {
		if mapper.Linked(self, toDetection(*candidate)) {
			linked[*candidate.FireRef] = true
		}
	}
	if len(linked) == 0 {
		ref := mapper.FireRef(row.Key)
		row.FireRef = &ref
		c.recordFire(ref, row.Key, false)
		return nil
	}

	refs := make([]string, 0, len(linked))
	for ref := range linked {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	survivor := refs[0]
	if len(refs) > 1 {
		// The earliest member decides the survivor; the table must hold the batch's
		// detections so far (this one is not stored yet, as before).
		if err := c.flushRows(); err != nil {
			return err
		}
		var firsts []struct {
			FireRef      string
			DetectedAt   time.Time
			DetectionKey string
		}
		if err := c.tx.Raw(
			"SELECT DISTINCT ON (fire_ref) fire_ref, detected_at, detection_key FROM satellite_detections WHERE fire_ref IN ? ORDER BY fire_ref, detected_at, detection_key",
			refs,
		).Scan(&firsts).Error; err != nil {
			return err
		}
		sort.Slice(firsts, func(i, j int) bool {
			if !firsts[i].DetectedAt.Equal(firsts[j].DetectedAt) {
				return firsts[i].DetectedAt.Before(firsts[j].DetectedAt)
			}
			return firsts[i].DetectionKey < firsts[j].DetectionKey
		})
		if len(firsts) == 0 {
			return fmt.Errorf("satellite merge: no stored members for fires %v", refs)
		}
		survivor = firsts[0].FireRef
		for _, ref := range refs {
			if ref == survivor {
				continue
			}
			if err := c.relabel(ref, survivor); err != nil {
				return err
			}
			change := c.fire(ref)
			to := survivor
			change.setMerged, change.mergedTo = true, &to
			c.touched[ref] = true
		}
	}
	row.FireRef = &survivor
	c.touched[survivor] = true
	// The fire now has at least the neighbour and this detection.
	c.fire(survivor).formed = true
	return nil
}

// split regroups a fire after one of its detections stopped counting.
func (c *clusterer) split(ref string) error {
	c.touched[ref] = true
	rows, err := c.members(ref)
	if err != nil || len(rows) == 0 {
		return err
	}
	detections := make([]mapper.Detection, len(rows))
	for i, row := range rows {
		detections[i] = toDetection(row)
	}
	for _, group := range mapper.Components(detections) {
		next := mapper.FireRef(rows[group[0]].Key)
		keys := make([]string, 0, len(group))
		for _, i := range group {
			if mapper.FireRef(rows[i].Key) == ref {
				next = ref
			}
			keys = append(keys, rows[i].Key)
		}
		if next == ref {
			continue
		}
		if err := c.tx.Model(&model.SatelliteDetection{}).Where("detection_key IN ?", keys).Update("fire_ref", next).Error; err != nil {
			return err
		}
		for _, key := range keys {
			if row, ok := c.rows[key]; ok {
				moved := next
				row.FireRef = &moved
			}
		}
		c.recordFire(next, rows[group[0]].Key, len(group) >= 2)
	}
	return nil
}

// hazards maps every touched fire to its HazardEvent. A fire that once had two
// detections keeps its event after shrinking or merging (severity NORMAL, with a
// status) so consumers see the change instead of the event silently disappearing.
func (c *clusterer) hazards() ([]model.HazardEvent, error) {
	if err := c.flushRows(); err != nil {
		return nil, err
	}
	if err := c.flushFires(); err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(c.touched))
	for ref := range c.touched {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	fires := map[string]model.SatelliteFire{}
	members := map[string][]mapper.Detection{}
	for start := 0; start < len(refs); start += 1000 {
		chunk := refs[start:min(start+1000, len(refs))]
		var stored []model.SatelliteFire
		if err := c.tx.Where("ref IN ?", chunk).Find(&stored).Error; err != nil {
			return nil, err
		}
		for _, fire := range stored {
			fires[fire.Ref] = fire
		}
		// Ordered as members() orders one fire, so each fire's list is the same.
		var rows []model.SatelliteDetection
		if err := c.tx.Where("fire_ref IN ?", chunk).Order("fire_ref, detected_at, detection_key").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			members[*row.FireRef] = append(members[*row.FireRef], toDetection(row))
		}
	}
	anchorKeys := make([]string, 0, len(fires))
	for _, ref := range refs {
		fire, ok := fires[ref]
		if !ok {
			return nil, fmt.Errorf("satellite fire %s: %w", ref, gorm.ErrRecordNotFound)
		}
		anchorKeys = append(anchorKeys, fire.AnchorKey)
	}
	anchors := map[string]model.SatelliteDetection{}
	for start := 0; start < len(anchorKeys); start += 1000 {
		var stored []model.SatelliteDetection
		if err := c.tx.Where("detection_key IN ?", anchorKeys[start:min(start+1000, len(anchorKeys))]).Find(&stored).Error; err != nil {
			return nil, err
		}
		for _, row := range stored {
			anchors[row.Key] = row
		}
	}

	events := []model.HazardEvent{}
	for _, ref := range refs {
		fire := fires[ref]
		anchor, ok := anchors[fire.AnchorKey]
		if !ok {
			return nil, fmt.Errorf("satellite fire %s anchor %s: %w", ref, fire.AnchorKey, gorm.ErrRecordNotFound)
		}
		detections := members[ref]
		switch {
		case len(detections) >= 2:
			events = append(events, mapper.Wildfire(ref, anchor.DetectedAt.UTC(), detections))
		case fire.Formed:
			mergedInto := ""
			if fire.MergedInto != nil {
				mergedInto = *fire.MergedInto
			}
			events = append(events, mapper.RetiredWildfire(ref, toDetection(anchor), detections, mergedInto))
		}
	}
	return events, nil
}
