package main

import (
	"context"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttributeDiscoveryValidationAndBudget(t *testing.T) {
	for _, kind := range []string{"logs-keys", "traces-keys"} {
		for _, raw := range []string{"scope=body", "scope=sql", "keySearch=" + strings.Repeat("x", 129), "keySearch=x%0Ay", "scope=resource&keySearch=needle"} {
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("GET", "/?"+raw, nil)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalKey, principal{Tenant: "isolated"}))
			store := &captureStore{}
			analyticsHandler(store, kind, make(chan struct{}, 1))(c)
			if strings.Contains(raw, "needle") {
				if out.Code != 200 || len(store.queries) != 1 || !strings.Contains(store.queries[0], searchSettings) || strings.Contains(store.queries[0], "needle") || store.args[0][2] != "isolated" {
					t.Fatalf("unsafe discovery: %s %#v", out.Body, store)
				}
			} else if out.Code != 400 || len(store.queries) != 0 {
				t.Fatal("invalid discovery reached database")
			}
		}
	}
}
