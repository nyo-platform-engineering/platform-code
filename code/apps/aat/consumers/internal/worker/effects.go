package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/propagation"
)

// Projection chooses a record kind without sending notifications or changing state.
type Projection func(Hazard) string

type Effect struct {
	DeliveryID    string `json:"delivery_id"`
	Kind          string `json:"kind"`
	Hazard        Hazard `json:"hazard"`
	CorrelationID string `json:"correlation_id,omitempty"`
	TraceParent   string `json:"trace_parent,omitempty"`
	TraceState    string `json:"trace_state,omitempty"`
}

type effectStore interface {
	Get(context.Context, string) (jetstream.KeyValueEntry, error)
	Create(context.Context, string, []byte, ...jetstream.KVCreateOpt) (uint64, error)
}

var ErrDeliveryConflict = errors.New("delivery ID already contains a different hazard")

func recordEffect(ctx context.Context, store effectStore, key string, hazard Hazard, project Projection) (Effect, bool, error) {
	existing := func(entry jetstream.KeyValueEntry) (Effect, bool, error) {
		var effect Effect
		if err := json.Unmarshal(entry.Value(), &effect); err != nil {
			return Effect{}, false, fmt.Errorf("decode consumer effect: %w", err)
		}
		stored, err := json.Marshal(effect.Hazard)
		if err != nil {
			return Effect{}, false, err
		}
		incoming, err := json.Marshal(hazard)
		if err != nil {
			return Effect{}, false, err
		}
		if effect.DeliveryID != key || effect.Kind == "" || !bytes.Equal(stored, incoming) {
			return Effect{}, false, ErrDeliveryConflict
		}
		return effect, false, nil
	}
	entry, err := store.Get(ctx, key)
	if err == nil {
		return existing(entry)
	}
	if !errors.Is(err, jetstream.ErrKeyNotFound) {
		return Effect{}, false, err
	}
	carrier := propagation.MapCarrier{}
	telemetry.Propagation.Inject(ctx, carrier)
	effect := Effect{DeliveryID: key, Kind: project(hazard), Hazard: hazard,
		CorrelationID: httpkit.CorrelationID(ctx), TraceParent: carrier.Get("traceparent"), TraceState: carrier.Get("tracestate")}
	if effect.Kind == "" {
		return Effect{}, false, errors.New("consumer effect kind is empty")
	}
	payload, err := json.Marshal(effect)
	if err != nil {
		return Effect{}, false, err
	}
	if _, err := store.Create(ctx, key, payload); err != nil {
		// A concurrent delivery or a lost write response may already have committed it.
		if entry, getErr := store.Get(ctx, key); getErr == nil {
			return existing(entry)
		}
		return Effect{}, false, err
	}
	return effect, true, nil
}
