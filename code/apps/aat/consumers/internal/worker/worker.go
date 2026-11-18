package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"aat/internal/eventbus"
	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Hazard struct {
	ID         string          `json:"hazard_id"`
	Source     string          `json:"source"`
	Type       string          `json:"hazard_type"`
	Severity   string          `json:"severity"`
	Area       string          `json:"area_name"`
	Occurred   time.Time       `json:"occurred_at"`
	Ingested   time.Time       `json:"ingested_at"`
	Attributes json.RawMessage `json:"attributes"`
}

// A new warning may update a hazard: deduplicate each delivery, not its hazard ID.
func deliveryKey(messageID string, hazard Hazard) string {
	if messageID == "" {
		messageID = hazard.ID + "-" + hazard.Ingested.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(messageID))
	return hex.EncodeToString(sum[:])
}

func Run(ctx context.Context, durable, bucket, natsURL string, project Projection) {
	for ctx.Err() == nil {
		nc, e := nats.Connect(natsURL, nats.Timeout(3*time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(time.Second))
		if e != nil {
			slog.Warn("NATS unavailable; consumer will retry", "durable", durable, "error", e)
			pause(ctx)
			continue
		}
		js, e := jetstream.New(nc)
		if e == nil {
			var consumer jetstream.Consumer
			consumer, e = js.CreateOrUpdateConsumer(ctx, eventbus.StreamName, jetstream.ConsumerConfig{
				Durable:       durable,
				Description:   durable + " independent hazard subscriber",
				FilterSubject: eventbus.Subject,
				DeliverPolicy: jetstream.DeliverAllPolicy,
				AckPolicy:     jetstream.AckExplicitPolicy,
				AckWait:       30 * time.Second,
				MaxAckPending: 1,
				ReplayPolicy:  jetstream.ReplayInstantPolicy,
			})
			if e == nil {
				var effects jetstream.KeyValue
				effects, e = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
					Bucket:      bucket + "_EFFECTS",
					Description: durable + " durable consumer inbox",
					Storage:     jetstream.FileStorage,
					Replicas:    1,
				})
				if e == nil {
					consume(ctx, durable, consumer, effects, project)
				}
			}
		}
		if e != nil && ctx.Err() == nil {
			slog.Warn("consumer setup or fetch failed; reconnecting", "durable", durable, "error", e)
		}
		nc.Close()
		pause(ctx)
	}
}

func consume(ctx context.Context, durable string, consumer jetstream.Consumer, effects effectStore, project Projection) {
	for ctx.Err() == nil {
		batch, e := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
		if e != nil {
			return
		}
		for msg := range batch.Messages() {
			func() {
				correlationID := msg.Headers().Get("X-Correlation-ID")
				if correlationID == "" {
					correlationID = httpkit.ID()
				}
				messageCtx := httpkit.WithCorrelationID(ctx, correlationID)
				messageCtx = telemetry.Propagation.Extract(messageCtx, eventbus.HeaderCarrier(msg.Headers()))
				messageCtx, span := telemetry.Start(messageCtx, "consume "+eventbus.Subject, trace.SpanKindConsumer)
				span.SetAttributes(attribute.String("messaging.system", "nats"), attribute.String("messaging.destination.name", eventbus.Subject), attribute.String("messaging.operation.type", "process"), attribute.String("messaging.consumer.group.name", durable))
				defer func() { telemetry.Fail(span, e); span.End() }()
				start := time.Now()
				handleMs := int64(-1)
				var hazard Hazard
				if e = json.Unmarshal(msg.Data(), &hazard); e != nil || hazard.ID == "" {
					slog.Error("invalid hazard event; terminating delivery",
						"durable", durable,
						"correlation_id", correlationID,
						"trace_id", telemetry.TraceID(messageCtx),
						"status", "invalid",
						"latency_ms", time.Since(start).Milliseconds(),
						"handle_ms", handleMs,
						"error", e)
					_ = msg.Term()
					return
				}
				span.SetAttributes(attribute.String("aat.hazard_id", hazard.ID), attribute.String("aat.source", hazard.Source))
				key := deliveryKey(msg.Headers().Get("Nats-Msg-Id"), hazard)
				handleStart := time.Now()
				effectCtx, cancel := context.WithTimeout(messageCtx, 5*time.Second)
				effect, created, recordErr := recordEffect(effectCtx, effects, key, hazard, project)
				cancel()
				e = recordErr
				handleMs = time.Since(handleStart).Milliseconds()
				if e != nil {
					slog.Error("consumer effect not confirmed; message will retry",
						"durable", durable, "delivery_id", key, "hazard_id", hazard.ID,
						"correlation_id", correlationID, "trace_id", telemetry.TraceID(messageCtx),
						"status", "error", "error", e)
					_ = msg.NakWithDelay(2 * time.Second)
					return
				}
				status := "duplicate"
				if created {
					status = "recorded"
				}
				ackCtx, cancelAck := context.WithTimeout(messageCtx, 5*time.Second)
				e = msg.DoubleAck(ackCtx)
				cancelAck()
				if e != nil {
					slog.Warn("hazard ack failed",
						"durable", durable,
						"correlation_id", correlationID,
						"trace_id", telemetry.TraceID(messageCtx),
						"hazard_id", hazard.ID,
						"status", "error",
						"latency_ms", time.Since(start).Milliseconds(),
						"handle_ms", handleMs,
						"error", e)
					return
				}
				slog.Info("consume_completed",
					"durable", durable,
					"correlation_id", correlationID,
					"trace_id", telemetry.TraceID(messageCtx),
					"hazard_id", hazard.ID,
					"status", status,
					"delivery_id", key,
					"effect_kind", effect.Kind,
					"latency_ms", time.Since(start).Milliseconds(),
					"handle_ms", handleMs)
			}()
		}
		if e = batch.Error(); e != nil {
			slog.Warn("consumer fetch batch ended with error", "error", e)
		}
	}
}

func pause(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
