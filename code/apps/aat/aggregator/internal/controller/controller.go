package controller

import (
	"context"

	"aat/aggregator/internal/dbtrace"
	"gorm.io/gorm"
)

type Controller struct {
	DB *gorm.DB
}

func (ctrl *Controller) Ping(ctx context.Context) error {
	db, err := ctrl.DB.DB()
	if err != nil {
		return err
	}
	return dbtrace.Observe(ctx, "ping", db.PingContext)
}
