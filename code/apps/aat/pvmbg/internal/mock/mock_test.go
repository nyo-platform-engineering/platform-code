package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Each test creates its own mock so outage/schema state cannot leak between tests.
func TestRejectsBMKGCredential(t *testing.T) {
	handler := NewHandler("pvmbg-test-only", time.Hour, 0, 0)
	response := requestPVMBG(handler, "GET", "/volcanic-reports", "bmkg-test-only", "")
	requireStatus(t, response, http.StatusUnauthorized)
}

// Startup uses the original schema: 20 reports with no confidence_level field.
func TestReportsStartWithOriginalSchema(t *testing.T) {
	handler := NewHandler("pvmbg-test-only", time.Hour, 0, 0)
	rows := fetchReports(t, handler)
	if len(rows) != 20 {
		t.Fatalf("got %d seed reports, want 20", len(rows))
	}
	if _, ok := rows[0]["confidence_level"]; ok {
		t.Fatal("confidence_level must be absent before schema activation")
	}
}

// Enabling the new schema changes responses from the same running handler.
func TestSchemaActivationAddsConfidence(t *testing.T) {
	handler := NewHandler("pvmbg-test-only", time.Hour, 0, 0)
	response := requestPVMBG(handler, "POST", "/admin/schema-version", "pvmbg-test-only", `{"enabled":true}`)
	requireStatus(t, response, http.StatusOK)
	rows := fetchReports(t, handler)
	if _, ok := rows[0]["confidence_level"]; !ok {
		t.Fatal("confidence_level missing after schema activation")
	}
}

// Outage returns 503; disabling it restores reports without recreating the mock.
func TestOutageAndRecovery(t *testing.T) {
	handler := NewHandler("pvmbg-test-only", time.Hour, 0, 0)
	response := requestPVMBG(handler, "POST", "/admin/outage", "pvmbg-test-only", `{"enabled":true}`)
	requireStatus(t, response, http.StatusOK)
	response = requestPVMBG(handler, "GET", "/volcanic-reports", "pvmbg-test-only", "")
	requireStatus(t, response, http.StatusServiceUnavailable)

	response = requestPVMBG(handler, "POST", "/admin/outage", "pvmbg-test-only", `{"enabled":false}`)
	requireStatus(t, response, http.StatusOK)
	response = requestPVMBG(handler, "GET", "/volcanic-reports", "pvmbg-test-only", "")
	requireStatus(t, response, http.StatusOK)
}

// Invalid timestamps must be rejected instead of silently disabling filtering.
func TestReportsRejectInvalidSince(t *testing.T) {
	handler := NewHandler("pvmbg-test-only", time.Hour, 0, 0)
	response := requestPVMBG(handler, "GET", "/volcanic-reports?since=bad", "pvmbg-test-only", "")
	requireStatus(t, response, http.StatusBadRequest)
}

// requestPVMBG exercises routing/auth directly, without a network server.
func requestPVMBG(handler http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("got status %d, want %d: %s", response.Code, want, response.Body.String())
	}
}

func fetchReports(t *testing.T, handler http.Handler) []map[string]any {
	t.Helper()
	response := requestPVMBG(handler, "GET", "/volcanic-reports", "pvmbg-test-only", "")
	requireStatus(t, response, http.StatusOK)
	var rows []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode reports: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected seeded reports")
	}
	return rows
}
