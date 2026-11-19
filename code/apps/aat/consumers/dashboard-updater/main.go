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
	go worker.Run(ctx, "dashboard-updater", "DASHBOARD_PROCESSED", httpkit.Env("NATS_URL", "nats://nats:4222"), func(worker.Hazard) string {
		return "dashboard_update"
	})
	router := httpkit.Router()
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	httpkit.Serve("Dashboard updater", "8085", router)
}
