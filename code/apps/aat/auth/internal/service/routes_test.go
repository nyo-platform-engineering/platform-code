package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPContract(t *testing.T) {
	s, _ := testStore()
	h := Handler(s, "internal-only", 2)
	request := func(path, auth, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s got %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing cache control")
		}
		return w
	}
	request("/auth/token", "", `{"client_id":"public","client_secret":"wrong"}`, 401)
	request("/auth/token", "", `{"client_id":"public","client_secret":"public-secret","role":"analyst"}`, 400)
	request("/auth/token", "", `{}`, 401)
	request("/auth/token", "", `{} {}`, 400)
	request("/auth/token", "", `{"client_secret":"`+strings.Repeat("a", 17000)+`"}`, 400)
	w := request("/auth/token", "", `{"client_id":"public","client_secret":"public-secret"}`, 200)
	var pair Pair
	if err := json.Unmarshal(w.Body.Bytes(), &pair); err != nil {
		t.Fatal(err)
	}
	request("/internal/introspect", "Bearer "+pair.AccessToken, `{"token":"`+pair.AccessToken+`"}`, 401)
	w = request("/internal/introspect", "Bearer internal-only", `{"token":"`+pair.AccessToken+`"}`, 200)
	if !strings.Contains(w.Body.String(), `"client_id":"public"`) {
		t.Fatal(w.Body.String())
	}
	request("/auth/refresh", "", `{"refresh_token":"`+pair.AccessToken+`"}`, 401)
	request("/auth/refresh", "", `{"refresh_token":"`+pair.RefreshToken+`"}`, 200)
}
