package internal

import (
	"net/http"

	"aat/aggregator/internal/controller"
	"aat/internal/httpkit"
)

func NewHandler(ctrl *controller.Controller, token string) http.Handler {
	return NewHandlerWithAudit(ctrl, token, "")
}

// NewHandlerWithAudit registers a separate audit group. Additional audit handlers
// (such as unprocessed) must use the same authentication and JSON error contract.
func NewHandlerWithAudit(ctrl *controller.Controller, token, auditToken string) http.Handler {
	mux := httpkit.Router()
	private := mux.Group("", httpkit.Auth("Authorization", "Bearer "+token))

	mux.GET("/health", ctrl.Health)
	private.GET("/internal/hazards", ctrl.Hazards)
	private.GET("/internal/source-status", ctrl.SourceStatus)
	private.GET("/internal/satellite/stats", ctrl.SatelliteStatsHandler)
	private.POST("/internal/ingest/bmkg", ctrl.Ingest("BMKG"))
	private.POST("/internal/ingest/pvmbg", ctrl.Ingest("PVMBG"))

	audit := mux.Group("/internal/audit", controller.AuditAuth(auditToken))
	audit.GET("/events", ctrl.AuditEvents)
	audit.GET("/checksum", ctrl.AuditChecksum)
	audit.GET("/location", ctrl.AuditLocation)

	return mux
}
