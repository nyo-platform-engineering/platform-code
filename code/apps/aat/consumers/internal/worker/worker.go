package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"aat/internal/eventbus"
	"aat/internal/httpkit"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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

type Handler func(context.Context, Hazard) error

// A new warning may update a hazard: deduplicate each delivery, not its hazard ID.
func deliveryKey(messageID string, hazard Hazard) string {
	if messageID == "" {
		messageID = hazard.ID + "-" + hazard.Ingested.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(messageID))
	return hex.EncodeToString(sum[:])
}

func Run(ctx context.Context, durable, bucket, natsURL string, handle Handler) {
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
				var dedupe jetstream.KeyValue
				dedupe, e = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
					Bucket:      bucket,
					Description: durable + " processed hazard IDs",
					Storage:     jetstream.FileStorage,
					TTL:         30 * 24 * time.Hour,
					Replicas:    1,
				})
			if e == nil {
				consume(ctx, durable, consumer, dedupe, handle)
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

func consume(ctx context.Context, durable string, consumer jetstream.Consumer, dedupe jetstream.KeyValue, handle Handler) {
	for ctx.Err() == nil {
		batch, e := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
		if e != nil {
			return
		}
		for msg := range batch.Messages() {
			correlationID := msg.Headers().Get("X-Correlation-ID")
			if correlationID == "" {
				correlationID = httpkit.ID()
			}
			messageCtx := httpkit.WithCorrelationID(ctx, correlationID)
			start := time.Now()
			handleMs := int64(-1)
			var hazard Hazard
			if e = json.Unmarshal(msg.Data(), &hazard); e != nil || hazard.ID == "" {
				slog.Error("invalid hazard event; terminating delivery",
					"durable", durable,
					"correlation_id", correlationID,
					"status", "invalid",
					"latency_ms", time.Since(start).Milliseconds(),
					"handle_ms", handleMs,
					"error", e)
				_ = msg.Term()
				continue
			}
			key := deliveryKey(msg.Headers().Get("Nats-Msg-Id"), hazard)
			_, e = dedupe.Get(messageCtx, key)
			if e == nil {
				if ackErr := msg.Ack(); ackErr != nil {
					slog.Warn("duplicate ack failed",
						"durable", durable,
						"correlation_id", correlationID,
						"hazard_id", hazard.ID,
						"status", "duplicate",
						"latency_ms", time.Since(start).Milliseconds(),
						"handle_ms", handleMs,
						"error", ackErr)
				} else {
					slog.Info("consume_completed",
						"durable", durable,
						"correlation_id", correlationID,
						"hazard_id", hazard.ID,
						"status", "duplicate",
						"latency_ms", time.Since(start).Milliseconds(),
						"handle_ms", handleMs)
				}
				continue
			}
			if !errors.Is(e, jetstream.ErrKeyNotFound) {
				slog.Warn("dedupe check failed; message will retry",
					"durable", durable,
					"correlation_id", correlationID,
					"hazard_id", hazard.ID,
					"status", "error",
					"latency_ms", time.Since(start).Milliseconds(),
					"handle_ms", handleMs,
					"error", e)
				_ = msg.NakWithDelay(2 * time.Second)
				continue
			}
			handleStart := time.Now()
			e = handle(messageCtx, hazard)
			handleMs = time.Since(handleStart).Milliseconds()
			if e != nil {
				slog.Error("hazard handling failed; message will retry",
					"durable", durable,
					"correlation_id", correlationID,
					"hazard_id", hazard.ID,
					"status", "error",
					"latency_ms", time.Since(start).Milliseconds(),
					"handle_ms", handleMs,
					"error", e)
				_ = msg.NakWithDelay(2 * time.Second)
				continue
			}
			if _, e = dedupe.Create(messageCtx, key, []byte(time.Now().UTC().Format(time.RFC3339Nano))); e != nil {
				if _, getErr := dedupe.Get(messageCtx, key); getErr != nil {
					slog.Warn("dedupe write failed; message will retry",
						"durable", durable,
						"correlation_id", correlationID,
						"hazard_id", hazard.ID,
						"status", "error",
						"latency_ms", time.Since(start).Milliseconds(),
						"handle_ms", handleMs,
						"error", e)
					_ = msg.NakWithDelay(2 * time.Second)
					continue
				}
			}
			if e = msg.Ack(); e != nil {
				slog.Warn("hazard ack failed",
					"durable", durable,
					"correlation_id", correlationID,
					"hazard_id", hazard.ID,
					"status", "error",
					"latency_ms", time.Since(start).Milliseconds(),
					"handle_ms", handleMs,
					"error", e)
				continue
			}
			slog.Info("consume_completed",
				"durable", durable,
				"correlation_id", correlationID,
				"hazard_id", hazard.ID,
				"status", "ok",
				"latency_ms", time.Since(start).Milliseconds(),
				"handle_ms", handleMs)
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
