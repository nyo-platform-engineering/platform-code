package controller

import (
	"aat/aggregator/internal/model"
	"context"
)

func (ctrl *Controller) List(ctx context.Context, source, after string, limit int) ([]model.HazardEvent, error) {
	return ctrl.HazardStore().List(ctx, source, after, limit)
}
