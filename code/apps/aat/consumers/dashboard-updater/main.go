package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"aat/consumers/internal/worker"
	"aat/internal/httpkit"
	"github.com/gin-gonic/gin"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go worker.Run(ctx, "dashboard-updater", "DASHBOARD_PROCESSED", httpkit.Env("NATS_URL", "nats://nats:4222"), func(ctx context.Context, hazard worker.Hazard) error {
		slog.Info("dashboard hazard update", "correlation_id", httpkit.CorrelationID(ctx), "hazard_id", hazard.ID, "source", hazard.Source, "type", hazard.Type, "severity", hazard.Severity, "area", hazard.Area, "occurred_at", hazard.Occurred)
		return nil
	})
	router := httpkit.Router()
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	httpkit.Serve("Dashboard updater", "8085", router)
}
