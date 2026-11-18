package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOnlyInternalOpsCanInspectAndReplayMessages(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, identity := range []string{"public", "responder", "analyst"} {
		for _, operation := range []struct{ method, path string }{
			{"GET", "/ops/field-reports/fr-" + id},
			{"GET", "/ops/unprocessed?source=PVMBG&limit=1"},
			{"GET", "/ops/unprocessed/" + id},
			{"POST", "/ops/unprocessed/" + id + "/replay"},
		} {
			t.Run(identity+operation.method+operation.path, func(t *testing.T) {
				calls := 0
				handler := handlerFor(t, func(r *http.Request) (*http.Response, error) {
					if r.URL.Host == "auth" {
						return response(200, `{"active":true,"client_id":"`+identity+`"}`), nil
					}
					calls++
					if r.Header.Get("Authorization") != "Bearer agg-internal" || r.Method != operation.method || r.URL.Path != "/internal"+strings.Split(operation.path, "?")[0] {
						t.Error("ops proxy changed credentials or operation")
					}
					return response(200, `{"items":[],"next_cursor":null}`), nil
				})
				request := httptest.NewRequest(operation.method, operation.path, nil)
				request.Header.Set("Authorization", "Bearer user-token")
				result := httptest.NewRecorder()
				handler.ServeHTTP(result, request)
				if identity == "analyst" {
					if result.Code != 200 || calls != 1 {
						t.Fatalf("ops denied: %d calls=%d", result.Code, calls)
					}
				} else if result.Code != 403 || calls != 0 {
					t.Fatalf("raw/replay access leaked: %d calls=%d", result.Code, calls)
				}
			})
		}
	}
}

func TestOpsRejectsInactiveAndMissingTokens(t *testing.T) {
	handler := handlerFor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "auth" {
			t.Fatal("unauthorized request reached storage")
		}
		return response(200, `{"active":false,"client_id":"analyst"}`), nil
	})
	for _, token := range []string{"", "Bearer expired"} {
		if result := invoke(handler, "/ops/unprocessed", token); result.Code != 401 {
			t.Fatalf("inactive token accepted: %d", result.Code)
		}
	}
}
