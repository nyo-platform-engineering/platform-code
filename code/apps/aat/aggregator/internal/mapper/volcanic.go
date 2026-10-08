package mapper

import (
	"aat/aggregator/internal/model"
	"fmt"
	"math"
	"time"
)

/*
Kanonik (HazardEvent)
Asal (VolcanicReport)

source_ref_id
report_id

hazard_type
VOLCANIC

area_name
Nama gunung api hasil pemetaan dari volcano_id

latitude, longitude
Koordinat gunung api dari tabel referensi statis milik BNPB

occurred_at
reported_at

severity
alert_level dipetakan langsung ke enum kanonik

attributes
eruption_count_24h, ash_column_height_m, serta confidence_level bila tersedia
*/

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
	input, err := readVolcanic(r)
	if err != nil {
		return model.HazardEvent{}, err
	}

	v := volcanoes[input.VolcanoID]
	h := Base("PVMBG", input.Ref, "VOLCANIC", input.Reported)
	h.Area = v.Name
	h.Latitude = v.Lat
	h.Longitude = v.Lon
	h.Severity = Levels[input.AlertLevel]
	// report_id (x)
	// volcano_id
	// alert_level (x)
	// eruption_count_24h
	// ash_column_height_m
	// reported_at (x)
	// confidence_level
	h.Attributes = AttributesExcept(r, "report_id", "reported_at", "alert_level")
	return h, nil
}

type volcanicInput struct {
	Ref        string    `json:"report_id"`
	Reported   time.Time `json:"reported_at"`
	VolcanoID  string    `json:"volcano_id"`
	AlertLevel string    `json:"alert_level"`
}

// Before mapping: validate source fields and the reference data used by the mapping.
func readVolcanic(r model.Record) (volcanicInput, error) {
	if err := validateVolcanic(r); err != nil {
		return volcanicInput{}, err
	}
	return decodeRecord[volcanicInput](r)
}

func validateVolcanic(r model.Record) error {
	err := validateFields(r,
		requiredString("report_id"),
		requiredField[time.Time]("reported_at"),
		validateVolcanoID,
		validateAlertLevel,
		validateEruptionCount,
		boundedNumber("ash_column_height_m", 0, math.MaxFloat64),
	)
	if err != nil {
		return err
	}
	if _, present := r["confidence_level"]; present {
		return boundedNumber("confidence_level", 0, 1)(r)
	}
	return nil
}

func validateVolcanoID(r model.Record) error {
	id, err := String(r, "volcano_id")
	if err != nil {
		return err
	}
	if _, ok := volcanoes[id]; !ok {
		return fmt.Errorf("unknown volcano_id %q", id)
	}
	return nil
}

func validateAlertLevel(r model.Record) error {
	level, err := String(r, "alert_level")
	if err != nil {
		return err
	}
	if _, ok := Levels[level]; !ok {
		return fmt.Errorf("unknown alert_level")
	}
	return nil
}

func validateEruptionCount(r model.Record) error {
	count, err := Read[int](r, "eruption_count_24h")
	if err != nil || count < 0 {
		return fmt.Errorf("invalid eruption_count_24h")
	}
	return nil
}
