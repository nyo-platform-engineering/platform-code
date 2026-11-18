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
