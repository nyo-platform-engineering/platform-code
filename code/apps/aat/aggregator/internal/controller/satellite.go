package controller

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultSatelliteLateness is how far behind the newest detection time a new
// detection may arrive and still update its fire. Later ones are kept and counted.
const DefaultSatelliteLateness = 6 * time.Hour

func (ctrl *Controller) satelliteLateness() time.Duration {
	if ctrl.SatelliteLateness > 0 {
		return ctrl.SatelliteLateness
	}
	return DefaultSatelliteLateness
}

func eligible(row *model.SatelliteDetection) bool {
	return !row.Late && !row.Retracted && row.Confidence != "l" && row.Confidence != ""
}

func toDetection(row model.SatelliteDetection) mapper.Detection {
	return mapper.Detection{
		Key: row.Key, Satellite: row.Satellite, DetectedAt: row.DetectedAt.UTC(),
		Latitude: row.Latitude, Longitude: row.Longitude, Confidence: row.Confidence, FRP: row.FRP,
	}
}

// satelliteHazards applies feed events in stream order and returns the fires they changed.
// Every live fire's ref is FireRef(key) of one of its own detections (its anchor), which
// keeps refs unique: merges keep the fire with the earliest detection, and on a split
// only the part still holding the anchor keeps the ref.
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
	rows := map[string]*model.SatelliteDetection{}
	for start := 0; start < len(keys); start += 1000 {
		var stored []model.SatelliteDetection
		if err := tx.Where("detection_key IN ?", keys[start:min(start+1000, len(keys))]).Find(&stored).Error; err != nil {
			return nil, err
		}
		for i := range stored {
			rows[stored[i].Key] = &stored[i]
		}
	}

	c := clusterer{tx: tx, touched: map[string]bool{}}
	now := time.Now().UTC()
	for _, event := range events {
		d := event.Detection
		row, found := rows[d.Key]
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
			if eligible(row) {
				if err := c.join(row); err != nil {
					return nil, err
				}
			}
			if err := tx.Create(row).Error; err != nil {
				return nil, err
			}
			rows[d.Key] = row
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
		if err := tx.Save(row).Error; err != nil {
			return nil, err
		}
		if previous != nil && row.FireRef == nil {
			if err := c.split(*previous); err != nil {
				return nil, err
			}
		}
	}
	return c.hazards()
}

// clusterer keeps fire state in the database only, so the resulting events are the
// same however the stream is split into batches.
type clusterer struct {
	tx      *gorm.DB
	touched map[string]bool // fires whose HazardEvent must be recomputed
}

// recordFire creates or revives a fire. A revived fire is no longer merged into another.
func (c *clusterer) recordFire(ref, anchorKey string, formed bool) error {
	c.touched[ref] = true
	return c.tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "ref"}},
		DoUpdates: clause.Set{
			{Column: clause.Column{Name: "formed"}, Value: gorm.Expr("satellite_fires.formed OR excluded.formed")},
			{Column: clause.Column{Name: "merged_into"}, Value: nil},
		},
	}).Create(&model.SatelliteFire{Ref: ref, AnchorKey: anchorKey, Formed: formed}).Error
}

func (c *clusterer) members(ref string) ([]model.SatelliteDetection, error) {
	var rows []model.SatelliteDetection
	err := c.tx.Where("fire_ref = ?", ref).Order("detected_at, detection_key").Find(&rows).Error
	return rows, err
}

// join attaches an eligible detection to every fire it links to, merging them.
func (c *clusterer) join(row *model.SatelliteDetection) error {
	latMin, latMax, lonMin, lonMax := mapper.NeighborBox(row.Latitude, row.Longitude)
	var candidates []model.SatelliteDetection
	if err := c.tx.Where(
		"fire_ref IS NOT NULL AND detection_key <> ? AND detected_at BETWEEN ? AND ? AND latitude BETWEEN ? AND ? AND longitude BETWEEN ? AND ?",
		row.Key, row.DetectedAt.Add(-mapper.LinkWindow), row.DetectedAt.Add(mapper.LinkWindow), latMin, latMax, lonMin, lonMax,
	).Find(&candidates).Error; err != nil {
		return err
	}
	self := toDetection(*row)
	linked := map[string]bool{}
	for _, candidate := range candidates {
		if mapper.Linked(self, toDetection(candidate)) {
			linked[*candidate.FireRef] = true
		}
	}
	if len(linked) == 0 {
		ref := mapper.FireRef(row.Key)
		row.FireRef = &ref
		return c.recordFire(ref, row.Key, false)
	}

	refs := make([]string, 0, len(linked))
	for ref := range linked {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	survivor := refs[0]
	if len(refs) > 1 {
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
		survivor = firsts[0].FireRef
		for _, ref := range refs {
			if ref == survivor {
				continue
			}
			if err := c.tx.Model(&model.SatelliteDetection{}).Where("fire_ref = ?", ref).Update("fire_ref", survivor).Error; err != nil {
				return err
			}
			if err := c.tx.Model(&model.SatelliteFire{}).Where("ref = ?", ref).Update("merged_into", survivor).Error; err != nil {
				return err
			}
			c.touched[ref] = true
		}
	}
	row.FireRef = &survivor
	c.touched[survivor] = true
	// The fire now has at least the neighbour and this detection.
	return c.tx.Model(&model.SatelliteFire{}).Where("ref = ?", survivor).Update("formed", true).Error
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
		if err := c.recordFire(next, rows[group[0]].Key, len(group) >= 2); err != nil {
			return err
		}
	}
	return nil
}

// hazards maps every touched fire to its HazardEvent. A fire that once had two
// detections keeps its event after shrinking or merging (severity NORMAL, with a
// status) so consumers see the change instead of the event silently disappearing.
func (c *clusterer) hazards() ([]model.HazardEvent, error) {
	refs := make([]string, 0, len(c.touched))
	for ref := range c.touched {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	events := []model.HazardEvent{}
	for _, ref := range refs {
		var fire model.SatelliteFire
		if err := c.tx.Take(&fire, "ref = ?", ref).Error; err != nil {
			return nil, err
		}
		var anchor model.SatelliteDetection
		if err := c.tx.Take(&anchor, "detection_key = ?", fire.AnchorKey).Error; err != nil {
			return nil, err
		}
		rows, err := c.members(ref)
		if err != nil {
			return nil, err
		}
		detections := make([]mapper.Detection, len(rows))
		for i, row := range rows {
			detections[i] = toDetection(row)
		}
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

type SatelliteStats struct {
	Fires            int64      `json:"fires"`
	SingleDetections int64      `json:"single_detections"`
	LateDetections   int64      `json:"late_detections"`
	LowConfidence    int64      `json:"low_confidence_detections"`
	Retracted        int64      `json:"retracted_detections"`
	NewestDetection  *time.Time `json:"newest_detection_at"`
}

// satelliteStatsTimeout bounds the stats request. The counts scan every stored
// detection (millions over a long feed), so they get this budget instead of the
// cluster's 2 s statement_timeout, which is meant for the request paths.
const satelliteStatsTimeout = 5 * time.Second

// SatelliteCounts reports what the spec requires to be counted rather than dropped.
func (ctrl *Controller) SatelliteCounts(ctx context.Context) (SatelliteStats, error) {
	var stats SatelliteStats
	err := ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SET LOCAL lasts until the end of this read-only transaction. The satellite
		// tables live on the coordinator, so nothing needs propagating to workers.
		if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", satelliteStatsTimeout.Milliseconds())).Error; err != nil {
			return err
		}
		// Serial on purpose: measured on the lab at 1.7M detections, the plan ran in
		// 1.0 s serial vs 1.8–2.6 s with parallel workers, and parallel workers were
		// still cancelled at the cluster's 2 s default despite the SET LOCAL above.
		if err := tx.Exec("SET LOCAL max_parallel_workers_per_gather = 0").Error; err != nil {
			return err
		}
		// One pass over the fires gives both counts; grouping twice doubled the cost.
		return tx.Raw(`
			SELECT f.fires, f.single_detections, d.late_detections, d.low_confidence, d.retracted, d.newest_detection
			FROM (SELECT count(*) FILTER (WHERE n >= 2) AS fires, count(*) FILTER (WHERE n = 1) AS single_detections
			      FROM (SELECT count(*) AS n FROM satellite_detections WHERE fire_ref IS NOT NULL GROUP BY fire_ref) g) f,
			     (SELECT count(*) FILTER (WHERE late) AS late_detections,
			             count(*) FILTER (WHERE confidence = 'l') AS low_confidence,
			             count(*) FILTER (WHERE retracted) AS retracted,
			             max(detected_at) AS newest_detection
			      FROM satellite_detections) d`).Scan(&stats).Error
	}, &sql.TxOptions{ReadOnly: true})
	if stats.NewestDetection != nil {
		utc := stats.NewestDetection.UTC()
		stats.NewestDetection = &utc
	}
	return stats, err
}

func (ctrl *Controller) SatelliteStatsHandler(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), satelliteStatsTimeout)
	defer cancel()
	stats, err := ctrl.SatelliteCounts(ctx)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	feed, err := ctrl.SatelliteFeedStatus(ctx)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	c.JSON(200, struct {
		SatelliteStats
		Feed SatelliteFeedStatus `json:"feed"`
	}{stats, feed})
}
