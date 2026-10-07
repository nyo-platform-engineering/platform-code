package controller

import (
	"context"
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
	return db.PingContext(ctx)
}
