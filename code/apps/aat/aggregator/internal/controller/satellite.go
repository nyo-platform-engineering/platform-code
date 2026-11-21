package controller

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
