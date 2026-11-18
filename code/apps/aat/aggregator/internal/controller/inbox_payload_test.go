package controller

import (
	"strings"
	"testing"
)

func TestInboxPayloadValidation(t *testing.T) {
	for _, tc := range []struct {
		raw    []byte
		reason string
	}{
		{[]byte(`[{}]`), ""},
		{[]byte(`[{}] {}`), "invalid_json"},
		{[]byte("[\xff]"), "invalid_utf8"},
		{[]byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)), "nesting_too_deep"},
		{[]byte(strings.Repeat("x", MaxProcessBytes+1)), "payload_too_large"},
		{[]byte(`[{"text":"[[[[\\\""}]`), ""},
	} {
		err := payloadError(tc.raw)
		if tc.reason == "" && err != nil || tc.reason != "" && (err == nil || err.Error() != tc.reason) {
			t.Fatalf("reason=%q err=%v", tc.reason, err)
		}
	}
}

func TestInboxLargeEnvelopeKeepsIndividualRecordLimit(t *testing.T) {
	record := `{"padding":"` + strings.Repeat("x", 1<<20) + `"}`
	records, err := decodeEnvelope([]byte("[" + record + "," + record + "]"))
	if err != nil || len(records) != 2 {
		t.Fatalf("valid batch over 2 MiB rejected: %d %v", len(records), err)
	}
	for _, raw := range records {
		if _, err := decodeInboxRecord(raw); err != nil {
			t.Fatalf("valid individual record rejected: %v", err)
		}
	}
	oversized := `{"padding":"` + strings.Repeat("x", MaxProcessBytes) + `"}`
	records, err = decodeEnvelope([]byte("[" + oversized + "]"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeInboxRecord(records[0]); err == nil || err.Error() != "payload_too_large" {
		t.Fatalf("individual record limit bypassed: %v", err)
	}
	if _, err := decodeEnvelope([]byte("[" + strings.Repeat(" ", MaxCaptureBytes) + "]")); err == nil || err.Error() != "payload_too_large" {
		t.Fatalf("envelope capture limit bypassed: %v", err)
	}
}
