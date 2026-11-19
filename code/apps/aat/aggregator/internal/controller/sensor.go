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
	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const SensorSource = "SENSOR_PANTAI"

// sensorBatchKey is the inbox batch key of one GET /v1/observations response.
const sensorBatchKey = "observations"

// SaveSensorStations replaces the cached station list used to validate readings.
func (ctrl *Controller) SaveSensorStations(ctx context.Context, stations []mapper.Station) error {
	if len(stations) == 0 {
		return Invalid{fmt.Errorf("empty station list")}
	}
	now := time.Now().UTC()
	rows := make([]model.SensorStation, 0, len(stations))
	for _, s := range stations {
		if s.ID == "" || s.Province == "" || s.Latitude < -90 || s.Latitude > 90 || s.Longitude < -180 || s.Longitude > 180 {
			return Invalid{fmt.Errorf("invalid station %q", s.ID)}
		}
		rows = append(rows, model.SensorStation{ID: s.ID, Name: s.Name, Province: s.Province, Latitude: s.Latitude, Longitude: s.Longitude, UpdatedAt: now})
	}
	return ctrl.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "station_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "province", "latitude", "longitude", "updated_at"}),
	}).Create(&rows).Error
}

func sensorStations(tx *gorm.DB) (map[string]mapper.Station, error) {
	var rows []model.SensorStation
	if err := tx.Find(&rows).Error; err != nil {
		return nil, err
	}
	stations := make(map[string]mapper.Station, len(rows))
	for _, r := range rows {
		stations[r.ID] = mapper.Station{ID: r.ID, Name: r.Name, Province: r.Province, Latitude: r.Latitude, Longitude: r.Longitude}
	}
	return stations, nil
}

// SensorStationCount reports whether the poller has a station list yet.
func (ctrl *Controller) SensorStationCount(ctx context.Context) (int64, error) {
	var n int64
	err := ctrl.DB.WithContext(ctx).Model(&model.SensorStation{}).Count(&n).Error
	return n, err
}

// CaptureSensorPoll stores one raw observations response in the inbox and moves the
// cursor past it in the same fenced transaction. The raw bytes are kept before any
// decoding, so a broken response is parked with its reason, never lost.
func (ctrl *Controller) CaptureSensorPoll(ctx context.Context, raw []byte, nextCursor string) error {
	if len(raw) > MaxCaptureBytes {
		return Invalid{fmt.Errorf("capture limit exceeded")}
	}
	return ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ctrl.fencePoll(tx, SensorSource, true); err != nil {
			return err
		}
		message := newInboxMessage(ctx, SensorSource, sensorBatchKey, "envelope", raw)
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).Create(&message).Error; err != nil {
			return err
		}
		if nextCursor == "" {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source"}},
			DoUpdates: clause.AssignmentColumns([]string{"cursor", "updated_at"}),
		}).Create(&model.SensorState{Source: SensorSource, Cursor: nextCursor, Breaker: "closed", UpdatedAt: time.Now().UTC()}).Error
	})
}

// SensorState returns the stored poller state; a zero state before the first poll.
func (ctrl *Controller) SensorState(ctx context.Context) (model.SensorState, error) {
	state := model.SensorState{Source: SensorSource, Breaker: "closed"}
	err := ctrl.DB.WithContext(ctx).Take(&state, "source = ?", SensorSource).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.SensorState{}, err
	}
	for _, t := range []**time.Time{&state.OpenUntil, &state.LastSuccessAt, &state.LastAttemptAt, &state.GeneratedAt, &state.NewestObservedAt} {
		*t = utc(*t)
	}
	return state, nil
}

// RecordSensorState saves the breaker and freshness view. The cursor is owned by
// CaptureSensorPoll and is not touched here.
func (ctrl *Controller) RecordSensorState(ctx context.Context, state model.SensorState) error {
	if ctrl.PollOwner == nil {
		return ErrPollFenced
	}
	return ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ctrl.fencePoll(tx, SensorSource, true); err != nil {
			return err
		}
		state.Source, state.UpdatedAt = SensorSource, time.Now().UTC()
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "source"}},
			DoUpdates: clause.AssignmentColumns([]string{"breaker", "consecutive_failures", "open_until", "last_success_at",
				"last_attempt_at", "last_error", "generated_at", "newest_observed_at", "stale", "stale_reason", "requests", "updated_at"}),
		}).Create(&state).Error
	})
}

// decodeSensorEnvelope returns the observation records of one sensor response.
func decodeSensorEnvelope(raw []byte) ([]json.RawMessage, error) {
	if err := boundedPayloadError(raw, MaxCaptureBytes); err != nil {
		return nil, err
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("expected_response_object")
	}
	var page struct {
		Items *[]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || page.Items == nil {
		return nil, fmt.Errorf("expected_items_array")
	}
	return *page.Items, nil
}

// criticalFirst moves readings flagged critical to the front of a page, keeping the
// order inside each group. Records are processed one transaction at a time in the
// order they are queued, so when the sensor sends a burst ("everything at once") a
// critical reading no longer waits behind hundreds of ordinary ones. Each reading
// has its own observation_id, so per-source_ref_id ordering is unaffected.
func criticalFirst(records []json.RawMessage) []json.RawMessage {
	ordered := make([]json.RawMessage, 0, len(records))
	var rest []json.RawMessage
	for _, raw := range records {
		var r model.Record
		if json.Unmarshal(raw, &r) == nil && mapper.CoastalCritical(r) {
			ordered = append(ordered, raw)
		} else {
			rest = append(rest, raw)
		}
	}
	return append(ordered, rest...)
}

// criticalAlert is returned for a reading that breaks the contract yet is flagged
// critical: the reading is parked like any other violation, and an unverified
// alert still goes to the Field Team so no critical event is lost.
type criticalAlert struct {
	Invalid
	alert model.HazardEvent
}

// Unwrap lets the inbox treat it as Invalid, so the reading is parked, not retried.
func (c criticalAlert) Unwrap() error { return c.Invalid }

func (ctrl *Controller) processSensorRecord(ctx context.Context, tx *gorm.DB, message model.InboxMessage) (int, error) {
	record, err := decodeInboxRecord(message.Raw)
	if err != nil {
		return 0, Invalid{err}
	}
	stations, err := sensorStations(tx)
	if err != nil {
		return 0, err
	}
	hazard, err := mapper.Coastal(record, stations)
	if err != nil {
		if mapper.CoastalCritical(record) {
			return 0, criticalAlert{Invalid{err}, unverifiedAlert(record, stations, message, err)}
		}
		return 0, Invalid{err}
	}
	// Latency is measured from arrival at BNPB; ingested_at is not part of the
	// change check, so a re-sent reading still produces no new message.
	hazard.Ingested = message.ReceivedAt.UTC()
	changed, err := storage.NewPostgres(tx, storage.Identity{}).Save(ctx, []model.HazardEvent{hazard})
	return len(changed), err
}

// unverifiedAlert shapes an alert for a broken critical reading like a hazard, so
// the notifier can read it, without storing it as a HazardEvent.
func unverifiedAlert(record model.Record, stations map[string]mapper.Station, message model.InboxMessage, reason error) model.HazardEvent {
	var id, stationID string
	_ = json.Unmarshal(record["observation_id"], &id)
	_ = json.Unmarshal(record["station_id"], &stationID)
	if id == "" {
		id = message.MessageID[:16]
	}
	h := mapper.Base(SensorSource, "unverified-"+id, "COASTAL", message.ReceivedAt)
	h.ID = "unverified-" + h.ID
	h.Severity, h.Area = "AWAS", "unknown"
	h.Ingested = message.ReceivedAt.UTC()
	if s, ok := stations[stationID]; ok {
		h.Area, h.Latitude, h.Longitude = s.Province, s.Latitude, s.Longitude
	}
	h.Attributes = model.Record{}
	for key, value := range map[string]any{"verified": false, "critical": true, "observation_id": id,
		"station_id": stationID, "reason": reason.Error(), "inbox_message_id": message.MessageID} {
		b, _ := json.Marshal(value)
		h.Attributes[key] = b
	}
	return h
}

// queueUnverifiedAlert puts the alert on the critical subject only.
func queueUnverifiedAlert(ctx context.Context, tx *gorm.DB, alert model.HazardEvent) error {
	payload, err := json.Marshal(alert)
	if err != nil {
		return err
	}
	carrier := propagation.MapCarrier{}
	telemetry.Propagation.Inject(ctx, carrier)
	correlationID := httpkit.CorrelationID(ctx)
	if correlationID == "" {
		correlationID = httpkit.ID()
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_key"}}, DoNothing: true}).Create(&model.HazardOutbox{
		EventKey: alert.ID, CorrelationID: correlationID, TraceParent: carrier.Get("traceparent"),
		TraceState: carrier.Get("tracestate"), Payload: payload, Lane: "critical_only",
	}).Error
}

// SensorStatusHandler serves the poller state for operators and the Client API.
func (ctrl *Controller) SensorStatusHandler(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	state, err := ctrl.SensorState(ctx)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	stations, err := ctrl.SensorStationCount(ctx)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	c.JSON(200, gin.H{"sensor": state, "available": sensorAvailable(state, time.Now()), "stations": stations})
}

// sensorAvailable: data is being fetched and is fresh. Only this decides health;
// the sensor's own /healthz is not trusted (it kept answering ok during outages).
func sensorAvailable(state model.SensorState, now time.Time) bool {
	return state.Breaker == "closed" && !state.Stale && state.LastSuccessAt != nil && now.Sub(*state.LastSuccessAt) < 30*time.Second
}
