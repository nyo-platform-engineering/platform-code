package internal

import (
	"fmt"
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
	if ctrl.PageCache == nil {
		ctrl.PageCache = &controller.HazardPageCache{}
	}
	mux := httpkit.Router()
	private := mux.Group("", httpkit.Auth("Authorization", "Bearer "+token))

	mux.GET("/health", ctrl.Health)
	private.Use(func(c *gin.Context) {
		if ctrl.SatelliteVerificationSchema != "" && c.Request.Method != http.MethodGet && c.Request.URL.Path != "/internal/satellite/verification/crash" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "controlled P4 verification in progress"})
			return
		}
		c.Next()
	})
	reads := limitClassedReads(ctrl.ReadConcurrency, ctrl.ReadPriorityConcurrency)
	rate := limitReadRate(ctrl.ReadRPSLimit, ctrl.ReadRPSBurst)
	private.GET("/internal/hazards", rate, limitReads(128), ctrl.CachedHazards, reads, ctrl.Hazards)
	private.GET("/internal/analytics", rate, reads, ctrl.Analytics)
	private.GET("/internal/source-status", ctrl.SourceStatus)
	private.GET("/internal/satellite/stats", ctrl.SatelliteStatsHandler)
	private.POST("/internal/satellite/verification/crash", func(c *gin.Context) {
		if ctrl.SatelliteVerificationSchema == "" || ctrl.VerificationCrash == nil {
			c.Status(http.StatusNotFound)
			return
		}
		ctrl.VerificationCrash() // Abrupt exit; never run in live mode.
		c.Status(http.StatusAccepted)
	})
	private.GET("/internal/satellite/capabilities", func(c *gin.Context) {
		c.JSON(200, gin.H{"controlled_verification_version": 1, "verification_schema": ctrl.SatelliteVerificationSchema, "database_role": ctrl.DatabaseRole})
	})
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

// ReadClassHeader is set by the Client API, after it has identified the caller, on
// reads for the Field Team and BNPB Internal. It is internal: the Client API never
// forwards a caller's own headers, and this API needs the Aggregator token.
const ReadClassHeader = "X-Read-Class"

// limitClassedReads bounds database reads per pod. Every read may take a shared slot;
// reads marked priority may also take one of the priority slots, which Media and
// unmarked callers never get. A priority read tries its reserved slot first so the
// shared slots stay available to everyone else. Nothing queues: a read with no free
// slot is rejected at once with 429 and Retry-After.
func limitClassedReads(shared, priority int) gin.HandlerFunc {
	if shared <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	sharedSlots := make(chan struct{}, shared)
	var prioritySlots chan struct{}
	if priority > 0 {
		prioritySlots = make(chan struct{}, priority)
	}
	return func(c *gin.Context) {
		if prioritySlots != nil && c.GetHeader(ReadClassHeader) == "priority" {
			select {
			case prioritySlots <- struct{}{}:
				defer func() { <-prioritySlots }()
				c.Next()
				return
			default:
			}
		}
		select {
		case sharedSlots <- struct{}{}:
			defer func() { <-sharedSlots }()
			c.Next()
		default:
			c.Header("Retry-After", "1")
			c.Header("X-Read-Limit", fmt.Sprint(shared))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "read capacity reached; retry later"})
		}
	}
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
			c.Header("X-Read-Limit", fmt.Sprint(capacity))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "read capacity reached; retry later"})
		}
	}
}
