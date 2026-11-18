package mapper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"aat/aggregator/internal/model"
)

/*
Kebakaran -> HazardEvent (WILDFIRE), spec "Definisi Kebakaran" and "Aturan Pemetaan Tambahan":
a detection is keyed by satellite|acq_date|acq_time|latitude|longitude taken verbatim from the feed;
low confidence is excluded; two detections link at <= 1.0 km and <= 360 minutes, transitively;
two or more linked detections form one fire.
*/

const (
	LinkDistanceKm = 1.0
	LinkWindow     = 360 * time.Minute
	earthRadiusKm  = 6371.0088
)

// Feed event kinds. The stream reader puts the SSE event name in "event_type".
const (
	SatelliteDetected  = "detection"
	SatelliteUpdated   = "update"
	SatelliteRetracted = "retract"
)

type Detection struct {
	Key        string
	Satellite  string
	DetectedAt time.Time
	Latitude   float64
	Longitude  float64
	Confidence string // "l", "n", "h"; empty on a retraction
	FRP        float64
}

type SatelliteEvent struct {
	Kind      string
	Detection Detection
	// SetConfidence and SetFRP say which values the event carries. A detection
	// carries both; an update carries only what changed; a retraction neither.
	SetConfidence, SetFRP bool
}

var keyFields = []string{"satellite", "acq_date", "acq_time", "latitude", "longitude"}

// Satellite reads one feed event. A detection carries the full row (flat or under
// "detection"); update and retract name their detection by "key"
// (satellite|acq_date|acq_time|latitude|longitude) and an update lists only the
// changed fields under "changes".
func Satellite(r model.Record) (SatelliteEvent, error) {
	kind, err := String(r, "event_type")
	if err != nil {
		return SatelliteEvent{}, err
	}
	if kind != SatelliteDetected && kind != SatelliteUpdated && kind != SatelliteRetracted {
		return SatelliteEvent{}, fmt.Errorf("unknown event_type %q", kind)
	}

	parts := map[string]string{}
	if _, keyed := r["key"]; keyed && kind != SatelliteDetected {
		key, err := String(r, "key")
		if err != nil {
			return SatelliteEvent{}, err
		}
		fields := strings.Split(key, "|")
		if len(fields) != len(keyFields) {
			return SatelliteEvent{}, fmt.Errorf("invalid key %q", key)
		}
		for i, name := range keyFields {
			if parts[name] = strings.TrimSpace(fields[i]); parts[name] == "" {
				return SatelliteEvent{}, fmt.Errorf("invalid key %q", key)
			}
		}
	} else {
		for _, name := range keyFields {
			if parts[name], err = rawToken(r, name); err != nil {
				return SatelliteEvent{}, err
			}
		}
	}
	at, err := detectionTime(parts["acq_date"], parts["acq_time"])
	if err != nil {
		return SatelliteEvent{}, err
	}
	lat, err := coordinate(parts["latitude"], "latitude", 90)
	if err != nil {
		return SatelliteEvent{}, err
	}
	lon, err := coordinate(parts["longitude"], "longitude", 180)
	if err != nil {
		return SatelliteEvent{}, err
	}

	event := SatelliteEvent{Kind: kind, Detection: Detection{
		Key:        strings.Join([]string{parts["satellite"], parts["acq_date"], parts["acq_time"], parts["latitude"], parts["longitude"]}, "|"),
		Satellite:  parts["satellite"],
		DetectedAt: at,
		Latitude:   lat,
		Longitude:  lon,
	}}
	switch kind {
	case SatelliteDetected:
		if event.Detection.Confidence, err = confidence(r); err != nil {
			return SatelliteEvent{}, err
		}
		if event.Detection.FRP, err = Number(r, "frp", 0, math.MaxFloat64); err != nil {
			return SatelliteEvent{}, err
		}
		event.SetConfidence, event.SetFRP = true, true
	case SatelliteUpdated:
		values := r
		if raw, ok := r["changes"]; ok {
			if err := json.Unmarshal(raw, &values); err != nil || values == nil {
				return SatelliteEvent{}, fmt.Errorf("invalid changes")
			}
		}
		if _, ok := values["confidence"]; ok {
			if event.Detection.Confidence, err = confidence(values); err != nil {
				return SatelliteEvent{}, err
			}
			event.SetConfidence = true
		}
		if _, ok := values["frp"]; ok {
			if event.Detection.FRP, err = Number(values, "frp", 0, math.MaxFloat64); err != nil {
				return SatelliteEvent{}, err
			}
			event.SetFRP = true
		}
	}
	return event, nil
}

// rawToken returns a string or number field exactly as the feed wrote it, so
// detection keys keep the feed's own formatting (e.g. "-2.51230", "243").
func rawToken(r model.Record, key string) (string, error) {
	text := strings.TrimSpace(string(r[key]))
	if text == "" || text == "null" {
		return "", fmt.Errorf("missing/null %s", key)
	}
	if text[0] == '"' {
		var s string
		if err := json.Unmarshal([]byte(text), &s); err != nil {
			return "", fmt.Errorf("invalid %s: %w", key, err)
		}
		text = strings.TrimSpace(s)
	} else if _, err := strconv.ParseFloat(text, 64); err != nil {
		return "", fmt.Errorf("invalid %s: not a string or number", key)
	}
	if text == "" || strings.Contains(text, "|") {
		return "", fmt.Errorf("invalid %s", key)
	}
	return text, nil
}

// acq_date and acq_time (HHMM, leading zeros optional) are UTC overpass time.
func detectionTime(date, hhmm string) (time.Time, error) {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid acq_date: %w", err)
	}
	clock, err := strconv.Atoi(hhmm)
	if err != nil || len(hhmm) > 4 || clock < 0 || clock/100 > 23 || clock%100 > 59 {
		return time.Time{}, fmt.Errorf("invalid acq_time %q", hhmm)
	}
	return day.Add(time.Duration(clock/100)*time.Hour + time.Duration(clock%100)*time.Minute), nil
}

func coordinate(text, key string, limit float64) (float64, error) {
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || v < -limit || v > limit {
		return 0, fmt.Errorf("%s out of range", key)
	}
	return v, nil
}

func confidence(r model.Record) (string, error) {
	value, err := String(r, "confidence")
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "l", "low":
		return "l", nil
	case "n", "nominal":
		return "n", nil
	case "h", "high":
		return "h", nil
	}
	return "", fmt.Errorf("invalid confidence %q", value)
}

func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

func Linked(a, b Detection) bool {
	gap := a.DetectedAt.Sub(b.DetectedAt)
	if gap < 0 {
		gap = -gap
	}
	return gap <= LinkWindow && DistanceKm(a.Latitude, a.Longitude, b.Latitude, b.Longitude) <= LinkDistanceKm
}

// NeighborBox bounds every point within LinkDistanceKm, with a margin; Linked does the exact check.
func NeighborBox(lat, lon float64) (latMin, latMax, lonMin, lonMax float64) {
	dLat := LinkDistanceKm / 110.0
	dLon := dLat / math.Max(math.Cos(lat*math.Pi/180), 0.01)
	return lat - dLat, lat + dLat, lon - dLon, lon + dLon
}

// Components groups detections sorted by (DetectedAt, Key) into fires. Each
// group lists indices in ascending order, so group[0] is its earliest detection.
func Components(ds []Detection) [][]int {
	parent := make([]int, len(ds))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for i := range ds {
		for j := i + 1; j < len(ds) && ds[j].DetectedAt.Sub(ds[i].DetectedAt) <= LinkWindow; j++ {
			if Linked(ds[i], ds[j]) {
				a, b := find(i), find(j)
				if a != b {
					parent[max(a, b)] = min(a, b)
				}
			}
		}
	}
	groups := map[int][]int{}
	roots := []int{}
	for i := range ds {
		root := find(i)
		if _, seen := groups[root]; !seen {
			roots = append(roots, root)
		}
		groups[root] = append(groups[root], i)
	}
	result := make([][]int, 0, len(roots))
	for _, root := range roots {
		result = append(result, groups[root])
	}
	return result
}

// FireRef derives a fire identifier from its anchor detection, so replaying
// the same stream yields the same identifiers.
func FireRef(detectionKey string) string {
	sum := sha256.Sum256([]byte(detectionKey))
	return "fire-" + hex.EncodeToString(sum[:8])
}

func SortDetections(ds []Detection) {
	sort.Slice(ds, func(i, j int) bool {
		if !ds[i].DetectedAt.Equal(ds[j].DetectedAt) {
			return ds[i].DetectedAt.Before(ds[j].DetectedAt)
		}
		return ds[i].Key < ds[j].Key
	})
}

// Wildfire maps a fire (two or more eligible detections) to its HazardEvent.
// hazard_id comes from the anchor's time, so it depends only on the ref.
func Wildfire(ref string, anchorAt time.Time, members []Detection) model.HazardEvent {
	ds := append([]Detection(nil), members...)
	SortDetections(ds)
	h := Base("SATELIT", ref, "WILDFIRE", anchorAt)
	h.Occurred = ds[0].DetectedAt

	var lat, lon float64
	for _, d := range ds {
		lat += d.Latitude
		lon += d.Longitude
	}
	h.Latitude = round(lat/float64(len(ds)), 5)
	h.Longitude = round(lon/float64(len(ds)), 5)
	h.Area = NearestProvince(h.Latitude, h.Longitude)

	h.Attributes = WildfireAttributes(ds)
	h.Severity = WildfireSeverity(len(ds), totalFRP(ds))
	return h
}

// RetiredWildfire is the event of a fire that no longer has two detections: severity
// NORMAL, located at its anchor, with status "dissolved", or "merged" plus merged_into.
func RetiredWildfire(ref string, anchor Detection, members []Detection, mergedInto string) model.HazardEvent {
	h := Base("SATELIT", ref, "WILDFIRE", anchor.DetectedAt)
	h.Latitude, h.Longitude = anchor.Latitude, anchor.Longitude
	h.Area = NearestProvince(anchor.Latitude, anchor.Longitude)
	h.Severity = "NORMAL"
	h.Attributes = WildfireAttributes(members)
	if mergedInto != "" && len(members) == 0 {
		h.Attributes["status"] = json.RawMessage(`"merged"`)
		into, _ := json.Marshal(mergedInto)
		h.Attributes["merged_into"] = into
	} else {
		h.Attributes["status"] = json.RawMessage(`"dissolved"`)
	}
	return h
}

func WildfireSeverity(detections int, totalFRP float64) string {
	switch {
	case detections >= 20 || totalFRP >= 100:
		return "AWAS"
	case detections >= 6 || totalFRP >= 30:
		return "SIAGA"
	}
	return "WASPADA"
}

func WildfireAttributes(ds []Detection) model.Record {
	satellites := []string{}
	seen := map[string]bool{}
	keys := make([]string, 0, len(ds))
	best := ""
	for _, d := range ds {
		if !seen[d.Satellite] {
			seen[d.Satellite] = true
			satellites = append(satellites, d.Satellite)
		}
		keys = append(keys, d.Key)
		if confidenceRank[d.Confidence] > confidenceRank[best] {
			best = d.Confidence
		}
	}
	sort.Strings(satellites)
	sort.Strings(keys)

	attrs := model.Record{}
	set := func(key string, value any) {
		raw, _ := json.Marshal(value)
		attrs[key] = raw
	}
	set("detection_count", len(ds))
	set("satellites", satellites)
	if best == "" {
		set("max_confidence", nil)
	} else {
		set("max_confidence", best)
	}
	set("total_frp_mw", totalFRP(ds))
	set("detection_keys", keys)
	return attrs
}

var confidenceRank = map[string]int{"": 0, "l": 1, "n": 2, "h": 3}

func totalFRP(ds []Detection) float64 {
	sum := 0.0
	for _, d := range ds {
		sum += d.FRP
	}
	return round(sum, 2)
}

func round(v float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(v*scale) / scale
}
