package common

import (
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
)

func TestAttributeDiscoveryValidationAndBudget(t *testing.T) {
	for _, signal := range []model.Signal{model.SignalLogs, model.SignalTraces} {
		for _, raw := range []string{"scope=body", "scope=sql", "keySearch=" + strings.Repeat("x", 129), "keySearch=x%0Ay", "scope=resource&keySearch=needle"} {
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("GET", "/?"+raw, nil)
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{OrganizationScope: "isolated"}))
			store := &captureStore{}
			analyticsHandler(store, signal, model.OperationAttributes, make(chan struct{}, 1))(c)
			if strings.Contains(raw, "needle") {
				if out.Code != 200 || len(store.queries) != 1 || !strings.Contains(store.queries[0], model.SearchSettings) || strings.Contains(store.queries[0], "needle") || store.args[0][2] != "isolated" {
					t.Fatalf("unsafe discovery: %s %#v", out.Body, store)
				}
			} else if out.Code != 400 || len(store.queries) != 0 {
				t.Fatal("invalid discovery reached database")
			}
		}
	}
}
