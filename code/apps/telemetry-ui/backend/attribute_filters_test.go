package main

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAttributeValidation(t *testing.T) {
	invalid := []string{
		`null`, `{}`, `{"scope":"sql","key":"x","op":"eq"}`,
		`{"scope":"log","key":"","op":"exists"}`,
		`{"scope":"log","key":"x","op":"like","value":"%"}`,
		`{"scope":"log","key":"x","op":"gt","value":"NaN"}`,
		`{"scope":"log","key":"x","op":"gte","value":"Inf"}`,
		`{"scope":"log","key":"x","op":"contains"}`,
		`{"scope":"log","key":"x","op":"exists","value":"ignored"}`,
		`{"scope":"log","key":"x","op":"exists","extra":1}`,
		`{"scope":"log","key":"x","op":"exists"} {}`,
	}
	for _, value := range invalid {
		if _, err := parseAttributes([]string{value}); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	values := make([]string, 9)
	if _, err := parseAttributes(values); err == nil {
		t.Fatal("accepted too many conditions")
	}
}
func TestAttributeSQLBindsKeysAndValues(t *testing.T) {
	key := "x'] OR 1=1 --"
	value := "quote%'"
	for _, op := range []string{"eq", "neq", "contains", "exists", "missing", "gte"} {
		f := attributeFilter{Scope: "resource", Key: key, Op: op, Value: value}
		if op == "gte" {
			f.Value = "500"
		}
		sql, args := f.sql()
		if strings.Contains(sql, key) || strings.Contains(sql, value) || args[0] != key {
			t.Fatalf("unsafe SQL: %s %#v", sql, args)
		}
		if op != "missing" && !strings.Contains(sql, "mapContains") {
			t.Fatal("missing keys must not match values")
		}
	}
}
func TestAttributeScopeMismatchRejected(t *testing.T) {
	raw, _ := json.Marshal(attributeFilter{Scope: "span", Key: "code", Op: "eq", Value: "500"})
	out := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(out)
	c.Request = httptest.NewRequest("GET", "/?attr="+url.QueryEscape(string(raw)), nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalKey, principal{Tenant: "local"}))
	store := &captureStore{}
	analyticsHandler(store, "logs", make(chan struct{}, 1))(c)
	if out.Code != 400 || len(store.queries) != 0 {
		t.Fatal("wrong signal scope must fail before query")
	}
}

func TestJSONPathValidation(t *testing.T) {
	for _, path := range []string{"a..b", ".a", "a.", "items.1000001", "a.b.c.d.e.f.g.h.i"} {
		if _, err := jsonPath(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	for _, scope := range []string{"body", "log"} {
		f := attributeFilter{Scope: scope, Key: "user.name", Op: "eq", Value: "Ada"}
		if scope == "log" {
			f.Key = "payload"
			f.Path = "user.name"
		}
		raw, _ := json.Marshal(f)
		if _, err := parseAttributes([]string{string(raw)}); err != nil {
			t.Fatal(err)
		}
		sql, args := f.sql()
		if strings.Count(sql, "?") != len(args) {
			t.Fatalf("placeholder mismatch: %s %#v", sql, args)
		}
	}
}
