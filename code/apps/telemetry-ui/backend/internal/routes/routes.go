package routes

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/controller"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/controller"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/controller"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

// Route wiring contains no permission decisions; see internal/policy/policy.go.
func Register(router *gin.Engine, traceStore, logStore model.QueryStore, frontend http.Handler, version string, slots chan struct{}) {
	router.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok\n") })
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		for _, store := range []model.QueryStore{traceStore, logStore} {
			if err := model.CheckConnection(ctx, store); err != nil {
				c.JSON(503, gin.H{"error": "storage_unavailable"})
				return
			}
		}
		if _, err := traceStore.Query(ctx, "SELECT TraceId FROM otel.otel_traces LIMIT 0"); err != nil {
			c.JSON(503, gin.H{"error": "storage_unavailable"})
			return
		}
		if _, err := logStore.Query(ctx, "SELECT TraceId FROM otel.otel_logs LIMIT 0"); err != nil {
			c.JSON(503, gin.H{"error": "storage_unavailable"})
			return
		}
		c.String(200, "ok\n")
	})
	api := router.Group("/api/v1")
	api.GET("/meta", metadata.Metadata(version))
	api.GET("/services", metadata.Services(traceStore, logStore, slots))
	api.GET("/traces/attributes", traces.Attributes(traceStore, slots))
	api.GET("/traces/red", traces.RED(traceStore, slots))
	api.GET("/traces", traces.List(traceStore, slots))
	api.GET("/traces/:traceId", traces.Detail(traceStore, slots))
	api.GET("/logs/attributes", logs.Attributes(logStore, slots))
	api.GET("/logs/volume", logs.Volume(logStore, slots))
	api.GET("/logs", logs.List(logStore, slots))

	router.GET("/", gin.WrapH(frontend))
	router.GET("/assets/*filepath", gin.WrapH(frontend))
	router.NoRoute(func(c *gin.Context) {
		frontend.ServeHTTP(c.Writer, c.Request)
	})
}
