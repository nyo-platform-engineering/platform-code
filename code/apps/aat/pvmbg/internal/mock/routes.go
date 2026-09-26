package mock

import (
	"net/http"
	"time"

	"aat/internal/httpkit"
	"aat/pvmbg/internal/mock/controller"
	"aat/pvmbg/internal/mock/model"
)

func NewHandler(token string, interval, minDelay, maxDelay time.Duration) http.Handler {
	feed := model.NewFeed(time.Now().UTC(), httpkit.ID(), interval)
	ctrl := &controller.Controller{
		Feed:     feed,
		MinDelay: minDelay,
		MaxDelay: maxDelay,
	}
	mux := httpkit.Router()
	private := mux.Group("", httpkit.Auth("Authorization", "Bearer "+token))

	mux.GET("/health", ctrl.Health)
	private.GET("/volcanic-reports", ctrl.VolcanicReports)
	private.POST("/admin/outage", ctrl.SetOutage)
	private.POST("/admin/schema-version", ctrl.SetSchemaVersion)

	return mux
}
