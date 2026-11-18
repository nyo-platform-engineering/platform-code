package internal

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
)

type auditStore struct{ query storage.EventQuery }

func (*auditStore) List(context.Context, string, string, int) ([]model.HazardEvent, error) {
	panic("unexpected legacy query")
}
func (s *auditStore) Events(_ context.Context, q storage.EventQuery) (storage.EventPage, error) {
	s.query = q
	return storage.EventPage{Items: []model.HazardEvent{}, Availability: storage.Availability{Partial: true, UnavailableUnits: []string{"unit-offline"}}}, nil
}
func (*auditStore) Checksum(context.Context, string) (storage.Checksum, error) {
	return storage.Checksum{}, storage.ErrUnknownNode
}
func (*auditStore) Locations(context.Context, string) (storage.Locations, error) {
	return storage.Locations{}, storage.ErrNotFound
}

func TestAuditAuthenticationAndValidation(t *testing.T) {
	for _, tc := range []struct {
		path, token, auditToken string
		status                  int
		code                    string
	}{
		{"/internal/audit/events", "", "audit-test", 401, "unauthorized"},
		{"/internal/audit/events", "Bearer app-test", "audit-test", 401, "unauthorized"},
		{"/internal/audit/events", "Bearer audit-test", "", 503, "unavailable"},
		{"/internal/audit/events?limit=0", "Bearer audit-test", "audit-test", 400, "bad_request"},
		{"/internal/audit/events?limit=1001", "Bearer audit-test", "audit-test", 400, "bad_request"},
		{"/internal/audit/events?since=bad", "Bearer audit-test", "audit-test", 400, "bad_request"},
		{"/internal/audit/events?source=OTHER", "Bearer audit-test", "audit-test", 400, "bad_request"},
		{"/internal/audit/location", "Bearer audit-test", "audit-test", 400, "bad_request"},
	} {
		t.Run(tc.path+tc.token+tc.auditToken, func(t *testing.T) {
			router := NewHandlerWithAudit(&controller.Controller{}, "app-test", tc.auditToken)
			request := httptest.NewRequest("GET", tc.path, nil)
			request.Header.Set("Authorization", tc.token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("%d %s", response.Code, response.Body)
			}
		})
	}
}

func TestAuditPartialAndQueryContract(t *testing.T) {
	store := &auditStore{}
	router := NewHandlerWithAudit(&controller.Controller{Store: store}, "app-test", "audit-test")
	request := httptest.NewRequest("GET", "/internal/audit/events?source=SATELIT&limit=7&since=2026-11-12T09:00:00%2B07:00&cursor=opaque", nil)
	request.Header.Set("Authorization", "Bearer audit-test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var body storage.EventPage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !body.Partial || len(body.UnavailableUnits) != 1 || body.Items == nil || body.NextCursor != nil {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	if store.query.Source != "SATELIT" || store.query.Limit != 7 || store.query.Cursor != "opaque" || store.query.Since.Format("2006-01-02T15:04:05Z07:00") != "2026-11-12T02:00:00Z" {
		t.Fatalf("query: %+v", store.query)
	}
	for _, path := range []string{"/internal/audit/location?hazard_id=absent", "/internal/audit/checksum"} {
		request = httptest.NewRequest("GET", path, nil)
		request.Header.Set("Authorization", "Bearer audit-test")
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		expected := 404
		if path == "/internal/audit/checksum" {
			expected = 503
		}
		if response.Code != expected {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body)
		}
	}
}
