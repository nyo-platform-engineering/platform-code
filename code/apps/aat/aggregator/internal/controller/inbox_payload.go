package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"aat/aggregator/internal/model"
)

const MaxProcessBytes = 2 << 20
const MaxCaptureBytes = 8 << 20

type RawPayload struct {
	BatchKey string
	Raw      []byte
}

func payloadError(raw []byte) error {
	return boundedPayloadError(raw, MaxProcessBytes)
}

func boundedPayloadError(raw []byte, limit int) error {
	if len(raw) > limit {
		return fmt.Errorf("payload_too_large")
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid_utf8")
	}
	depth, quoted, escaped := 0, false, false
	for _, ch := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		switch ch {
		case '"':
			quoted = true
		case '[', '{':
			depth++
			if depth > 64 {
				return fmt.Errorf("nesting_too_deep")
			}
		case ']', '}':
			depth--
		}
	}
	if !json.Valid(raw) {
		return fmt.Errorf("invalid_json")
	}
	return nil
}

func decodeEnvelope(raw []byte) ([]json.RawMessage, error) {
	// A valid historical batch may exceed the limit for an individual record.
	// Records are validated separately when processed from the durable inbox.
	if err := boundedPayloadError(raw, MaxCaptureBytes); err != nil {
		return nil, err
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("expected_record_array")
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("expected_record_array")
	}
	return records, nil
}

func decodeInboxRecord(raw []byte) (model.Record, error) {
	if err := payloadError(raw); err != nil {
		return nil, err
	}
	var record model.Record
	if err := json.Unmarshal(raw, &record); err != nil || record == nil {
		return nil, fmt.Errorf("expected_record_object")
	}
	return record, nil
}

func inboxID(source, key, kind string, raw []byte) string {
	hash := sha256.New()
	for _, value := range []string{source, key, kind} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	_, _ = hash.Write(raw)
	return hex.EncodeToString(hash.Sum(nil))
}

func validBatchKey(source, key string) bool {
	return source == "PVMBG" && key == "volcanic_reports" || source == "BMKG" && (key == "seismic_events" || key == "tsunami_warnings")
}

func inboxRef(key string, raw []byte) string {
	record, err := decodeInboxRecord(raw)
	if err != nil {
		return ""
	}
	field := map[string]string{"seismic_events": "event_id", "tsunami_warnings": "related_event_id", "volcanic_reports": "report_id"}[key]
	var ref string
	_ = json.Unmarshal(record[field], &ref)
	if len(ref) > 1024 {
		return ""
	}
	return ref
}
