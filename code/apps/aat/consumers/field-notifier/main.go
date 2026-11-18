package main

import (
	"context"
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
	go worker.Run(ctx, "field-notifier", "FIELD_NOTIFIER_PROCESSED", httpkit.Env("NATS_URL", "nats://nats:4222"), func(hazard worker.Hazard) string {
		if hazard.Severity == "SIAGA" || hazard.Severity == "AWAS" {
			return "field_alert"
		}
		return "no_field_alert"
	})

	router := httpkit.Router()
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	httpkit.Serve("Field notifier", "8086", router)
}
