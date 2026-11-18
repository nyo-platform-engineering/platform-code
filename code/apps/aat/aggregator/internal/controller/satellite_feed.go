package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"aat/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const SatelliteSource = "SATELIT"

// FeedEvent is one Server-Sent Event from the satellite feed.
type FeedEvent struct {
	Offset     int64
	Type       string // detection, update, retract, heartbeat
	Data       []byte
	ReceivedAt time.Time
}

// FeedResult describes one committed batch.
type FeedResult struct {
	Offset   int64 // stored offset after the batch
	Applied  int   // detection/update/retract events applied
	Skipped  int   // events at or below the stored offset (already applied)
	Rejected int   // events kept in satellite_rejects instead of applied
	Changed  []model.HazardEvent
}

// SatelliteFeedOffset returns the last applied feed offset, or nil before the first batch.
func (ctrl *Controller) SatelliteFeedOffset(ctx context.Context) (*int64, error) {
	var feed model.SatelliteFeed
	err := ctrl.DB.WithContext(ctx).Take(&feed, "source = ?", SatelliteSource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if feed.Offset < 0 { // row created by RecordFeedSession before any batch
		return nil, nil
	}
	return &feed.Offset, nil
}

// IngestFeedBatch applies events in offset order and stores the new offset in the
// same transaction, fenced by the poll owner. Events at or below the stored offset
// are skipped, so replaying a batch after a crash or reconnect cannot apply it twice.
func (ctrl *Controller) IngestFeedBatch(ctx context.Context, events []FeedEvent) (result FeedResult, err error) {
	ctx, span := telemetry.Start(ctx, "ingest SATELIT feed", trace.SpanKindInternal)
	defer func() {
		telemetry.Fail(span, err)
		span.SetAttributes(
			attribute.Int("aat.feed.applied", result.Applied),
			attribute.Int("aat.feed.rejected", result.Rejected),
			attribute.Int64("aat.feed.offset", result.Offset),
		)
		span.End()
	}()
	if ctrl.PollOwner == nil {
		return FeedResult{}, ErrPollFenced
	}
	if len(events) == 0 {
		return FeedResult{}, Invalid{fmt.Errorf("empty feed batch")}
	}

	tx := ctrl.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return FeedResult{}, tx.Error
	}
	defer tx.Rollback()
	if err := ctrl.fencePoll(tx, SatelliteSource, true); err != nil {
		return FeedResult{}, err
	}

	var stored *int64
	var feed model.SatelliteFeed
	switch err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&feed, "source = ?", SatelliteSource).Error; {
	case err == nil:
		stored = &feed.Offset
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return FeedResult{}, err
	}

	records := []model.Record{}
	last := stored
	for _, event := range events {
		if last != nil && event.Offset <= *last {
			result.Skipped++
			continue
		}
		offset := event.Offset
		last = &offset
		if event.Type == "heartbeat" {
			continue
		}
		record, reason := feedRecord(event)
		if reason == nil {
			if _, reason = mapper.Satellite(record); reason == nil {
				records = append(records, record)
				continue
			}
		}
		result.Rejected++
		reject := model.SatelliteReject{
			Offset: event.Offset, EventType: event.Type, Payload: string(event.Data),
			Reason: reason.Error(), ReceivedAt: event.ReceivedAt.UTC(),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reject).Error; err != nil {
			return FeedResult{}, err
		}
	}
	if last == nil || (stored != nil && *last == *stored) {
		result.Offset = valueOr(stored, -1)
		return result, nil // nothing new; nothing to commit
	}

	if len(records) > 0 {
		hazards, err := ctrl.satelliteHazards(tx, records)
		if err != nil {
			return FeedResult{}, err
		}
		if result.Changed, err = storage.NewPostgres(tx, storage.Identity{}).Save(ctx, hazards); err != nil {
			return FeedResult{}, err
		}
	}
	result.Applied = len(records)

	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{"feed_offset", "updated_at"}),
	}).Create(&model.SatelliteFeed{Source: SatelliteSource, Offset: *last, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		return FeedResult{}, err
	}
	if err := tx.Commit().Error; err != nil {
		return FeedResult{}, err
	}
	result.Offset = *last
	return result, nil
}

// RecordFeedSession stores what the feed reports about itself, for the lag figure.
func (ctrl *Controller) RecordFeedSession(ctx context.Context, latestOffset int64, virtualNow *time.Time) error {
	if ctrl.PollOwner == nil {
		return ErrPollFenced
	}
	return ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ctrl.fencePoll(tx, SatelliteSource, true); err != nil {
			return err
		}
		now := time.Now().UTC()
		return tx.Exec(`INSERT INTO satellite_feeds (source, feed_offset, latest_offset, virtual_now, checked_at, updated_at)
			VALUES (?, -1, ?, ?, ?, ?)
			ON CONFLICT (source) DO UPDATE SET latest_offset = excluded.latest_offset,
			virtual_now = excluded.virtual_now, checked_at = excluded.checked_at`,
			SatelliteSource, latestOffset, virtualNow, now, now).Error
	})
}

// feedRecord turns an SSE payload into the flat record the mapper reads. The SSE
// event name decides event_type. A payload that nests the detection under
// "detection" or "data" is flattened first.
func feedRecord(event FeedEvent) (model.Record, error) {
	if event.Type != mapper.SatelliteDetected && event.Type != mapper.SatelliteUpdated && event.Type != mapper.SatelliteRetracted {
		return nil, fmt.Errorf("unknown event type %q", event.Type)
	}
	decoder := json.NewDecoder(bytes.NewReader(event.Data))
	decoder.UseNumber()
	var record model.Record
	if err := decoder.Decode(&record); err != nil || record == nil {
		return nil, fmt.Errorf("payload is not a JSON object")
	}
	for _, key := range []string{"detection", "data"} {
		nested, ok := record[key]
		if !ok {
			continue
		}
		var inner model.Record
		if json.Unmarshal(nested, &inner) == nil && inner != nil {
			delete(record, key)
			for k, v := range inner {
				if _, exists := record[k]; !exists {
					record[k] = v
				}
			}
		}
	}
	kind, _ := json.Marshal(event.Type)
	record["event_type"] = kind
	return record, nil
}

func valueOr(v *int64, fallback int64) int64 {
	if v == nil {
		return fallback
	}
	return *v
}

type SatelliteFeedStatus struct {
	Offset       *int64     `json:"offset"`
	LatestOffset *int64     `json:"latest_offset"`
	Lag          *int64     `json:"lag_events"`
	VirtualNow   *time.Time `json:"virtual_now"`
	CheckedAt    *time.Time `json:"checked_at"`
	UpdatedAt    *time.Time `json:"updated_at"`
	Rejected     int64      `json:"rejected_events"`
}

func (ctrl *Controller) SatelliteFeedStatus(ctx context.Context) (SatelliteFeedStatus, error) {
	var status SatelliteFeedStatus
	var feed model.SatelliteFeed
	err := ctrl.DB.WithContext(ctx).Take(&feed, "source = ?", SatelliteSource).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return status, err
	}
	if err == nil {
		if feed.Offset >= 0 {
			status.Offset = &feed.Offset
		}
		status.LatestOffset, status.VirtualNow, status.CheckedAt = feed.LatestOffset, utc(feed.VirtualNow), utc(feed.CheckedAt)
		updated := feed.UpdatedAt.UTC()
		status.UpdatedAt = &updated
		if feed.LatestOffset != nil {
			lag := max(*feed.LatestOffset-feed.Offset, 0)
			status.Lag = &lag
		}
	}
	err = ctrl.DB.WithContext(ctx).Model(&model.SatelliteReject{}).Count(&status.Rejected).Error
	return status, err
}
