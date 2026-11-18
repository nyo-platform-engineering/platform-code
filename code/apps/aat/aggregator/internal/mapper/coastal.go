package mapper

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"aat/aggregator/internal/model"
)

/*
Sensor Pantai observation -> HazardEvent (COASTAL), following the base contract
(Lampiran C): observation_id, station_id, observed_at, sea_level_anomaly_m,
wave_height_m, quality, critical. A reading that breaks the contract is an error
here and is parked by the inbox with its reason instead of becoming an event.
*/

// Station is one entry of the sensor's GET /v1/stations.
type Station struct {
	ID        string  `json:"station_id"`
	Name      string  `json:"name"`
	Province  string  `json:"province"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Physical bounds outside which a reading is not a measurement but a fault.
const (
	maxSeaLevelAnomalyM = 30.0
	maxWaveHeightM      = 30.0
	maxObservationID    = 200
)

// Severity thresholds for non-critical readings. The sensor's own critical flag
// always maps to AWAS; these grade everything else.
const (
	siagaAnomalyM   = 0.5
	siagaWaveM      = 4.0
	waspadaAnomalyM = 0.3
	waspadaWaveM    = 2.5
)

// CoastalCritical reads only the critical flag, for readings that fail the
// contract elsewhere but still claim to be critical.
func CoastalCritical(r model.Record) bool {
	var critical bool
	return json.Unmarshal(r["critical"], &critical) == nil && critical
}

// Coastal validates one observation against the contract and maps it.
// stations must hold the sensor's station list; an unknown station is a violation.
func Coastal(r model.Record, stations map[string]Station) (model.HazardEvent, error) {
	id, err := String(r, "observation_id")
	if err != nil {
		return model.HazardEvent{}, err
	}
	if len(id) > maxObservationID || strings.TrimSpace(id) != id {
		return model.HazardEvent{}, fmt.Errorf("invalid observation_id")
	}
	stationID, err := String(r, "station_id")
	if err != nil {
		return model.HazardEvent{}, err
	}
	station, known := stations[stationID]
	if !known {
		return model.HazardEvent{}, fmt.Errorf("unknown station_id %q", stationID)
	}
	observedText, err := String(r, "observed_at")
	if err != nil {
		return model.HazardEvent{}, err
	}
	observed, err := time.Parse(time.RFC3339, observedText)
	if err != nil {
		return model.HazardEvent{}, fmt.Errorf("invalid observed_at: not RFC3339")
	}
	anomaly, err := Number(r, "sea_level_anomaly_m", -maxSeaLevelAnomalyM, maxSeaLevelAnomalyM)
	if err != nil {
		return model.HazardEvent{}, err
	}
	wave, err := Number(r, "wave_height_m", 0, maxWaveHeightM)
	if err != nil {
		return model.HazardEvent{}, err
	}
	quality, err := String(r, "quality")
	if err != nil {
		return model.HazardEvent{}, err
	}
	if quality != "ok" && quality != "suspect" {
		return model.HazardEvent{}, fmt.Errorf("invalid quality %q", quality)
	}
	raw, present := r["critical"]
	var critical bool
	if !present || json.Unmarshal(raw, &critical) != nil {
		return model.HazardEvent{}, fmt.Errorf("missing/invalid critical: must be a boolean")
	}

	h := Base("SENSOR_PANTAI", id, "COASTAL", observed)
	h.Area = station.Province
	h.Latitude, h.Longitude = station.Latitude, station.Longitude
	h.Severity = CoastalSeverity(critical, quality, anomaly, wave)
	h.Attributes = model.Record{}
	set := func(key string, value any) {
		b, _ := json.Marshal(value)
		h.Attributes[key] = b
	}
	set("station_id", station.ID)
	set("station_name", station.Name)
	set("sea_level_anomaly_m", anomaly)
	set("wave_height_m", wave)
	set("quality", quality)
	set("critical", critical)
	return h, nil
}

// CoastalSeverity: the sensor's critical flag is AWAS whatever the quality, so no
// critical warning is ever downgraded. A suspect reading is kept but cannot raise
// more than WASPADA on its own values.
func CoastalSeverity(critical bool, quality string, anomaly, wave float64) string {
	if critical {
		return "AWAS"
	}
	level := "NORMAL"
	switch {
	case math.Abs(anomaly) >= siagaAnomalyM || wave >= siagaWaveM:
		level = "SIAGA"
	case math.Abs(anomaly) >= waspadaAnomalyM || wave >= waspadaWaveM:
		level = "WASPADA"
	}
	if quality == "suspect" && level == "SIAGA" {
		level = "WASPADA"
	}
	return level
}
