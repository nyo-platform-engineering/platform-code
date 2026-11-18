package storage

import (
	"context"
	"encoding/json"
	"time"

	"aat/aggregator/internal/model"
	"aat/internal/httpkit"
	"gorm.io/gorm/clause"
)

// Save stores changed hazards and queues their messages in the caller's transaction.
// An event is matched on (source, source_ref_id) and keeps its first hazard_id, so a
// later change to occurred_at updates the event instead of creating a second one.
func (p *Postgres) Save(ctx context.Context, events []model.HazardEvent) ([]model.HazardEvent, error) {
	tx := p.db.WithContext(ctx)
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
