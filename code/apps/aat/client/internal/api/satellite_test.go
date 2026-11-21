package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestSatelliteDashboardUsesAnalystBoundaryAndInternalCredential(t *testing.T) {
	for _, tc := range []struct {
		role   string
		active bool
		want   int
	}{{"analyst", true, 200}, {"public", true, 403}, {"responder", true, 403}, {"analyst", false, 401}} {
		t.Run(tc.role+http.StatusText(tc.want), func(t *testing.T) {
			calls := 0
			h := handlerFor(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "auth" {
					active := "false"
					if tc.active {
						active = "true"
					}
					return response(200, `{"active":`+active+`,"client_id":"`+tc.role+`"}`), nil
				}
				calls++
				if r.URL.Path != "/internal/satellite/stats" || r.Header.Get("Authorization") != "Bearer agg-internal" {
					t.Errorf("incorrect forwarding: %s %s", r.URL.Path, r.Header.Get("Authorization"))
				}
				return response(200, `{"late_detections":7,"feed":{"offset":10,"latest_offset":25,"lag_events":15,"rejected_events":0}}`), nil
			})
			w := invoke(h, "/satellite/stats", "Bearer user-token")
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.want == 200 {
				if calls != 1 || !strings.Contains(w.Body.String(), `"lag_events":15`) {
					t.Fatal("dashboard lost live lag")
				}
			} else if calls != 0 {
				t.Fatal("unauthorized role reached satellite stats")
			}
			if strings.Contains(w.Body.String(), "agg-internal") {
				t.Fatal("internal credential leaked")
			}
		})
	}
}
func TestSatelliteDashboardRejectsMissingToken(t *testing.T) {
	h := handlerFor(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("missing token reached upstream")
		return nil, nil
	})
	if w := invoke(h, "/satellite/stats", ""); w.Code != 401 {
		t.Fatalf("got %d", w.Code)
	}
}
