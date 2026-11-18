package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aat/internal/fieldreport"
)

const reportBody = `{"area_name":"Padang","description":"Jalan terputus","observed_at":"2026-11-18T01:00:00Z"}`

func reportRequest(h http.Handler, token, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/field-reports", strings.NewReader(body))
	r.Header.Set("Authorization", token)
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("X-Correlation-ID", "report-trace")
	r.Header.Set("X-Client-ID", "analyst")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestFieldReportsOnlyAcceptTimLapangan(t *testing.T) {
	for _, identity := range []string{"public", "analyst", "responder"} {
		t.Run(identity, func(t *testing.T) {
			writes := 0
			h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "auth" {
					return response(200, `{"active":true,"client_id":"`+identity+`"}`), nil
				}
				writes++
				if r.URL.Path != "/internal/field-reports" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer agg-internal" || r.Header.Get("X-Client-ID") != "responder" || r.Header.Get("Idempotency-Key") != "report-1" || r.Header.Get("X-Correlation-ID") != "report-trace" {
					t.Error("incorrect internal report request")
				}
				raw, _ := io.ReadAll(r.Body)
				if string(raw) != reportBody {
					t.Error("raw report body changed")
				}
				body, _ := json.Marshal(fieldreport.Receipt{ReportID: fieldreport.ID("responder", "report-1"), Status: "accepted", AcceptedAt: time.Now().UTC()})
				return response(202, string(body)), nil
			})
			result := reportRequest(h, "Bearer user-token", "report-1", reportBody)
			if identity == "responder" {
				if result.Code != 202 || writes != 1 || result.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("valid report rejected: %d %s", result.Code, result.Body.String())
				}
			} else if result.Code != 403 || writes != 0 {
				t.Fatalf("unauthorized write: %d writes=%d", result.Code, writes)
			}
		})
	}
}

func TestFieldReportsRejectInvalidInputBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		token, key, body string
		status           int
	}{
		{"", "key", reportBody, 401},
		{"Bearer user", "", reportBody, 400},
		{"Bearer user", "bad key", reportBody, 400},
		{"Bearer user", "key", `{}`, 400},
		{"Bearer user", "key", reportBody + ` {}`, 400},
		{"Bearer user", "key", strings.Repeat(" ", fieldreport.MaxBodyBytes+1), 413},
	} {
		h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "auth" {
				t.Fatal("invalid request reached storage")
			}
			return response(200, `{"active":true,"client_id":"responder"}`), nil
		})
		if result := reportRequest(h, tc.token, tc.key, tc.body); result.Code != tc.status {
			t.Fatalf("status=%d want=%d body=%s", result.Code, tc.status, result.Body.String())
		}
	}
}

func TestFieldReportsRequireDurableAcceptanceReceipt(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		expected int
	}{
		{503, `{}`, 503}, {409, `{}`, 409}, {200, `{}`, 503},
		{202, `{"report_id":"wrong","status":"accepted","accepted_at":"2026-11-18T01:00:00Z"}`, 503},
		{202, `{`, 503},
	} {
		h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "auth" {
				return response(200, `{"active":true,"client_id":"responder"}`), nil
			}
			return response(tc.status, tc.body), nil
		})
		if result := reportRequest(h, "Bearer user", "key", reportBody); result.Code != tc.expected {
			t.Fatalf("unexpected acceptance: status=%d want=%d", result.Code, tc.expected)
		}
	}
}

func TestFieldReportsRejectExpiredToken(t *testing.T) {
	h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "auth" {
			t.Fatal("expired token reached report storage")
		}
		return response(200, `{"active":false,"client_id":"responder"}`), nil
	})
	if result := reportRequest(h, "Bearer expired", "key", reportBody); result.Code != 401 {
		t.Fatalf("expired token accepted: %d", result.Code)
	}
}
