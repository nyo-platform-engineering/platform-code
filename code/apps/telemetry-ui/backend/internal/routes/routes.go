package routes

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	admin "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/admin/controller"
	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/controller"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/controller"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/controller"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	"gorm.io/gorm"
)

// Route wiring contains no permission decisions; see internal/policy/policy.go.
func Register(router *gin.Engine, traceStore, logStore model.QueryStore, frontend http.Handler, version string, slots chan struct{}, oauth *auth.OAuth, controlDB *gorm.DB) {
	router.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok\n") })
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		if oauth != nil {
			if err := oauth.Ready(ctx); err != nil {
				c.JSON(503, gin.H{"error": "auth_unavailable"})
				return
			}
		}
		for _, store := range []model.QueryStore{traceStore, logStore} {
			if err := model.CheckConnection(ctx, store); err != nil {
				c.JSON(503, gin.H{"error": "storage_unavailable"})
				return
			}
		}
		if err := model.CheckTable(ctx, traceStore, "SELECT TraceId FROM otel.otel_traces LIMIT 0"); err != nil {
			c.JSON(503, gin.H{"error": "storage_unavailable"})
			return
		}
		if err := model.CheckTable(ctx, logStore, "SELECT TraceId FROM otel.otel_logs LIMIT 0"); err != nil {
			c.JSON(503, gin.H{"error": "storage_unavailable"})
			return
		}
		c.String(200, "ok\n")
	})
	api := router.Group("/api/v1")
	if oauth != nil {
		api.GET("/auth/providers", oauth.Providers)
		api.GET("/auth/session", oauth.Session)
		api.GET("/auth/:provider/login", oauth.Start)
		api.GET("/auth/:provider/callback", oauth.Callback)
		api.POST("/auth/logout", oauth.Logout)
	} else {
		api.GET("/auth/providers", func(c *gin.Context) { c.JSON(200, gin.H{"mode": "local", "providers": []string{}}) })
		api.GET("/auth/session", func(c *gin.Context) { c.JSON(200, gin.H{"mode": "local"}) })
		disabled := func(c *gin.Context) { c.JSON(404, gin.H{"error": "login_disabled"}) }
		api.GET("/auth/:provider/login", disabled)
		api.GET("/auth/:provider/callback", disabled)
		api.POST("/auth/logout", disabled)
	}
	api.GET("/meta", metadata.Metadata(version))
	api.GET("/data-sources", metadata.DataSources(controlDB))
	api.GET("/services", metadata.Services(traceStore, logStore, slots))
	api.GET("/traces/attributes", traces.Attributes(traceStore, slots))
	api.GET("/traces/red", traces.RED(traceStore, slots))
	api.GET("/traces", traces.List(traceStore, slots))
	api.GET("/traces/:traceId", traces.Detail(traceStore, slots))
	api.GET("/logs/attributes", logs.Attributes(logStore, slots))
	api.GET("/logs/volume", logs.Volume(logStore, slots))
	api.GET("/logs", logs.List(logStore, slots))
	api.GET("/admin/summary", admin.Summary(controlDB))

	router.GET("/", gin.WrapH(frontend))
	router.GET("/assets/*filepath", gin.WrapH(frontend))
	router.NoRoute(func(c *gin.Context) {
		frontend.ServeHTTP(c.Writer, c.Request)
	})
}
