package internal

import (
	"net/http/httptest"
	"strings"
	"testing"

	"aat/aggregator/internal/controller"
	"aat/internal/fieldreport"
)

func TestFieldReportValidationBeforeDatabaseAccess(t *testing.T) {
	for _, tc := range []struct {
		token, client, key, body string
		status                   int
	}{
		{"", "responder", "key", `{}`, 401},
		{"Bearer app", "analyst", "key", `{}`, 403},
		{"Bearer app", "responder", "", `{}`, 400},
		{"Bearer app", "responder", "key", `{}`, 400},
		{"Bearer app", "responder", "key", strings.Repeat(" ", fieldreport.MaxBodyBytes+1), 413},
	} {
		handler := NewHandler(&controller.Controller{}, "app")
		r := httptest.NewRequest("POST", "/internal/field-reports", strings.NewReader(tc.body))
		r.Header.Set("Authorization", tc.token)
		r.Header.Set("X-Client-ID", tc.client)
		r.Header.Set("Idempotency-Key", tc.key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d", w.Code, tc.status)
		}
	}
}
