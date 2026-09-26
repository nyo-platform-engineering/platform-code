package aggregate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type Record map[string]json.RawMessage

type Hazard struct {
	ID         string    `json:"hazard_id"`
	Source     string    `json:"source"`
	Ref        string    `json:"source_ref_id"`
	Type       string    `json:"hazard_type"`
	Severity   string    `json:"severity"`
	Area       string    `json:"area_name"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	Occurred   time.Time `json:"occurred_at"`
	Ingested   time.Time `json:"ingested_at"`
	Attributes Record    `json:"attributes"`
}

var levels = map[string]string{"Normal": "NORMAL", "Waspada": "WASPADA", "Siaga": "SIAGA", "Awas": "AWAS"}

var rank = map[string]int{"NORMAL": 0, "WASPADA": 1, "SIAGA": 2, "AWAS": 3}

// Coordinates are static demo references owned by BNPB, not navigation data.
var volcanoes = map[string]struct {
	Name     string
	Lat, Lon float64
}{
	"MERAPI":        {"Gunung Merapi", -7.54, 110.446},
	"SEMERU":        {"Gunung Semeru", -8.108, 112.922},
	"ANAK_KRAKATAU": {"Gunung Anak Krakatau", -6.102, 105.423},
}

func read[T any](r Record, key string) (T, error) {
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

func str(r Record, key string) (string, error) {
	v, e := read[string](r, key)
	if e == nil && v == "" {
		e = fmt.Errorf("empty %s", key)
	}

	return v, e
}

func num(r Record, key string, min, max float64) (float64, error) {
	v, e := read[float64](r, key)
	if e == nil && (math.IsNaN(v) || math.IsInf(v, 0) || v < min || v > max) {
		e = fmt.Errorf("%s out of range", key)
	}

	return v, e
}

func attributes(r Record, keys ...string) Record {
	a := Record{}
	for k, v := range r {
		a[k] = v
	}

	for _, k := range keys {
		delete(a, k)
	}

	return a
}

func base(source, ref, kind string, at time.Time) Hazard {
	sum := sha256.Sum256([]byte(source + ":" + ref))
	return Hazard{
		ID:       "haz-" + hex.EncodeToString(sum[:16]),
		Source:   source,
		Ref:      ref,
		Type:     kind,
		Occurred: at.UTC(),
		Ingested: time.Now().UTC(),
	}
}

func Warning(r Record) (id, ref string, err error) {
	id, err = str(r, "warning_id")
	if err != nil {
		return
	}

	ref, err = str(r, "related_event_id")
	if err != nil {
		return
	}

	level, e := str(r, "threat_level")
	if e != nil || rank[levels[level]] < 1 {
		err = fmt.Errorf("invalid threat_level")
		return
	}

	if _, err = read[[]string](r, "affected_zones"); err != nil {
		return
	}

	_, err = read[time.Time](r, "estimated_arrival")
	return
}

func Seismic(r Record, warnings []Record) (Hazard, error) {
	var h Hazard
	ref, e := str(r, "event_id")
	if e != nil {
		return h, e
	}

	at, e := read[time.Time](r, "occurred_at")
	if e != nil {
		return h, e
	}

	h = base("BMKG", ref, "SEISMIC", at)
	if h.Area, e = str(r, "region_name"); e != nil {
		return h, e
	}

	if h.Latitude, e = num(r, "epicenter_lat", -90, 90); e != nil {
		return h, e
	}

	if h.Longitude, e = num(r, "epicenter_lon", -180, 180); e != nil {
		return h, e
	}

	mag, e := num(r, "magnitude", 0, math.MaxFloat64)
	if e != nil {
		return h, e
	}

	if _, e = num(r, "depth_km", 0, math.MaxFloat64); e != nil {
		return h, e
	}

	potential, e := read[bool](r, "potential_tsunami")
	if e != nil {
		return h, e
	}

	h.Severity = "NORMAL"
	if mag >= 6.5 {
		h.Severity = "SIAGA"
	} else if mag >= 5 {
		h.Severity = "WASPADA"
	}

	h.Attributes = attributes(r, "event_id", "region_name", "epicenter_lat", "epicenter_lon", "occurred_at")
	if len(warnings) > 0 {
		if !potential {
			return h, fmt.Errorf("warning requires potential_tsunami=true")
		}

		h.Severity = "NORMAL"
		for _, w := range warnings {
			_, related, e := Warning(w)
			if e != nil {
				return h, e
			}

			if related != ref {
				return h, fmt.Errorf("unrelated warning")
			}

			level, _ := str(w, "threat_level")
			if rank[levels[level]] > rank[h.Severity] {
				h.Severity = levels[level]
			}
		}

		if _, collision := h.Attributes["tsunami_warnings"]; collision {
			return h, fmt.Errorf("reserved attribute tsunami_warnings")
		}

		h.Attributes["tsunami_warnings"], _ = json.Marshal(warnings)
	}

	return h, nil
}

func Volcanic(r Record) (Hazard, error) {
	var h Hazard
	ref, e := str(r, "report_id")
	if e != nil {
		return h, e
	}

	at, e := read[time.Time](r, "reported_at")
	if e != nil {
		return h, e
	}

	h = base("PVMBG", ref, "VOLCANIC", at)
	id, e := str(r, "volcano_id")
	if e != nil {
		return h, e
	}

	v, ok := volcanoes[id]
	if !ok {
		return h, fmt.Errorf("unknown volcano_id %q", id)
	}

	h.Area = v.Name
	h.Latitude = v.Lat
	h.Longitude = v.Lon
	level, e := str(r, "alert_level")
	if e != nil {
		return h, e
	}

	h.Severity, ok = levels[level]
	if !ok {
		return h, fmt.Errorf("unknown alert_level")
	}

	count, e := read[int](r, "eruption_count_24h")
	if e != nil || count < 0 {
		return h, fmt.Errorf("invalid eruption_count_24h")
	}

	if _, e = num(r, "ash_column_height_m", 0, math.MaxFloat64); e != nil {
		return h, e
	}

	if _, ok = r["confidence_level"]; ok {
		if _, e = num(r, "confidence_level", 0, 1); e != nil {
			return h, e
		}
	}

	h.Attributes = attributes(r, "report_id", "reported_at", "alert_level")
	return h, nil
}
