package internal

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aat/internal/httpkit"
	"github.com/gin-gonic/gin"
)

func TestReadAdmissionReservesCapacityAndReleasesSlot(t *testing.T) {
	router := httpkit.Router()
	gate := limitReads(1)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	private := router.Group("", httpkit.Auth("Authorization", "Bearer test"))
	private.GET("/hold", gate, func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusNoContent)
	})
	private.GET("/read", gate, func(c *gin.Context) { c.Status(http.StatusBadRequest) })
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	go func() { request("/hold", "Bearer test"); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	if w := request("/read", "Bearer test"); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("busy read: %d %v", w.Code, w.Header())
	}
	if w := request("/read", "Bearer wrong"); w.Code != 401 {
		t.Fatalf("authentication bypassed: %d", w.Code)
	}
	if w := request("/health", ""); w.Code != 200 {
		t.Fatalf("health blocked: %d", w.Code)
	}
	release <- struct{}{}
	<-done
	for range 2 {
		if w := request("/read", "Bearer test"); w.Code != 400 {
			t.Fatalf("slot leaked after handler error: %d", w.Code)
		}
	}
}
