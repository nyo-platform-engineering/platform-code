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
	go worker.Run(ctx, "test-consumer", "TEST", httpkit.Env("NATS_URL", "nats://nats:4222"), func(worker.Hazard) string {
		return "test_delivery"
	})
	router := httpkit.Router()
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	httpkit.Serve("Test consumer", "8087", router)
}
