package api

import (
	"encoding/json"
	"math"
	"time"
)

// This wire model deliberately ignores new top-level upstream fields.
type Hazard struct {
	ID         string                     `json:"hazard_id"`
	Source     string                     `json:"source"`
	Ref        string                     `json:"source_ref_id"`
	Type       string                     `json:"hazard_type"`
	Severity   string                     `json:"severity"`
	Area       string                     `json:"area_name"`
	Latitude   *float64                   `json:"latitude"`
	Longitude  *float64                   `json:"longitude"`
	Occurred   time.Time                  `json:"occurred_at"`
	Ingested   time.Time                  `json:"ingested_at"`
	Attributes map[string]json.RawMessage `json:"attributes"`
}

func identityKnown(id string) bool { return id == "public" || id == "responder" || id == "analyst" }

func (h Hazard) valid() bool {
	if h.ID == "" || h.Ref == "" || h.Area == "" || h.Occurred.IsZero() || h.Ingested.IsZero() || h.Attributes == nil {
		return false
	}
	if h.Source != "BMKG" && h.Source != "PVMBG" {
		return false
	}
	if h.Type != "SEISMIC" && h.Type != "VOLCANIC" {
		return false
	}
	if h.Severity != "NORMAL" && h.Severity != "WASPADA" && h.Severity != "SIAGA" && h.Severity != "AWAS" {
		return false
	}
	return h.Latitude != nil && h.Longitude != nil && !math.IsNaN(*h.Latitude) && !math.IsNaN(*h.Longitude) && *h.Latitude >= -90 && *h.Latitude <= 90 && *h.Longitude >= -180 && *h.Longitude <= 180
}

func (h Hazard) project(identity string) map[string]any {
	fields := map[string]any{"hazard_id": h.ID, "hazard_type": h.Type, "severity": h.Severity, "area_name": h.Area, "occurred_at": h.Occurred, "source": h.Source, "ingested_at": h.Ingested}
	if identity == "responder" || identity == "analyst" {
		fields["latitude"], fields["longitude"] = *h.Latitude, *h.Longitude
		fields["source_ref_id"], fields["attributes"] = h.Ref, h.Attributes
	}
	return fields
}

// Field permissions follow the M1 classification, including future deny-by-default fields.
func allowedField(identity, field string) bool {
	switch field {
	case "hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at":
		return true
	case "source_ref_id", "latitude", "longitude", "attributes":
		return identity == "responder" || identity == "analyst"
	}
	return false
}
