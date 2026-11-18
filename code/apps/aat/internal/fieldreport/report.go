package fieldreport

// AI-assisted implementation.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxBodyBytes = 16 << 10

var ErrTooLarge = errors.New("field report exceeds 16 KiB")

type Submission struct {
	AreaName    string    `json:"area_name"`
	Description string    `json:"description"`
	ObservedAt  time.Time `json:"observed_at"`
	Latitude    *float64  `json:"latitude,omitempty"`
	Longitude   *float64  `json:"longitude,omitempty"`
	HazardID    string    `json:"hazard_id,omitempty"`
}

type Receipt struct {
	ReportID   string    `json:"report_id"`
	Status     string    `json:"status"`
	AcceptedAt time.Time `json:"accepted_at"`
}

func ValidKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, ch := range key {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("._:-", ch)) {
			return false
		}
	}
	return true
}

func ID(client, key string) string {
	hash := sha256.Sum256([]byte(client + "\x00" + key))
	return "fr-" + hex.EncodeToString(hash[:])
}

func Read(reader io.Reader) ([]byte, Submission, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxBodyBytes+1))
	if err != nil {
		return nil, Submission{}, err
	}
	report, err := Decode(raw)
	return raw, report, err
}

func Decode(raw []byte) (Submission, error) {
	var report Submission
	if len(raw) > MaxBodyBytes {
		return report, ErrTooLarge
	}
	if !utf8.Valid(raw) {
		return report, fmt.Errorf("report must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("invalid report JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return report, fmt.Errorf("expected one report")
	}
	report.AreaName = strings.TrimSpace(report.AreaName)
	report.Description = strings.TrimSpace(report.Description)
	report.HazardID = strings.TrimSpace(report.HazardID)
	report.ObservedAt = report.ObservedAt.UTC()
	if report.AreaName == "" || utf8.RuneCountInString(report.AreaName) > 200 {
		return report, fmt.Errorf("area_name must contain 1..200 characters")
	}
	if report.Description == "" || utf8.RuneCountInString(report.Description) > 4000 {
		return report, fmt.Errorf("description must contain 1..4000 characters")
	}
	if report.ObservedAt.IsZero() {
		return report, fmt.Errorf("observed_at is required")
	}
	if len(report.HazardID) > 256 {
		return report, fmt.Errorf("hazard_id is too long")
	}
	if (report.Latitude == nil) != (report.Longitude == nil) {
		return report, fmt.Errorf("provide both coordinates")
	}
	if report.Latitude != nil {
		lat, lon := *report.Latitude, *report.Longitude
		if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return report, fmt.Errorf("invalid coordinates")
		}
	}
	return report, nil
}

func Canonical(report Submission) ([]byte, string, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, "", err
	}
	normalized, err := Decode(raw)
	if err != nil {
		return nil, "", err
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(raw)
	return raw, hex.EncodeToString(hash[:]), nil
}
