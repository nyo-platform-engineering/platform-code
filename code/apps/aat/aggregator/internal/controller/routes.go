package controller

import (
	"net/http"

	"aat/internal/httpkit"
)

func NewHandler(ctrl *Controller, token string) http.Handler {
	mux := httpkit.Router()
	private := mux.Group("", httpkit.Auth("Authorization", "Bearer "+token))

	mux.GET("/health", ctrl.Health)
	private.GET("/internal/hazards", ctrl.Hazards)
	private.GET("/internal/source-status", ctrl.SourceStatus)
	private.POST("/internal/ingest/bmkg", ctrl.Ingest("BMKG"))
	private.POST("/internal/ingest/pvmbg", ctrl.Ingest("PVMBG"))

	return mux
}
