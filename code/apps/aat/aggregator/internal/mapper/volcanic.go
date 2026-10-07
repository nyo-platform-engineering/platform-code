package mapper

import (
	"aat/aggregator/internal/model"
	"fmt"
	"math"
	"time"
)

// Coordinates are static demo references owned by BNPB, not navigation data.
var volcanoes = map[string]struct {
	Name     string
	Lat, Lon float64
}{
	"MERAPI":        {"Gunung Merapi", -7.54, 110.446},
	"SEMERU":        {"Gunung Semeru", -8.108, 112.922},
	"ANAK_KRAKATAU": {"Gunung Anak Krakatau", -6.102, 105.423},
}

func Volcanic(r model.Record) (model.HazardEvent, error) {
	var h model.HazardEvent
	ref, e := String(r, "report_id")
	if e != nil {
		return h, e
	}

	at, e := Read[time.Time](r, "reported_at")
	if e != nil {
		return h, e
	}

	h = Base("PVMBG", ref, "VOLCANIC", at)
	id, e := String(r, "volcano_id")
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
	level, e := String(r, "alert_level")
	if e != nil {
		return h, e
	}

	h.Severity, ok = Levels[level]
	if !ok {
		return h, fmt.Errorf("unknown alert_level")
	}

	count, e := Read[int](r, "eruption_count_24h")
	if e != nil || count < 0 {
		return h, fmt.Errorf("invalid eruption_count_24h")
	}

	if _, e = Number(r, "ash_column_height_m", 0, math.MaxFloat64); e != nil {
		return h, e
	}

	if _, ok = r["confidence_level"]; ok {
		if _, e = Number(r, "confidence_level", 0, 1); e != nil {
			return h, e
		}
	}

	h.Attributes = Attributes(r, "report_id", "reported_at", "alert_level")
	return h, nil
}
