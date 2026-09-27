package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
)

type fixedAuthenticator struct{ actor auth.Principal }

func (a fixedAuthenticator) Authenticate(*http.Request) auth.Principal { return a.actor }

type auditStore struct {
	calls   int
	tenants []string
}

func (s *auditStore) Query(_ context.Context, _ string, args ...any) ([]map[string]any, error) {
	s.calls++
	if len(args) > 2 {
		s.tenants = append(s.tenants, args[2].(string))
	}
	return []map[string]any{}, nil
}
func auditHandler(t *testing.T, actor auth.Principal, store *auditStore) http.Handler {
	t.Helper()
	h, err := newHandler(config{WebDistDir: t.TempDir(), TraceStore: store, LogStore: store, Authenticator: fixedAuthenticator{actor}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Independent expectations catch permission mixups in the production policy table.
func TestAccessMatrix(t *testing.T) {
	routes := []struct{ path, permission string }{
		{"/meta", auth.MetadataRead}, {"/services", auth.MetadataRead},
		{"/traces", auth.TracesRead}, {"/traces/" + strings.Repeat("a", 32), auth.TracesRead},
		{"/traces/red", auth.TracesRead}, {"/traces/attributes", auth.TracesRead},
		{"/logs", auth.LogsRead}, {"/logs/volume", auth.LogsRead}, {"/logs/attributes", auth.LogsRead},
	}
	for _, route := range routes {
		for _, permission := range []string{"", auth.MetadataRead, auth.TracesRead, auth.LogsRead} {
			for _, preview := range []string{"", "?preview=1"} {
				t.Run(route.path+"/"+permission+preview, func(t *testing.T) {
					store := &auditStore{}
					h := auditHandler(t, auth.Principal{Subject: "reader", Tenant: "tenant-a", Permissions: []string{permission}}, store)
					req := httptest.NewRequest("GET", "/api/v1"+route.path+preview, nil)
					req.Header.Set("X-Tenant-ID", "tenant-b")
					out := httptest.NewRecorder()
					h.ServeHTTP(out, req)
					expected := http.StatusForbidden
					if permission == route.permission {
						expected = http.StatusOK
					}
					if out.Code != expected {
						t.Fatalf("want %d, got %d: %s", expected, out.Code, out.Body)
					}
					if (expected == http.StatusForbidden || preview != "") && store.calls != 0 {
						t.Fatal("denial/preview reached storage")
					}
					for _, tenant := range store.tenants {
						if tenant != "tenant-a" {
							t.Fatalf("untrusted tenant: %q", tenant)
						}
					}
					if expected == http.StatusOK && preview != "" && route.path != "/meta" && !strings.Contains(out.Body.String(), "tenant-a") {
						t.Fatal("preview missing trusted tenant")
					}
				})
			}
		}
	}
}

func TestMissingIdentityOrTenantDenied(t *testing.T) {
	for _, actor := range []auth.Principal{
		{Subject: "reader", Permissions: []string{auth.MetadataRead, auth.TracesRead, auth.LogsRead}},
		{Tenant: "tenant-a", Permissions: []string{auth.MetadataRead, auth.TracesRead, auth.LogsRead}},
	} {
		store := &auditStore{}
		h := auditHandler(t, actor, store)
		for _, path := range []string{"/api/v1/meta", "/api/v1/services", "/api/v1/traces?preview=1", "/api/v1/logs"} {
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
			if out.Code != 403 {
				t.Fatalf("%s: got %d", path, out.Code)
			}
		}
		if store.calls != 0 {
			t.Fatal("denied request reached storage")
		}
	}
}

func TestPublicAndFallbackAccess(t *testing.T) {
	h := auditHandler(t, auth.Principal{}, &auditStore{})
	for _, path := range []string{"/healthz", "/readyz"} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != 200 {
			t.Fatalf("public %s: %d", path, out.Code)
		}
	}
	for _, request := range []struct{ method, path string }{
		{"GET", "/api"}, {"GET", "/api/v1/unknown"}, {"GET", "/otlp"}, {"POST", "/otlp/v1/traces"},
		{"POST", "/api/v1/logs"}, {"HEAD", "/api/v1/logs"}, {"POST", "/traces"},
	} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest(request.method, request.path, nil))
		if out.Code != 404 {
			t.Fatalf("%s %s: %d", request.method, request.path, out.Code)
		}
	}
}

func TestUnsupportedAuthModeFailsClosed(t *testing.T) {
	if _, err := newHandler(config{AuthMode: "oidc"}, slog.Default()); err == nil {
		t.Fatal("unsupported mode accepted")
	}
}

func TestFrontendIsPublic(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{"index.html": "frontend", "assets/app.js": "asset"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := newHandler(config{WebDistDir: root, TraceStore: &auditStore{}, LogStore: &auditStore{}, Authenticator: fixedAuthenticator{}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/assets/app.js", "/traces"} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != 200 {
			t.Fatalf("public frontend %s: %d", path, out.Code)
		}
	}
}
