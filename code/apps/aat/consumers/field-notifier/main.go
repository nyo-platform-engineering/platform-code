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
	go worker.Run(ctx, "field-notifier", "FIELD_NOTIFIER_PROCESSED", httpkit.Env("NATS_URL", "nats://nats:4222"), func(_ context.Context, hazard worker.Hazard) error {
		if hazard.Severity == "SIAGA" || hazard.Severity == "AWAS" {
			slog.Warn("field alert sent", "hazard_id", hazard.ID, "source", hazard.Source, "severity", hazard.Severity, "area", hazard.Area)
		} else {
			slog.Info("no field alert required", "hazard_id", hazard.ID, "severity", hazard.Severity)
		}
		return nil
	})
	
	router := httpkit.Router()
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	httpkit.Serve("Field notifier", "8086", router)
}
