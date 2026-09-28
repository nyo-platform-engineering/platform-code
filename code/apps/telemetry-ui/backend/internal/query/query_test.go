package query_test

import (
	"testing"

	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestCompilersRejectUnscopedOrInvalidQueries(t *testing.T) {
	for _, tc := range []struct {
		kind, signal string
		compile      query.Compiler
	}{
		{"traces", "span", traces.CompileQueries}, {"logs", "log", logs.CompileQueries}, {"services", "", metadata.CompileQueries},
	} {
		f := query.Filter{Limit: 100}
		if _, err := tc.compile(query.Request{Filter: f, Kind: tc.kind, Tenant: ""}); err == nil {
			t.Fatalf("%s accepted empty tenant", tc.kind)
		}
		if _, err := tc.compile(query.Request{Filter: f, Kind: "injected; DROP TABLE otel.otel_logs", Tenant: "tenant"}); err == nil {
			t.Fatalf("%s accepted invalid kind", tc.kind)
		}
		if tc.signal == "" {
			continue
		}
		for _, attr := range []query.AttributeFilter{
			{Scope: "resource); DROP TABLE x;--", Key: "x", Op: "exists"},
			{Scope: tc.signal, Key: "x", Op: "= 1 OR 1=1"},
			{Scope: tc.signal, Key: "payload", Path: "a..b", Op: "exists"},
		} {
			f.Attributes = []query.AttributeFilter{attr}
			if _, err := tc.compile(query.Request{Filter: f, Kind: tc.kind, Tenant: "tenant"}); err == nil {
				t.Fatalf("accepted unsafe attribute: %#v", attr)
			}
		}
		f.Attributes = nil
		f.DiscoveryScope = "Body) FROM secret --"
		if _, err := tc.compile(query.Request{Filter: f, Kind: tc.kind + "-keys", Tenant: "tenant"}); err == nil {
			t.Fatal("accepted arbitrary discovery identifier")
		}
	}
}
