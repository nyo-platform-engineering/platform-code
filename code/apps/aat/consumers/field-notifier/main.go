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

// critical reports whether a hazard is a Sensor Pantai reading the sensor flagged
// critical. Those are delivered on the critical lane, not the regular one.
func critical(hazard worker.Hazard) bool {
	var attrs struct {
		Critical bool `json:"critical"`
	}
	return hazard.Source == "SENSOR_PANTAI" && json.Unmarshal(hazard.Attributes, &attrs) == nil && attrs.Critical
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	natsURL := httpkit.Env("NATS_URL", "nats://nats:4222")

	// Regular lane: every hazard, one at a time.
	go worker.Run(ctx, "field-notifier", "FIELD_NOTIFIER_PROCESSED", natsURL, func(ctx context.Context, hazard worker.Hazard) error {
		switch {
		case critical(hazard):
			slog.Info("field alert on critical lane", "correlation_id", httpkit.CorrelationID(ctx), "hazard_id", hazard.ID)
		case hazard.Severity == "SIAGA" || hazard.Severity == "AWAS":
			slog.Warn("field alert sent", "correlation_id", httpkit.CorrelationID(ctx), "hazard_id", hazard.ID, "source", hazard.Source, "severity", hazard.Severity, "area", hazard.Area)
		default:
			slog.Info("no field alert required", "correlation_id", httpkit.CorrelationID(ctx), "hazard_id", hazard.ID, "severity", hazard.Severity)
		}
		return nil
	})

	// Critical lane: Sensor Pantai critical readings only, on their own durable
	// consumer, so they never queue behind regular traffic. alert_latency_ms runs
	// from the reading's arrival at BNPB (ingested_at) to this delivery; the
	// target is p95 <= 5000 ms.
	go worker.RunSubject(ctx, "field-notifier-critical", "FIELD_NOTIFIER_CRITICAL", natsURL, eventbus.CriticalSubject, func(ctx context.Context, hazard worker.Hazard) error {
		var attrs struct {
			Verified      *bool  `json:"verified"`
			ObservationID string `json:"observation_id"`
			StationID     string `json:"station_id"`
			Reason        string `json:"reason"`
		}
		_ = json.Unmarshal(hazard.Attributes, &attrs)
		verified := attrs.Verified == nil || *attrs.Verified
		latency := time.Since(hazard.Ingested).Milliseconds()
		slog.Warn("critical field alert sent",
			"correlation_id", httpkit.CorrelationID(ctx),
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
		return nil
	})

	router := httpkit.Router()
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	httpkit.Serve("Field notifier", "8086", router)
}
