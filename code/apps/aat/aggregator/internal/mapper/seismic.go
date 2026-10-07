package mapper

import (
	"aat/aggregator/internal/model"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

func Seismic(r model.Record, warnings []model.Record) (model.HazardEvent, error) {
	var h model.HazardEvent
	ref, e := String(r, "event_id")
	if e != nil {
		return h, e
	}

	at, e := Read[time.Time](r, "occurred_at")
	if e != nil {
		return h, e
	}

	h = Base("BMKG", ref, "SEISMIC", at)
	if h.Area, e = String(r, "region_name"); e != nil {
		return h, e
	}

	if h.Latitude, e = Number(r, "epicenter_lat", -90, 90); e != nil {
		return h, e
	}

	if h.Longitude, e = Number(r, "epicenter_lon", -180, 180); e != nil {
		return h, e
	}

	mag, e := Number(r, "magnitude", 0, math.MaxFloat64)
	if e != nil {
		return h, e
	}

	if _, e = Number(r, "depth_km", 0, math.MaxFloat64); e != nil {
		return h, e
	}

	potential, e := Read[bool](r, "potential_tsunami")
	if e != nil {
		return h, e
	}

	h.Severity = "NORMAL"
	if mag >= 6.5 {
		h.Severity = "SIAGA"
	} else if mag >= 5 {
		h.Severity = "WASPADA"
	}

	h.Attributes = Attributes(r, "event_id", "region_name", "epicenter_lat", "epicenter_lon", "occurred_at")
	if len(warnings) > 0 {
		if !potential {
			return h, fmt.Errorf("warning requires potential_tsunami=true")
		}

		h.Severity = "NORMAL"
		for _, w := range warnings {
			_, related, e := Tsunami(w)
			if e != nil {
				return h, e
			}

			if related != ref {
				return h, fmt.Errorf("unrelated warning")
			}

			level, _ := String(w, "threat_level")
			if Rank[Levels[level]] > Rank[h.Severity] {
				h.Severity = Levels[level]
			}
		}

		if _, collision := h.Attributes["tsunami_warnings"]; collision {
			return h, fmt.Errorf("reserved attribute tsunami_warnings")
		}

		h.Attributes["tsunami_warnings"], _ = json.Marshal(warnings)
	}

	return h, nil
}
