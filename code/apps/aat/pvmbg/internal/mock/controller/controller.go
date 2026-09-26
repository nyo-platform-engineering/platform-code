package controller

import (
	"time"

	"aat/pvmbg/internal/mock/model"
)

type Controller struct {
	Feed     *model.Feed
	MinDelay time.Duration
	MaxDelay time.Duration
}
