package fieldreport

import (
	"errors"
	"strings"
	"testing"
)

const validReport = `{"area_name":" Padang ","description":" Jalan terputus ","observed_at":"2026-11-18T08:00:00+07:00","latitude":-0.9492,"longitude":100.3543}`

func TestCanonicalReportNormalizesTextAndTime(t *testing.T) {
	report, err := Decode([]byte(validReport))
	if err != nil {
		t.Fatal(err)
	}
	canonical, hash, err := Canonical(report)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := `{"longitude":100.3543,"latitude":-0.9492,"observed_at":"2026-11-18T01:00:00Z","description":"Jalan terputus","area_name":"Padang"}`
	other, err := Decode([]byte(equivalent))
	if err != nil {
		t.Fatal(err)
	}
	_, sameHash, err := Canonical(other)
	if err != nil || hash != sameHash || !strings.Contains(string(canonical), `"observed_at":"2026-11-18T01:00:00Z"`) || report.AreaName != "Padang" {
		t.Fatalf("normalization differs: %s %v", canonical, err)
	}
}

func TestReportValidation(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`, validReport + ` {}`, `{"area_name":"Padang","description":"ok","observed_at":"bad"}`,
		strings.Replace(validReport, `"latitude":-0.9492,`, "", 1),
		strings.Replace(validReport, `100.3543`, `181`, 1),
		strings.Replace(validReport, `" Padang "`, `"   "`, 1),
		strings.Replace(validReport, `" Jalan terputus "`, `"`+strings.Repeat("x", 4001)+`"`, 1),
		strings.Replace(validReport, `"area_name"`, `"client_id"`, 1),
		strings.Replace(validReport, `" Jalan terputus "`, string([]byte{'"', 0xff, '"'}), 1),
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid body: %.100s", raw)
		}
	}
	if _, _, err := Read(strings.NewReader(strings.Repeat(" ", MaxBodyBytes+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize body not rejected: %v", err)
	}
}

func TestIdempotencyKeysAreBoundedAndScoped(t *testing.T) {
	for _, key := range []string{"", strings.Repeat("x", 129), "key with spaces", "key/segment", "é"} {
		if ValidKey(key) {
			t.Fatalf("accepted key %q", key)
		}
	}
	if !ValidKey("team-1:report_001.v1") || ID("responder", "key") == ID("other", "key") {
		t.Fatal("invalid key scoping")
	}
}
