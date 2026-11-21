package controller

// AI-assisted implementation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
	"unicode/utf8"

	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CaptureRawPoll commits the original bytes before any JSON decoding.
func (ctrl *Controller) CaptureRawPoll(ctx context.Context, source string, payloads []RawPayload) error {
	if len(payloads) == 0 {
		return Invalid{fmt.Errorf("empty poll")}
	}
	for _, payload := range payloads {
		if !validBatchKey(source, payload.BatchKey) || len(payload.Raw) > MaxCaptureBytes {
			return Invalid{fmt.Errorf("invalid batch key or capture limit exceeded")}
		}
	}
	var proof pollWriteEvidence
	err := ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ctrl.checkPollFence(tx, source, true, &proof); err != nil {
			return err
		}
		for _, payload := range payloads {
			message := newInboxMessage(ctx, source, payload.BatchKey, "envelope", payload.Raw)
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).Create(&message).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`INSERT INTO poll_states (source, healthy, last_success_at)
			VALUES (?, true, clock_timestamp()) ON CONFLICT (source) DO UPDATE
			SET healthy = true, last_success_at = excluded.last_success_at, last_error_at = NULL, last_error = NULL`, source).Error
	})
	ctrl.logPollWrite(ctx, source, "raw_capture", proof, err)
	return err
}

func newInboxMessage(ctx context.Context, source, key, kind string, raw []byte) model.InboxMessage {
	if raw == nil {
		raw = []byte{}
	}
	carrier := propagation.MapCarrier{}
	telemetry.Propagation.Inject(ctx, carrier)
	return model.InboxMessage{MessageID: inboxID(source, key, kind, raw), Source: source, BatchKey: key, Kind: kind,
		Raw: raw, Status: "retrying", CorrelationID: httpkit.CorrelationID(ctx), TraceParent: carrier.Get("traceparent"), TraceState: carrier.Get("tracestate")}
}

// ProcessInbox commits each record's effects and processed status together.
func (ctrl *Controller) ProcessInbox(ctx context.Context, source string, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, Invalid{fmt.Errorf("invalid processing limit")}
	}
	changed := 0
	for range limit {
		found := false
		delta := 0
		var proof pollWriteEvidence
		operation := "inbox_process"
		writeCtx := ctx
		err := ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := ctrl.checkPollFence(tx, source, true, &proof); err != nil {
				return err
			}
			var message model.InboxMessage
			result := tx.Raw(`SELECT m.* FROM inbox_messages m WHERE m.source = ? AND m.status = 'retrying'
				AND m.next_attempt_at <= clock_timestamp()
				AND (m.kind = 'envelope' OR m.source_ref_id = '' OR NOT EXISTS (
					SELECT 1 FROM inbox_messages older WHERE older.source = m.source AND older.kind = 'record'
					AND older.source_ref_id = m.source_ref_id AND older.sequence < m.sequence AND older.status <> 'processed'))
				ORDER BY m.sequence LIMIT 1 FOR UPDATE OF m SKIP LOCKED`, source).Scan(&message)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			found = true
			messageCtx := telemetry.Propagation.Extract(ctx, propagation.MapCarrier{"traceparent": message.TraceParent, "tracestate": message.TraceState})
			messageCtx = httpkit.WithCorrelationID(messageCtx, message.CorrelationID)
			writeCtx = messageCtx
			operation = "inbox_" + message.Kind
			name := "ingest " + message.Source
			if message.Kind == "envelope" {
				name = "inbox expand " + message.Source
			}
			messageCtx, span := telemetry.Start(messageCtx, name, trace.SpanKindInternal)
			defer span.End()
			tx = tx.WithContext(messageCtx)
			if err := tx.SavePoint("inbox_effect").Error; err != nil {
				return err
			}
			var count int
			var err error
			if message.Kind == "envelope" {
				err = ctrl.expandEnvelope(messageCtx, tx, message)
			} else {
				count, err = ctrl.processRecord(messageCtx, tx, message)
			}
			status, reason, delay := "processed", "", time.Duration(0)
			if err != nil {
				telemetry.Fail(span, err)
				if rollback := tx.RollbackTo("inbox_effect").Error; rollback != nil {
					return rollback
				}
				// A broken reading flagged critical is parked, but its alert still goes out.
				var alert criticalAlert
				if errors.As(err, &alert) {
					if err := queueUnverifiedAlert(messageCtx, tx, alert.alert); err != nil {
						return err
					}
				}
				var invalid Invalid
				if errors.As(err, &invalid) {
					status, reason = "parked", "schema_incompatible: "+invalid.Error()
				} else {
					status, reason = "retrying", "processing_unavailable"
					delay = time.Second*time.Duration(1<<min(message.Attempts+1, 6)) + time.Duration(rand.IntN(500))*time.Millisecond
				}
				count = 0
			}
			if len(reason) > 1024 {
				reason = reason[:1024]
				for !utf8.ValidString(reason) {
					reason = reason[:len(reason)-1]
				}
			}
			updates := map[string]any{"status": status, "reason": reason, "attempts": gorm.Expr("attempts + 1"),
				"next_attempt_at": gorm.Expr("clock_timestamp() + (? * interval '1 millisecond')", delay.Milliseconds())}
			if status == "processed" {
				updates["raw"] = []byte{}
			}
			if err := tx.Model(&model.InboxMessage{}).Where("sequence = ?", message.Sequence).Updates(updates).Error; err != nil {
				return err
			}
			delta = count
			return nil
		})
		if found || err != nil {
			ctrl.logPollWrite(writeCtx, source, operation, proof, err)
		}
		if err != nil {
			return changed, err
		}
		changed += delta
		if !found {
			break
		}
	}
	return changed, nil
}

func (ctrl *Controller) expandEnvelope(ctx context.Context, tx *gorm.DB, envelope model.InboxMessage) error {
	decode := decodeEnvelope
	if envelope.Source == SensorSource {
		decode = decodeSensorEnvelope
	}
	records, err := decode(envelope.Raw)
	if err != nil {
		return Invalid{err}
	}
	if envelope.Source == SensorSource {
		records = criticalFirst(records)
	}
	var cursor *time.Time
	for _, raw := range records {
		child := newInboxMessage(ctx, envelope.Source, envelope.BatchKey, "record", raw)
		child.SourceRefID = inboxRef(envelope.BatchKey, raw)
		child.ReceivedAt = envelope.ReceivedAt
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).Create(&child).Error; err != nil {
			return err
		}
		field := ""
		if envelope.BatchKey == "seismic_events" {
			field = "occurred_at"
		}
		if envelope.BatchKey == "volcanic_reports" {
			field = "reported_at"
		}
		if field != "" {
			var record model.Record
			var stamp time.Time
			if json.Unmarshal(raw, &record) == nil && json.Unmarshal(record[field], &stamp) == nil && !stamp.IsZero() && (cursor == nil || stamp.After(*cursor)) {
				stamp = stamp.UTC()
				cursor = &stamp
			}
		}
	}
	if cursor != nil {
		return tx.Model(&model.PollState{}).Where("source = ?", envelope.Source).
			Update("fetch_cursor", gorm.Expr("GREATEST(fetch_cursor, ?)", cursor)).Error
	}
	return nil
}

func (ctrl *Controller) processRecord(ctx context.Context, tx *gorm.DB, message model.InboxMessage) (int, error) {
	if message.Source == SensorSource {
		return ctrl.processSensorRecord(ctx, tx, message)
	}
	record, err := decodeInboxRecord(message.Raw)
	if err != nil {
		return 0, Invalid{err}
	}
	events, err := ctrl.agencyHazards(tx, message.Source, map[string][]model.Record{message.BatchKey: {record}})
	if err != nil {
		return 0, err
	}
	changed, err := storage.NewPostgres(tx, storage.Identity{}).Save(ctx, events)
	return len(changed), err
}
