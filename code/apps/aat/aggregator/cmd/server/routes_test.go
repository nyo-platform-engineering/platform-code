package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"aat/aggregator/internal/aggregate"
)

// Invalid credentials and payloads must fail before any database access.
func TestRouteValidationBeforeStoreAccess(t *testing.T) {
	// No database is needed for authentication and input validation failures.
	router := handler(aggregate.Store{}, "test-only")
	for _, tc := range []struct {
		name, method, path, token, body string
		status                          int
	}{
		{"missing_read_token", "GET", "/internal/hazards", "", "", 401},
		{"missing_bmkg_ingest_token", "POST", "/internal/ingest/bmkg", "", `{}`, 401},
		{"wrong_pvmbg_ingest_token", "POST", "/internal/ingest/pvmbg", "Bearer wrong", `{}`, 401},
		{"limit_below_minimum", "GET", "/internal/hazards?limit=0", "Bearer test-only", "", 400},
		{"unknown_source", "GET", "/internal/hazards?source=unknown", "Bearer test-only", "", 400},
		{"malformed_json", "POST", "/internal/ingest/bmkg", "Bearer test-only", `{`, 400},
		{"multiple_json_documents", "POST", "/internal/ingest/pvmbg", "Bearer test-only", `{} {}`, 400},
		{"body_over_two_mib", "POST", "/internal/ingest/pvmbg", "Bearer test-only", `{"padding":"` + strings.Repeat("x", 2<<20) + `"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", tc.token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}
