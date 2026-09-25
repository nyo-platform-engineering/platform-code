package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPreviewMatchesExecutionWithoutQueryingStore(t *testing.T) {
	for _, kind := range []string{"logs", "logs-volume", "traces", "red", "detail", "services"} {
		params := url.Values{"from": {"2026-09-25T01:00:00.123456789Z"}, "to": {"2026-09-25T02:00:00.123456789Z"}, "q": {"needle"}, "service": {"go-demo"}, "offset": {"100"}}
		scope := "span"
		if strings.HasPrefix(kind, "logs") {
			scope = "log"
		}
		raw, _ := json.Marshal(attributeFilter{Scope: scope, Key: "payload", Path: "user.id", Op: "eq", Value: "a' OR 1=1 --"})
		params.Set("attr", string(raw))
		run := func(preview, auth bool) (*httptest.ResponseRecorder, *captureStore) {
			params.Del("preview")
			if preview {
				params.Set("preview", "1")
			}
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("GET", "/?"+params.Encode(), nil)
			if auth {
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalKey, principal{Tenant: "test-tenant"}))
			}
			store := &captureStore{}
			analyticsHandler(store, kind, make(chan struct{}, 1))(c)
			return out, store
		}
		denied, store := run(true, false)
		if denied.Code != 403 || len(store.queries) != 0 {
			t.Fatal("preview bypassed tenant authorization")
		}
		executed, store := run(false, true)
		preview, unused := run(true, true)
		if executed.Code != 200 || preview.Code != 200 || len(unused.queries) != 0 {
			t.Fatalf("%s execution=%s preview=%s", kind, executed.Body, preview.Body)
		}
		var body struct {
			Queries []struct {
				SQL        string
				Parameters []struct {
					Position    int
					Type, Value string
				}
			}
		}
		if err := json.Unmarshal(preview.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Queries) != len(store.queries) {
			t.Fatal("missing preview query")
		}
		for i, q := range body.Queries {
			if q.SQL != store.queries[i] || len(q.Parameters) != len(store.args[i]) {
				t.Fatal("preview differs from execution")
			}
			if !strings.Contains(q.SQL, "PREWHERE") {
				t.Fatal("missing early filters")
			}
			for j, param := range q.Parameters {
				if param.Position != j+1 || param.Value != fmt.Sprint(store.args[i][j]) {
					t.Fatal("parameter precision or ordering lost")
				}
			}
			if kind != "services" {
				pieces := strings.SplitN(q.SQL, " WHERE ", 2)
				if len(pieces) != 2 || strings.Contains(pieces[0], "JSONExtract") || !strings.Contains(pieces[1], "JSONExtract") {
					t.Fatal("JSON must remain after PREWHERE")
				}
			}
		}
	}
}
