package httpkit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Public routes bypass auth; only a valid token may execute the protected handler.
func TestGinAuth(t *testing.T) {
	router := Router()
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	calls := 0
	private := router.Group("/internal", Auth("Authorization", "Bearer test-only"))
	private.GET("/data", func(c *gin.Context) {
		calls++
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	for _, tc := range []struct {
		name, path, token string
		status            int
	}{
		{"public_health", "/health", "", 200},
		{"missing_token", "/internal/data", "", 401},
		{"wrong_token", "/internal/data", "Bearer wrong", 401},
		{"valid_token", "/internal/data", "Bearer test-only", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			r := httptest.NewRequest("GET", tc.path, nil)
			r.Header.Set("Authorization", tc.token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%s: got %d, want %d", tc.path, w.Code, tc.status)
			}
			wantCalls := 0
			if tc.name == "valid_token" {
				wantCalls = 1
			}
			if calls-before != wantCalls {
				t.Fatalf("protected handler called %d times, want %d", calls-before, wantCalls)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("protected handler called %d times", calls)
	}
}
