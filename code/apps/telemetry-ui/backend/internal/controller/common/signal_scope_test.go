package common

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestAttributeScopeMismatchRejected(t *testing.T) {
	raw, _ := json.Marshal(model.AttributeFilter{Scope: "span", Key: "code", Op: "eq", Value: "500"})
	out := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(out)
	c.Request = httptest.NewRequest("GET", "/?attr="+url.QueryEscape(string(raw)), nil)
	c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{Tenant: "local"}))
	store := &captureStore{}
	analyticsHandler(store, "logs", make(chan struct{}, 1))(c)
	if out.Code != 400 || len(store.queries) != 0 {
		t.Fatal("wrong signal scope must fail before query")
	}
}
