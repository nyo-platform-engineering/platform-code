package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"
	"aat/internal/httpkit"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Invalid struct {
	Err error
}

func (invalid Invalid) Error() string {
	return invalid.Err.Error()
}

func (ctrl *Controller) IngestBatch(ctx context.Context, source string, batch map[string][]model.Record) ([]model.HazardEvent, error) {
	return ctrl.ingestBatch(ctx, source, batch, nil)
}

// IngestPollBatch saves hazards, outbox messages, and the cursor in one transaction.
func (ctrl *Controller) IngestPollBatch(ctx context.Context, source string, batch map[string][]model.Record, cursor *time.Time) ([]model.HazardEvent, error) {
	if ctrl.PollOwner == nil {
		return nil, ErrPollFenced
	}
	return ctrl.ingestBatch(ctx, source, batch, &pollCompletion{cursor: cursor})
}

type pollCompletion struct{ cursor *time.Time }

func (ctrl *Controller) ingestBatch(ctx context.Context, source string, batch map[string][]model.Record, completion *pollCompletion) ([]model.HazardEvent, error) {
	if len(batch) == 0 {
		return nil, Invalid{fmt.Errorf("empty batch")}
	}

	for key := range batch {
		if source == "BMKG" && (key == "seismic_events" || key == "tsunami_warnings") {
			continue
		}

		if source == "PVMBG" && key == "volcanic_reports" {
			continue
		}

		// Feed events stay in one ordered list because their order changes fire membership.
		if source == "SATELIT" && key == "satellite_events" {
			continue
		}

		return nil, Invalid{fmt.Errorf("unexpected batch key %s", key)}
	}

	tx := ctrl.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}

	defer tx.Rollback()
	if ctrl.PollOwner != nil {
		if err := ctrl.fencePoll(tx, source, true); err != nil {
			return nil, err
		}
	}

	var events []model.HazardEvent
	var err error
	if source == "SATELIT" {
		events, err = ctrl.satelliteHazards(tx, batch["satellite_events"])
	} else {
		events, err = agencyHazards(tx, source, batch)
	}
	if err != nil {
		return nil, err
	}

	changedEvents, err := saveHazards(ctx, tx, events)
	if err != nil {
		return nil, err
	}

	if completion != nil {
		if err := tx.Exec(`INSERT INTO poll_states
			(source, healthy, last_success_at, fetch_cursor)
			VALUES (?, true, clock_timestamp(), ?)
			ON CONFLICT (source) DO UPDATE SET healthy = true,
			last_success_at = excluded.last_success_at, last_error_at = NULL,
			last_error = NULL, fetch_cursor = GREATEST(poll_states.fetch_cursor, excluded.fetch_cursor)`, source, completion.cursor).Error; err != nil {
			return nil, err
		}
	}

	if err = tx.Commit().Error; err != nil {
		return nil, err
	}

	return changedEvents, nil
}

// agencyHazards maps BMKG and PVMBG batches, storing the raw BMKG records they correlate with.
func agencyHazards(tx *gorm.DB, source string, batch map[string][]model.Record) ([]model.HazardEvent, error) {
	var err error

	// Validate the source records and collect the event IDs that need locking.
	affectedEvents := map[string]bool{}
	for _, record := range batch["seismic_events"] {
		hazard, err := mapper.Seismic(record, nil)
		if err != nil {
			return nil, Invalid{err}
		}

		affectedEvents[hazard.Ref] = true
	}

	for _, warningRecord := range batch["tsunami_warnings"] {
		_, ref, err := mapper.Tsunami(warningRecord)
		if err != nil {
			return nil, Invalid{err}
		}

		affectedEvents[ref] = true
	}

	events := []model.HazardEvent{}
	for _, record := range batch["volcanic_reports"] {
		hazard, err := mapper.Volcanic(record)
		if err != nil {
			return nil, Invalid{err}
		}

		affectedEvents[hazard.Ref] = true
		events = append(events, hazard)
	}

	eventRefs := []string{}
	for key := range affectedEvents {
		eventRefs = append(eventRefs, key)
	}

	// Lock affected events in a stable order across replicas to prevent deadlocks.
	sort.Strings(eventRefs)
	for _, key := range eventRefs {
		if err = tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", source+":"+key).Error; err != nil {
			return nil, err
		}
	}

	// Keep BMKG payloads so warnings can be correlated with their seismic events.
	for _, record := range batch["seismic_events"] {
		id, _ := mapper.String(record, "event_id")
		seismicEvent := model.SeismicEvent{ID: id, Document: record}
		if err = tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"document"}),
		}).Create(&seismicEvent).Error; err != nil {
			return nil, err
		}
	}
	for _, warningRecord := range batch["tsunami_warnings"] {
		id, ref, _ := mapper.Tsunami(warningRecord)
		warning := model.TsunamiWarning{ID: id, EventID: ref, Document: warningRecord}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"document"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "tsunami_warnings.event_id = excluded.event_id"}}},
		}).Create(&warning)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			return nil, Invalid{fmt.Errorf("warning related_event_id is immutable")}
		}
	}

	// Remap affected seismic events with every warning already stored for them.
	if source == "BMKG" {
		for _, ref := range eventRefs {
			var seismicEvent model.SeismicEvent
			err = tx.Take(&seismicEvent, "id = ?", ref).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			var warnings []model.TsunamiWarning
			if err = tx.Where("event_id = ?", ref).Order("id").Find(&warnings).Error; err != nil {
				return nil, err
			}
			warningRecords := []model.Record{}
			for _, warning := range warnings {
				warningRecords = append(warningRecords, warning.Document)
			}
			record := seismicEvent.Document

			hazard, err := mapper.Seismic(record, warningRecords)
			if err != nil {
				return nil, Invalid{err}
			}

			events = append(events, hazard)
		}
	}

	return events, nil
}

// saveHazards stores changed hazards and queues their messages in the caller's transaction.
// An event is matched on (source, source_ref_id) and keeps its first hazard_id, so a
// later change to occurred_at updates the event instead of creating a second one.
func saveHazards(ctx context.Context, tx *gorm.DB, events []model.HazardEvent) ([]model.HazardEvent, error) {
	changedEvents := []model.HazardEvent{}
	for _, hazard := range events {
		result := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "source"}, {Name: "source_ref_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"hazard_type", "severity", "area_name",
				"latitude", "longitude", "occurred_at", "ingested_at", "attributes",
			}),
			Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: hazardChanged}}},
		}, clause.Returning{Columns: []clause.Column{{Name: "id"}}}).Create(&hazard)

		if result.Error != nil {
			return nil, result.Error
		}

		if result.RowsAffected > 0 {
			payload, err := json.Marshal(hazard)
			if err != nil {
				return nil, err
			}
			eventKey := hazard.ID + "-" + hazard.Ingested.UTC().Format(time.RFC3339Nano)
			correlationID := httpkit.CorrelationID(ctx)
			if correlationID == "" {
				correlationID = httpkit.ID()
			}
			outbox := model.HazardOutbox{EventKey: eventKey, CorrelationID: correlationID, Payload: json.RawMessage(payload)}
			if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_key"}}, DoNothing: true}).Create(&outbox).Error; err != nil {
				return nil, err
			}

			changedEvents = append(changedEvents, hazard)
		}
	}

	return changedEvents, nil
}
