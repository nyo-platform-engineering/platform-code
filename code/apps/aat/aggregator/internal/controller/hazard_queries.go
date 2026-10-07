package controller

import (
	"context"

	"aat/aggregator/internal/model"
)

func (ctrl *Controller) List(ctx context.Context, source, after string, limit int) ([]model.HazardEvent, error) {
	items := []model.HazardEvent{}
	query := ctrl.DB.WithContext(ctx).Where("id > ?", after).Order("id").Limit(limit)
	if source != "" {
		query = query.Where("source = ?", source)
	}
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Occurred = items[i].Occurred.UTC()
		items[i].Ingested = items[i].Ingested.UTC()
	}
	return items, nil
}
