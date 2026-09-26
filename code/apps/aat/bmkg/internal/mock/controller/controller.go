package controller

import (
	"time"

	"aat/bmkg/internal/mock/model"
)

type Controller struct {
	Feed  *model.Feed
	Delay time.Duration
}
