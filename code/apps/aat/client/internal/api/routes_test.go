package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

const hazardPage = `{"items":[{"hazard_id":"haz-1","source":"BMKG","source_ref_id":"eq-1","hazard_type":"SEISMIC","severity":"SIAGA","area_name":"Jawa","latitude":-8,"longitude":110,"occurred_at":"2026-09-30T00:00:00Z","ingested_at":"2026-09-30T00:00:10Z","attributes":{"magnitude":6.5,"future_sensor":{"private":true}},"future_private":"secret"}],"next_after":"haz-1"}`

func handlerFor(t *testing.T, fn roundTrip) http.Handler {
	t.Helper()
	h, err := Handler(Config{AuthURL: "http://auth", AggregatorURL: "http://aggregator", AuthToken: "auth-internal", AggregatorToken: "agg-internal", MaxConcurrent: 1, Timeout: 100 * time.Millisecond, StaleAfter: time.Minute, HTTPClient: &http.Client{Transport: fn}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func invoke(h http.Handler, path, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", bearer)
	r.Header.Set("X-Correlation-ID", "test-correlation")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestFieldAllowlistAndInternalCredentialBoundary(t *testing.T) {
	for _, tc := range []struct {
		id    string
		count int
	}{{"public", 5}, {"responder", 9}, {"analyst", 11}} {
		t.Run(tc.id, func(t *testing.T) {
			h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("X-Correlation-ID") != "test-correlation" {
					t.Error("correlation lost")
				}
				if r.URL.Host == "auth" {
					if r.Header.Get("Authorization") != "Bearer auth-internal" || r.Method != "POST" {
						t.Error("auth credential/method")
					}
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if body["token"] != "user-token" {
						t.Error("wrong introspection token")
					}
					return response(200, `{"active":true,"client_id":"`+tc.id+`"}`), nil
				}
				if r.Header.Get("Authorization") != "Bearer agg-internal" {
					t.Error("client token leaked to aggregator")
				}
				if r.URL.Path == "/internal/hazards" {
					if r.URL.Query().Get("after") != "a&b" || r.URL.Query().Get("limit") != "2" {
						t.Error("query not preserved")
					}
					return response(200, hazardPage), nil
				}
				return response(404, `{}`), nil
			})
			w := invoke(h, "/hazards?source=BMKG&limit=2&after=a%26b", "Bearer user-token")
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var page struct {
				Items   []map[string]any `json:"items"`
				Next    string           `json:"next_after"`
				Sources []SourceStatus   `json:"sources"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			item := page.Items[0]
			if len(item) != tc.count || item["future_private"] != nil || page.Next != "haz-1" {
				t.Fatal(page)
			}
			if tc.id != "analyst" && (item["attributes"] != nil || item["source_ref_id"] != nil) {
				t.Fatal("private data leaked")
			}
			if tc.id == "public" && item["latitude"] != nil {
				t.Fatal("coordinates leaked")
			}
			if len(page.Sources) != 1 || page.Sources[0].Status != "unknown" || !page.Sources[0].Stale {
				t.Fatal("missing poll status treated as fresh")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cache header")
			}
		})
	}
}

func TestInvalidAuthQueriesAndUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name, bearer, query, authBody, data string
		authCode, dataCode, want            int
	}{
		{"missing", "", "", `{"active":true,"client_id":"public"}`, hazardPage, 200, 200, 401},
		{"inactive", "Bearer invalid", "", `{"active":false}`, hazardPage, 200, 200, 401},
		{"unknown_identity", "Bearer token", "", `{"active":true,"client_id":"root"}`, hazardPage, 200, 200, 401},
		{"auth_down", "Bearer token", "", `{}`, hazardPage, 503, 200, 503},
		{"auth_bad_json", "Bearer token", "", `{`, hazardPage, 200, 200, 503},
		{"agg_down", "Bearer token", "", `{"active":true,"client_id":"public"}`, `{}`, 200, 503, 503},
		{"agg_bad_shape", "Bearer token", "", `{"active":true,"client_id":"public"}`, `{"items":[{}]}`, 200, 200, 503},
		{"field_override", "Bearer token", "?fields=attributes", `{"active":true,"client_id":"public"}`, hazardPage, 200, 200, 400},
		{"role_override", "Bearer token", "?role=analyst", `{"active":true,"client_id":"public"}`, hazardPage, 200, 200, 400},
		{"duplicate_limit", "Bearer token", "?limit=1&limit=2", `{"active":true,"client_id":"public"}`, hazardPage, 200, 200, 400},
		{"bad_limit", "Bearer token", "?limit=1001", `{"active":true,"client_id":"public"}`, hazardPage, 200, 200, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
				if tc.bearer == "" {
					t.Error("missing auth reached upstream")
				}
				if r.URL.Host == "auth" {
					return response(tc.authCode, tc.authBody), nil
				}
				return response(tc.dataCode, tc.data), nil
			})
			w := invoke(h, "/hazards"+tc.query, tc.bearer)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestFreshnessUsesPollSuccessNotEventAge(t *testing.T) {
	recent := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	for _, tc := range []struct {
		name, statusBody, want string
		code                   int
		stale                  bool
	}{
		{"recent_poll_old_event", `{"sources":[{"source":"BMKG","healthy":true,"last_success_at":"` + recent + `"}]}`, "fresh", 200, false},
		{"old_poll", `{"sources":[{"source":"BMKG","healthy":true,"last_success_at":"` + old + `"}]}`, "stale", 200, true},
		{"failed_poll", `{"sources":[{"source":"BMKG","healthy":false,"last_success_at":"` + recent + `"}]}`, "stale", 200, true},
		{"never_polled", `{"sources":[{"source":"BMKG","healthy":false,"last_success_at":null}]}`, "unknown", 200, true},
		{"future", `{"sources":[{"source":"BMKG","healthy":true,"last_success_at":"` + future + `"}]}`, "unknown", 200, true},
		{"missing", `{"sources":[]}`, "unknown", 200, true},
		{"unavailable", `{}`, "unknown", 503, true},
		{"bad_timestamp", `{"sources":[{"source":"BMKG","healthy":true,"last_success_at":"bad"}]}`, "unknown", 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "auth" {
					return response(200, `{"active":true,"client_id":"public"}`), nil
				}
				if r.URL.Path == "/internal/hazards" {
					return response(200, hazardPage), nil
				}
				return response(tc.code, tc.statusBody), nil
			})
			w := invoke(h, "/hazards?source=BMKG", "Bearer token")
			var page struct {
				Sources []SourceStatus `json:"sources"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || len(page.Sources) != 1 || page.Sources[0].Status != tc.want || page.Sources[0].Stale != tc.stale {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestConcurrentRequestRejectedAndTimeoutReleasesSlot(t *testing.T) {
	entered, done := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "auth" {
			return response(200, `{"active":true,"client_id":"public"}`), nil
		}
		if r.URL.Path == "/internal/hazards" {
			if calls.Add(1) == 1 {
				close(entered)
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return response(200, hazardPage), nil
		}
		return response(404, `{}`), nil
	})
	go func() {
		defer close(done)
		w := invoke(h, "/hazards", "Bearer token")
		if w.Code != 503 {
			t.Error("timeout should be 503", w.Code)
		}
	}()
	<-entered
	w := invoke(h, "/hazards", "Bearer token")
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Header())
	}
	<-done
	w = invoke(h, "/hazards", "Bearer token")
	if w.Code != 200 {
		t.Fatal("slot not released", w.Code)
	}
}
