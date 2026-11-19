package internal

import (
	"net/http"

	"aat/aggregator/internal/controller"
	"aat/internal/httpkit"
	"github.com/gin-gonic/gin"
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
	reads := limitReads(ctrl.ReadConcurrency)
	private.GET("/internal/hazards", reads, ctrl.Hazards)
	private.GET("/internal/analytics", reads, ctrl.Analytics)
	private.GET("/internal/source-status", ctrl.SourceStatus)
	private.GET("/internal/satellite/stats", ctrl.SatelliteStatsHandler)
	private.GET("/internal/sensor/status", ctrl.SensorStatusHandler)
	private.POST("/internal/ingest/bmkg", ctrl.Ingest("BMKG"))
	private.POST("/internal/ingest/pvmbg", ctrl.Ingest("PVMBG"))
	private.POST("/internal/field-reports", ctrl.FieldReportAccept)
	private.GET("/internal/ops/field-reports/:report_id", ctrl.FieldReportDetails)
	private.GET("/internal/ops/unprocessed", ctrl.AuditUnprocessed)
	private.GET("/internal/ops/unprocessed/:message_id", ctrl.InboxDetail)
	private.POST("/internal/ops/unprocessed/:message_id/replay", ctrl.InboxReplay)

	audit := mux.Group("/internal/audit", controller.AuditAuth(auditToken))
	audit.GET("/events", ctrl.AuditEvents)
	audit.GET("/checksum", ctrl.AuditChecksum)
	audit.GET("/location", ctrl.AuditLocation)
	audit.GET("/unprocessed", ctrl.AuditUnprocessed)

	return mux
}

func limitReads(capacity int) gin.HandlerFunc {
	if capacity <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	slots := make(chan struct{}, capacity)
	return func(c *gin.Context) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			c.Next()
		default:
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "read capacity reached; retry later"})
		}
	}
}
