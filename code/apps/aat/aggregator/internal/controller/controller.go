package controller

import (
	"context"
	"time"

	"aat/aggregator/internal/dbtrace"
	"gorm.io/gorm"
)

type Controller struct {
	DB *gorm.DB
	// SatelliteLateness overrides DefaultSatelliteLateness when positive.
	SatelliteLateness time.Duration
}

func (ctrl *Controller) Ping(ctx context.Context) error {
	db, err := ctrl.DB.DB()
	if err != nil {
		return err
	}
	return dbtrace.Observe(ctx, "ping", db.PingContext)
}
