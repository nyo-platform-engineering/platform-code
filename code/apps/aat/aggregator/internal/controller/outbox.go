package controller

import (
	"context"
	"time"

	"aat/aggregator/internal/model"

	"gorm.io/gorm"
)

func (ctrl *Controller) ClaimOutbox(ctx context.Context, limit int, lease time.Duration) ([]model.OutboxMessage, error) {
	tx := ctrl.DB.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	items := []model.OutboxMessage{}
	err = tx.Raw(`WITH selected AS (
        SELECT id FROM hazard_outboxes
        WHERE published_at IS NULL AND (lease_until IS NULL OR lease_until < now())
        ORDER BY id LIMIT ? FOR UPDATE SKIP LOCKED
    )
    UPDATE hazard_outboxes AS outbox
    SET lease_until = now() + (? * interval '1 second'), attempts = attempts + 1
    FROM selected WHERE outbox.id = selected.id
    RETURNING outbox.id, outbox.event_key, outbox.payload`, limit, lease.Seconds()).Scan(&items).Error
	if err != nil {
		return nil, err
	}
	if err = tx.Commit().Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (ctrl *Controller) MarkOutboxPublished(ctx context.Context, id int64) error {
	return ctrl.DB.WithContext(ctx).Model(&model.HazardOutbox{}).Where("id = ? AND published_at IS NULL", id).Updates(map[string]any{"published_at": gorm.Expr("now()"), "lease_until": nil}).Error
}
