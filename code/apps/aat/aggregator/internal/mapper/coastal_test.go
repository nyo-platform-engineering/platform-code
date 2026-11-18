package mapper_test

import (
	"encoding/json"
	"strings"
	"testing"

	"aat/aggregator/internal/mapper"
)

var stations = map[string]mapper.Station{
	"stn-cilacap-01": {ID: "stn-cilacap-01", Name: "Cilacap", Province: "Jawa Tengah", Latitude: -7.7279, Longitude: 109.015},
}

const goodObservation = `{"critical":false,"observation_id":"obs-000002","observed_at":"2026-11-12T00:00:30Z","quality":"ok",
	"sea_level_anomaly_m":0.25,"station_id":"stn-cilacap-01","wave_height_m":1.37}`

func TestCoastalMapsAValidObservation(t *testing.T) {
	h, err := mapper.Coastal(satelliteRecord(t, goodObservation), stations)
	if err != nil {
		t.Fatal(err)
	}
	if h.Source != "SENSOR_PANTAI" || h.Type != "COASTAL" || h.Ref != "obs-000002" || h.Area != "Jawa Tengah" ||
		h.Latitude != -7.7279 || h.Severity != "NORMAL" || h.Occurred.Second() != 30 {
		t.Fatalf("unexpected mapping %+v", h)
	}
	raw, _ := json.Marshal(h.Attributes)
	for _, want := range []string{`"station_id":"stn-cilacap-01"`, `"quality":"ok"`, `"critical":false`, `"wave_height_m":1.37`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("attributes %s missing %s", raw, want)
		}
	}
	// Whole numbers sent as 1 instead of 1.0 are still numbers.
	if _, err := mapper.Coastal(satelliteRecord(t, strings.Replace(goodObservation, "1.37", "1", 1)), stations); err != nil {
		t.Fatal(err)
	}
}

func TestCoastalRejectsContractViolations(t *testing.T) {
	for name, change := range map[string][2]string{
		"number became text":   {`"wave_height_m":1.37`, `"wave_height_m":"1.37"`},
		"renamed field":        {`"sea_level_anomaly_m"`, `"sea_level_m"`},
		"missing id":           {`"observation_id":"obs-000002",`, ``},
		"unknown station":      {`stn-cilacap-01`, `stn-nowhere-01`},
		"bad timestamp":        {`2026-11-12T00:00:30Z`, `12/11/2026 00:00`},
		"unknown quality":      {`"quality":"ok"`, `"quality":"great"`},
		"critical not boolean": {`"critical":false`, `"critical":"no"`},
		"critical missing":     {`"critical":false,`, ``},
		"impossible wave":      {`"wave_height_m":1.37`, `"wave_height_m":-2`},
		"impossible anomaly":   {`"sea_level_anomaly_m":0.25`, `"sea_level_anomaly_m":999`},
	} {
		raw := strings.Replace(goodObservation, change[0], change[1], 1)
		if raw == goodObservation {
			t.Fatalf("%s: fixture replacement did nothing", name)
		}
		if _, err := mapper.Coastal(satelliteRecord(t, raw), stations); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCoastalSeverityNeverDowngradesCritical(t *testing.T) {
	cases := []struct {
		critical bool
		quality  string
		anomaly  float64
		wave     float64
		want     string
	}{
		{true, "ok", 0, 0, "AWAS"},
		{true, "suspect", 0, 0, "AWAS"}, // the sensor's critical flag always wins
		{false, "ok", 0.6, 1, "SIAGA"},
		{false, "ok", -0.6, 1, "SIAGA"}, // a drawdown before a tsunami counts too
		{false, "ok", 0, 4.2, "SIAGA"},
		{false, "suspect", 2.1, 5, "WASPADA"}, // unreliable data cannot raise SIAGA alone
		{false, "ok", 0.35, 1, "WASPADA"},
		{false, "ok", 0.1, 1, "NORMAL"},
	}
	for _, c := range cases {
		if got := mapper.CoastalSeverity(c.critical, c.quality, c.anomaly, c.wave); got != c.want {
			t.Errorf("%+v: %s, want %s", c, got, c.want)
		}
	}
	if !mapper.CoastalCritical(satelliteRecord(t, `{"critical":true,"wave_height_m":"broken"}`)) {
		t.Error("a broken reading's critical flag must still be readable")
	}
}
