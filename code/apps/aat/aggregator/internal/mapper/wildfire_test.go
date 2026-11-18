package mapper_test

import (
	"encoding/json"
	"testing"
	"time"

	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"
)

func satelliteRecord(t *testing.T, raw string) model.Record {
	t.Helper()
	var r model.Record
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSatelliteKeyKeepsFeedTextVerbatim(t *testing.T) {
	event, err := mapper.Satellite(satelliteRecord(t, `{"event_type":"detection","satellite":"N20","acq_date":"2026-11-12",
		"acq_time":"0243","latitude":-2.51230,"longitude":113.80440,"confidence":"h","frp":4.5}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "N20|2026-11-12|0243|-2.51230|113.80440"; event.Detection.Key != want {
		t.Fatalf("key %q, want %q (no rounding or reformatting)", event.Detection.Key, want)
	}
	if want := time.Date(2026, 11, 12, 2, 43, 0, 0, time.UTC); !event.Detection.DetectedAt.Equal(want) {
		t.Fatalf("detected at %s, want %s (acq_date + acq_time in UTC)", event.Detection.DetectedAt, want)
	}
	if !event.SetConfidence || !event.SetFRP {
		t.Fatal("a detection carries confidence and frp")
	}

	// The real feed writes acq_time as a number without leading zeros; the key keeps that text.
	event, err = mapper.Satellite(satelliteRecord(t, `{"event_type":"detection","satellite":"N","acq_date":"2026-11-18",
		"acq_time":15,"latitude":1.94583,"longitude":122.25594,"confidence":"low","frp":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.Detection.Key != "N|2026-11-18|15|1.94583|122.25594" || event.Detection.DetectedAt.Minute() != 15 || event.Detection.Confidence != "l" {
		t.Fatalf("unexpected detection %+v", event.Detection)
	}
}

func TestSatelliteReadsKeyedUpdatesAndRetractions(t *testing.T) {
	// Shapes as sent by the assistants' feed.
	event, err := mapper.Satellite(satelliteRecord(t, `{"event_type":"update","offset":77,
		"key":"N|2026-11-17|2358|-9.19203|111.36786","changes":{"bright_ti4":362.45,"frp":6.48}}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.Detection.Key != "N|2026-11-17|2358|-9.19203|111.36786" || !event.SetFRP || event.SetConfidence || event.Detection.FRP != 6.48 {
		t.Fatalf("partial update misread: %+v", event)
	}
	if want := time.Date(2026, 11, 17, 23, 58, 0, 0, time.UTC); !event.Detection.DetectedAt.Equal(want) {
		t.Fatalf("detected at %s, want %s", event.Detection.DetectedAt, want)
	}

	event, err = mapper.Satellite(satelliteRecord(t, `{"event_type":"update","key":"N20|2026-11-17|2350|2.39728|103.71227",
		"changes":{"bright_ti5":275.59,"confidence":"l"}}`))
	if err != nil || event.SetFRP || !event.SetConfidence || event.Detection.Confidence != "l" {
		t.Fatalf("confidence-only update misread: %+v, %v", event, err)
	}

	event, err = mapper.Satellite(satelliteRecord(t, `{"event_type":"update","key":"N20|2026-11-17|2350|2.39728|103.71227",
		"changes":{"bright_ti5":275.59}}`))
	if err != nil || event.SetFRP || event.SetConfidence {
		t.Fatalf("an update without confidence or frp must change neither: %+v, %v", event, err)
	}

	event, err = mapper.Satellite(satelliteRecord(t, `{"event_type":"retract","offset":158,"reason":"false_positive",
		"key":"N|2026-11-18|15|1.94583|122.25594"}`))
	if err != nil || event.Detection.DetectedAt.Minute() != 15 || event.Detection.Key != "N|2026-11-18|15|1.94583|122.25594" {
		t.Fatalf("keyed retraction misread: %+v, %v", event, err)
	}
}

func TestSatelliteRejectsBrokenEvents(t *testing.T) {
	for name, raw := range map[string]string{
		"missing satellite":  `{"event_type":"detection","acq_date":"2026-11-12","acq_time":"0243","latitude":1,"longitude":1,"confidence":"h","frp":1}`,
		"bad time":           `{"event_type":"detection","satellite":"N20","acq_date":"2026-11-12","acq_time":"2460","latitude":1,"longitude":1,"confidence":"h","frp":1}`,
		"latitude range":     `{"event_type":"detection","satellite":"N20","acq_date":"2026-11-12","acq_time":"0243","latitude":91,"longitude":1,"confidence":"h","frp":1}`,
		"unknown confidence": `{"event_type":"detection","satellite":"N20","acq_date":"2026-11-12","acq_time":"0243","latitude":1,"longitude":1,"confidence":"x","frp":1}`,
		"negative frp":       `{"event_type":"detection","satellite":"N20","acq_date":"2026-11-12","acq_time":"0243","latitude":1,"longitude":1,"confidence":"h","frp":-1}`,
		"unknown event":      `{"event_type":"other","satellite":"N20","acq_date":"2026-11-12","acq_time":"0243","latitude":1,"longitude":1}`,
		"short key":          `{"event_type":"retract","key":"N20|2026-11-17|2350|2.39728"}`,
		"bad update value":   `{"event_type":"update","key":"N20|2026-11-17|2350|2.39728|103.71227","changes":{"confidence":"x"}}`,
		"changes not object": `{"event_type":"update","key":"N20|2026-11-17|2350|2.39728|103.71227","changes":5}`,
		"key with bad lat":   `{"event_type":"retract","key":"N20|2026-11-17|2350|abc|103.71227"}`,
	} {
		if _, err := mapper.Satellite(satelliteRecord(t, raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func at(minutes int) time.Time {
	return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

// kmNorth moves about km kilometres north of latitude 0.
func kmNorth(km float64) float64 { return km / 111.195 }

func TestLinkedUsesDistanceAndTimeBoundaries(t *testing.T) {
	base := mapper.Detection{Key: "a", DetectedAt: at(0)}
	cases := []struct {
		name   string
		other  mapper.Detection
		linked bool
	}{
		{"0.9 km, same time", mapper.Detection{Latitude: kmNorth(0.9), DetectedAt: at(0)}, true},
		{"1.1 km", mapper.Detection{Latitude: kmNorth(1.1), DetectedAt: at(0)}, false},
		{"360 min apart", mapper.Detection{DetectedAt: at(360)}, true},
		{"361 min apart", mapper.Detection{DetectedAt: at(361)}, false},
	}
	for _, c := range cases {
		if got := mapper.Linked(base, c.other); got != c.linked {
			t.Errorf("%s: linked=%v, want %v", c.name, got, c.linked)
		}
	}
}

func TestComponentsChainLinksTransitively(t *testing.T) {
	ds := []mapper.Detection{
		{Key: "a", DetectedAt: at(0), Latitude: 0},
		{Key: "b", DetectedAt: at(10), Latitude: kmNorth(0.9)},
		{Key: "c", DetectedAt: at(20), Latitude: kmNorth(1.8)}, // 1.8 km from a, linked through b
		{Key: "d", DetectedAt: at(30), Latitude: kmNorth(10)},
	}
	groups := mapper.Components(ds)
	if len(groups) != 2 || len(groups[0]) != 3 || len(groups[1]) != 1 || groups[1][0] != 3 {
		t.Fatalf("groups %v, want [[0 1 2] [3]]", groups)
	}
}

func TestWildfireSeverityThresholds(t *testing.T) {
	cases := []struct {
		count int
		frp   float64
		want  string
	}{
		{2, 0, "WASPADA"}, {5, 29.99, "WASPADA"},
		{6, 0, "SIAGA"}, {2, 30, "SIAGA"}, {19, 99.99, "SIAGA"},
		{20, 0, "AWAS"}, {2, 100, "AWAS"},
	}
	for _, c := range cases {
		if got := mapper.WildfireSeverity(c.count, c.frp); got != c.want {
			t.Errorf("count=%d frp=%v: %s, want %s", c.count, c.frp, got, c.want)
		}
	}
}

func TestWildfireMapsAFireSeenByTwoSatellites(t *testing.T) {
	members := []mapper.Detection{
		{Key: "N21|2026-11-12|0245|-2.51500|113.80600", Satellite: "N21", DetectedAt: at(165), Latitude: -2.515, Longitude: 113.806, Confidence: "n", FRP: 12},
		{Key: "N20|2026-11-12|0243|-2.51230|113.80440", Satellite: "N20", DetectedAt: at(163), Latitude: -2.5123, Longitude: 113.8044, Confidence: "h", FRP: 20.5},
	}
	ref := mapper.FireRef(members[1].Key)
	if ref != mapper.FireRef(members[1].Key) || ref == mapper.FireRef(members[0].Key) {
		t.Fatal("fire refs must be stable per anchor and distinct across anchors")
	}
	h := mapper.Wildfire(ref, members[1].DetectedAt, members)
	if h.Source != "SATELIT" || h.Type != "WILDFIRE" || h.Ref != ref {
		t.Fatalf("unexpected identity %+v", h)
	}
	if !h.Occurred.Equal(at(163)) {
		t.Fatalf("occurred_at %s, want the earliest detection", h.Occurred)
	}
	if h.Severity != "SIAGA" { // total FRP 32.5 MW
		t.Fatalf("severity %s, want SIAGA", h.Severity)
	}
	if h.Area != "Kalimantan Tengah" {
		t.Fatalf("area %q, want Kalimantan Tengah", h.Area)
	}
	var attrs struct {
		Count      int      `json:"detection_count"`
		Satellites []string `json:"satellites"`
		Confidence string   `json:"max_confidence"`
		FRP        float64  `json:"total_frp_mw"`
		Keys       []string `json:"detection_keys"`
	}
	raw, _ := json.Marshal(h.Attributes)
	if err := json.Unmarshal(raw, &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs.Count != 2 || len(attrs.Satellites) != 2 || attrs.Satellites[0] != "N20" || attrs.Confidence != "h" ||
		attrs.FRP != 32.5 || len(attrs.Keys) != 2 || attrs.Keys[0] != members[1].Key {
		t.Fatalf("unexpected attributes %+v", attrs)
	}
}
