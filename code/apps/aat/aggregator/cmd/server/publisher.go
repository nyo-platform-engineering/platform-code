package main

import (
	"context"
	"log/slog"
	"time"

	"aat/aggregator/internal/aggregate"
	"aat/internal/eventbus"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type outboxStore interface {
	ClaimOutbox(context.Context, int, time.Duration) ([]aggregate.OutboxMessage, error)
	MarkOutboxPublished(context.Context, int64) error
}

func runOutboxRelay(ctx context.Context, store outboxStore, url string) {
	for ctx.Err() == nil {
		nc, e := nats.Connect(url, nats.Timeout(3*time.Second), nats.MaxReconnects(0))
		if e != nil {
			slog.Warn("NATS unavailable; outbox will retry", "error", e)
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		js, e := jetstream.New(nc)
		if e == nil {
			e = eventbus.EnsureStream(ctx, js)
		}
		if e != nil {
			slog.Error("JetStream setup failed", "error", e)
			nc.Close()
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}

		reconnect := false
		for ctx.Err() == nil && !reconnect {
			items, claimErr := store.ClaimOutbox(ctx, 32, 30*time.Second)
			if claimErr != nil {
				slog.Error("outbox claim failed", "error", claimErr)
				if !wait(ctx, time.Second) {
					break
				}
				continue
			}
			if len(items) == 0 {
				wait(ctx, 500*time.Millisecond)
				continue
			}
			for _, item := range items {
				publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, e = js.Publish(publishCtx, eventbus.Subject, item.Payload,
					jetstream.WithMsgID(item.EventKey), jetstream.WithExpectStream(eventbus.StreamName))
				cancel()
				if e != nil {
					slog.Warn("JetStream publish failed; outbox will retry", "outbox_id", item.ID, "error", e)
					reconnect = true
					break
				}
				if e = store.MarkOutboxPublished(ctx, item.ID); e != nil {
					slog.Error("outbox acknowledgement update failed", "outbox_id", item.ID, "error", e)
					reconnect = true
					break
				}
				slog.Info("hazard published", "outbox_id", item.ID, "event_key", item.EventKey, "subject", eventbus.Subject)
			}
		}
		nc.Close()
		if !wait(ctx, time.Second) {
			return
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
