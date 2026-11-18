package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aat/consumers/internal/worker"
	"aat/internal/eventbus"
	"aat/internal/httpkit"

	"github.com/gin-gonic/gin"
)

func critical(hazard worker.Hazard) bool {
	var attrs struct {
		Critical bool `json:"critical"`
	}
	return hazard.Source == "SENSOR_PANTAI" && json.Unmarshal(hazard.Attributes, &attrs) == nil && attrs.Critical
}

func regularKind(hazard worker.Hazard) string {
	if critical(hazard) {
		return "critical_alert_delegated"
	}
	if hazard.Severity == "SIAGA" || hazard.Severity == "AWAS" {
		return "field_alert"
	}
	return "no_field_alert"
}

func observeCritical(ctx context.Context, effect worker.Effect) {
	hazard := effect.Hazard
	var attrs struct {
		Verified  *bool  `json:"verified"`
		StationID string `json:"station_id"`
		Reason    string `json:"reason"`
	}
	_ = json.Unmarshal(hazard.Attributes, &attrs)
	verified := attrs.Verified == nil || *attrs.Verified
	latency := time.Since(hazard.Ingested).Milliseconds()
	slog.Warn("critical field alert recorded",
		"correlation_id", httpkit.CorrelationID(ctx),
		"delivery_id", effect.DeliveryID,
		"hazard_id", hazard.ID,
		"source", hazard.Source,
		"severity", hazard.Severity,
		"area", hazard.Area,
		"station_id", attrs.StationID,
		"verified", verified,
		"reason", attrs.Reason,
		"alert_latency_ms", latency,
		"within_slo", latency <= 5000,
	)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	natsURL := httpkit.Env("NATS_URL", "nats://nats:4222")
	go worker.Run(ctx, "field-notifier", "FIELD_NOTIFIER_PROCESSED", natsURL, regularKind)
	go worker.RunSubject(ctx, "field-notifier-critical", "FIELD_NOTIFIER_CRITICAL", natsURL, eventbus.CriticalSubject,
		func(worker.Hazard) string { return "critical_field_alert" }, observeCritical)

	router := httpkit.Router()
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	httpkit.Serve("Field notifier", "8086", router)
}
