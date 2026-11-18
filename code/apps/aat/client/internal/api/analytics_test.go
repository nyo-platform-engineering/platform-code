package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aat/internal/analytics"
)

func analyticsFixture(q analytics.Query) analytics.Result {
	total := int64(1)
	return analytics.Result{Query: q, AsOf: q.To, Total: &total, ByType: []analytics.Count{{Key: "SEISMIC", Count: 1}, {Key: "TSUNAMI"}, {Key: "VOLCANIC"}, {Key: "WILDFIRE"}, {Key: "COASTAL"}}, BySeverity: []analytics.Count{{Key: "NORMAL", Count: 1}, {Key: "WASPADA"}, {Key: "SIAGA"}, {Key: "AWAS"}}, ByArea: []analytics.Count{{Key: "Jawa", Count: 1}}, Timeline: []analytics.Bucket{{At: q.From, Counts: map[string]int64{"SEISMIC": 1, "TSUNAMI": 0, "VOLCANIC": 0, "WILDFIRE": 0, "COASTAL": 0}}}, UnavailableUnits: []string{}}
}
func TestAnalyticsPermissionBoundaryAndPartial(t *testing.T) {
	for _, tc := range []struct {
		id, mode string
		want     int
	}{{"public", "", 403}, {"responder", "", 403}, {"analyst", "", 200}, {"analyst", "partial", 200}, {"analyst", "corrupt", 503}} {
		t.Run(tc.id+tc.mode, func(t *testing.T) {
			h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "auth" {
					return response(200, `{"active":true,"client_id":"`+tc.id+`"}`), nil
				}
				if tc.id != "analyst" {
					t.Error("non-analyst reached aggregate data")
				}
				if r.URL.Path == "/internal/source-status" {
					return response(200, `{"sources":[]}`), nil
				}
				if r.Header.Get("Authorization") != "Bearer agg-internal" {
					t.Error("browser token crossed internal boundary")
				}
				q, err := analytics.Parse(r.URL.Query(), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				result := analyticsFixture(q)
				if tc.mode == "partial" {
					result = analytics.Result{Query: q, AsOf: q.To, Partial: true, UnavailableUnits: []string{"worker-offline"}}
				}
				if tc.mode == "corrupt" {
					*result.Total = 999
				}
				raw, _ := json.Marshal(result)
				return response(200, string(raw)), nil
			})
			w := invoke(h, "/analytics?from=2026-11-18T00:00:00Z&to=2026-11-18T01:00:00Z", "Bearer user-token")
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.mode == "partial" && (!strings.Contains(w.Body.String(), `"partial":true`) || !strings.Contains(w.Body.String(), `"total":null`)) {
				t.Fatal("partial was presented as a complete number")
			}
		})
	}
}

func TestDashboardSessionAndStaticBoundaries(t *testing.T) {
	h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("internal credential must not be attached to login")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path == "/auth/token" && (body["client_id"] != "analyst" || body["client_secret"] != "browser-secret") {
			t.Fatal("incorrect login forwarding")
		}
		if r.URL.Path == "/auth/refresh" && body["refresh_token"] != "refresh" {
			t.Fatal("incorrect refresh forwarding")
		}
		return response(200, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":60,"refresh_expires_in":3600}`), nil
	})
	for path, body := range map[string]string{"/session": `{"client_secret":"browser-secret"}`, "/session/refresh": `{"refresh_token":"refresh"}`} {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("session: %d %s", w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("POST", "/session", strings.NewReader(`{"client_id":"public","client_secret":"secret"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("client role override accepted")
	}
	for _, path := range []string{"/", "/dashboard.js", "/dashboard.css"} {
		w := invoke(h, path, "")
		if w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "agg-internal") || strings.Contains(w.Body.String(), "auth-internal") {
			t.Fatal("service credentials embedded in assets")
		}
	}
}

func TestAnalyticsRejectsMalformedFilters(t *testing.T) {
	for _, query := range []string{"source=BMKG;unexpected=value", "interval=second", "source=OTHER", "from=2026-11-18T00:00:00Z&from=2026-11-17T00:00:00Z"} {
		h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "auth" {
				t.Error("invalid filters reached Aggregator")
			}
			return response(200, `{"active":true,"client_id":"analyst"}`), nil
		})
		w := invoke(h, "/analytics?"+query, "Bearer user-token")
		if w.Code != 400 {
			t.Fatalf("query %s: %d %s", query, w.Code, w.Body.String())
		}
	}
}
