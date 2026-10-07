package mapper

import (
	"aat/aggregator/internal/model"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

var Levels = map[string]string{"Normal": "NORMAL", "Waspada": "WASPADA", "Siaga": "SIAGA", "Awas": "AWAS"}

var Rank = map[string]int{"NORMAL": 0, "WASPADA": 1, "SIAGA": 2, "AWAS": 3}

func Read[T any](r model.Record, key string) (T, error) {
	var v T
	b, ok := r[key]
	if !ok || string(b) == "null" {
		return v, fmt.Errorf("missing/null %s", key)
	}

	if e := json.Unmarshal(b, &v); e != nil {
		return v, fmt.Errorf("invalid %s: %w", key, e)
	}

	return v, nil
}

func String(r model.Record, key string) (string, error) {
	v, e := Read[string](r, key)
	if e == nil && v == "" {
		e = fmt.Errorf("empty %s", key)
	}

	return v, e
}

func Number(r model.Record, key string, min, max float64) (float64, error) {
	v, e := Read[float64](r, key)
	if e == nil && (math.IsNaN(v) || math.IsInf(v, 0) || v < min || v > max) {
		e = fmt.Errorf("%s out of range", key)
	}

	return v, e
}

func Attributes(r model.Record, keys ...string) model.Record {
	a := model.Record{}
	for k, v := range r {
		a[k] = v
	}

	for _, k := range keys {
		delete(a, k)
	}

	return a
}

func Base(source, ref, kind string, at time.Time) model.HazardEvent {
	sum := sha256.Sum256([]byte(source + ":" + ref))
	return model.HazardEvent{
		ID:       "haz-" + hex.EncodeToString(sum[:16]),
		Source:   source,
		Ref:      ref,
		Type:     kind,
		Occurred: at.UTC(),
		Ingested: time.Now().UTC(),
	}
}
