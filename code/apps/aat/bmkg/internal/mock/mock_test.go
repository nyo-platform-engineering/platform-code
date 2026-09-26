package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// BMKG credentials must not be interchangeable with PVMBG credentials.
func TestRejectsPVMBGCredential(t *testing.T) {
	handler := NewHandler("bmkg-test-only", time.Second, 0)
	response := requestBMKG(handler, "/seismic-events", "pvmbg-test-only")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", response.Code)
	}
}

// A new mock exposes 20 historical events before the next generation interval.
func TestSeismicEndpointReturnsSeedEvents(t *testing.T) {
	handler := NewHandler("bmkg-test-only", time.Hour, 0)
	response := requestBMKG(handler, "/seismic-events", "bmkg-test-only")
	rows := decodeBMKGRows(t, response)
	if len(rows) != 20 {
		t.Fatalf("got %d seed events, want 20", len(rows))
	}
}

// Warnings have their own endpoint and reference the related seismic event.
func TestWarningEndpointReturnsEventReferences(t *testing.T) {
	handler := NewHandler("bmkg-test-only", time.Hour, 0)
	response := requestBMKG(handler, "/tsunami-warnings", "bmkg-test-only")
	rows := decodeBMKGRows(t, response)
	if len(rows) == 0 || rows[0]["related_event_id"] == nil {
		t.Fatalf("missing warning event reference: %s", response.Body.String())
	}
}

// requestBMKG exercises the router without starting a network server.
func requestBMKG(handler http.Handler, path, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("X-BMKG-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeBMKGRows(t *testing.T, response *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200: %s", response.Code, response.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return rows
}
