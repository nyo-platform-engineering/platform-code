package mapper

import (
	"aat/aggregator/internal/model"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

/*
Kanonik (HazardEvent)
Asal (SeismicEvent)

source_ref_id
event_id

hazard_type
SEISMIC

area_name
region_name

latitude, longitude
epicenter_lat, epicenter_lon

occurred_at
occurred_at

severity
AWAS bila terdapat TsunamiWarning dengan threat_level = Awas; selain itu mengikuti threat_level dari TsunamiWarning terkait; bila tidak ada warning, NORMAL untuk magnitude < 5.0, WASPADA untuk 5.0 ≤ magnitude < 6.5, dan SIAGA untuk magnitude ≥ 6.5

attributes
magnitude, depth_km, potential_tsunami, serta field TsunamiWarning terkait bila ada
*/

func Seismic(r model.Record, warnings []model.Record) (model.HazardEvent, error) {
	input, err := readSeismic(r)
	if err != nil {
		return model.HazardEvent{}, err
	}

	h := Base("BMKG", input.Ref, "SEISMIC", input.Occurred)
	h.Area = input.Area
	h.Latitude = input.Latitude
	h.Longitude = input.Longitude
	h.Severity = seismicSeverity(input.Magnitude)
	// event_id (x)
	// magnitude
	// depth_km
	// epicenter_lat (x)
	// epicenter_lon (x)
	// region_name (x)
	// occurred_at (x)
	// potential_tsunami
	h.Attributes = AttributesExcept(r, "event_id", "region_name", "epicenter_lat", "epicenter_lon", "occurred_at")

	if err := applyTsunamiWarnings(&h, input.PotentialTsunami, warnings); err != nil {
		return h, err
	}
	return h, nil
}

type seismicInput struct {
	Ref              string    `json:"event_id"`
	Occurred         time.Time `json:"occurred_at"`
	Area             string    `json:"region_name"`
	Latitude         float64   `json:"epicenter_lat"`
	Longitude        float64   `json:"epicenter_lon"`
	Magnitude        float64   `json:"magnitude"`
	PotentialTsunami bool      `json:"potential_tsunami"`
}

// Before mapping: read and validate the source fields, keeping the raw record intact.
func readSeismic(r model.Record) (seismicInput, error) {
	if err := validateSeismic(r); err != nil {
		return seismicInput{}, err
	}
	return decodeRecord[seismicInput](r)
}

func validateSeismic(r model.Record) error {
	return validateFields(r,
		requiredString("event_id"),
		requiredField[time.Time]("occurred_at"),
		requiredString("region_name"),
		boundedNumber("epicenter_lat", -90, 90),
		boundedNumber("epicenter_lon", -180, 180),
		boundedNumber("magnitude", 0, math.MaxFloat64),
		boundedNumber("depth_km", 0, math.MaxFloat64),
		requiredField[bool]("potential_tsunami"),
	)
}

func seismicSeverity(magnitude float64) string {
	if magnitude >= 6.5 {
		return "SIAGA"
	}
	if magnitude >= 5 {
		return "WASPADA"
	}
	return "NORMAL"
}

// After mapping: related warnings override severity and enrich the attributes.
func applyTsunamiWarnings(h *model.HazardEvent, potential bool, warnings []model.Record) error {
	if len(warnings) == 0 {
		return nil
	}
	if !potential {
		return fmt.Errorf("warning requires potential_tsunami=true")
	}

	h.Severity = "NORMAL"
	for _, w := range warnings {
		_, related, e := Tsunami(w)
		if e != nil {
			return e
		}

		if related != h.Ref {
			return fmt.Errorf("unrelated warning")
		}

		level, _ := String(w, "threat_level")
		if Rank[Levels[level]] > Rank[h.Severity] {
			h.Severity = Levels[level]
		}
	}

	if _, collision := h.Attributes["tsunami_warnings"]; collision {
		return fmt.Errorf("seismic event payload must not contain tsunami_warnings; fetch warnings from the /tsunami-warnings endpoint")
	}
	h.Attributes["tsunami_warnings"], _ = json.Marshal(warnings)
	return nil
}
