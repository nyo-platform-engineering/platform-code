package query_test

import (
	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	"strings"
	"testing"
	"time"
)

func TestCompilersRejectUnscopedOrInvalidQueries(t *testing.T) {
	for _, tc := range []struct {
		kind, signal string
		compile      func(query.Filter, string, string) ([]query.CompiledQuery, bool, error)
	}{
		{"traces", "span", traces.CompileQueries}, {"logs", "log", logs.CompileQueries}, {"services", "", metadata.CompileQueries},
	} {
		f := query.Filter{Limit: 100}
		if _, _, err := tc.compile(f, tc.kind, ""); err == nil {
			t.Fatalf("%s accepted empty tenant", tc.kind)
		}
		if _, _, err := tc.compile(f, "injected; DROP TABLE otel.otel_logs", "tenant"); err == nil {
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
			if _, _, err := tc.compile(f, tc.kind, "tenant"); err == nil {
				t.Fatalf("accepted unsafe attribute: %#v", attr)
			}
		}
		f.Attributes = nil
		f.DiscoveryScope = "Body) FROM secret --"
		if _, _, err := tc.compile(f, tc.kind+"-keys", "tenant"); err == nil {
			t.Fatal("accepted arbitrary discovery identifier")
		}
	}
}

func TestBuilderBindsHostileValuesAndKeepsClauseOrder(t *testing.T) {
	attack := "x' OR 1=1; -- ? \\"
	f := query.Filter{From: time.Now().Add(-time.Minute), To: time.Now(), Search: attack, Services: []string{attack}, Attributes: []query.AttributeFilter{{Scope: "resource", Key: attack, Op: "eq", Value: attack}}}
	for _, table := range []query.Table{query.Traces, query.Logs} {
		b, err := query.Select(table, "TraceId", f, attack, false)
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := b.ContainsKey(attack).OrderBy("TraceId").Page(101, 2).Compile("test")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(compiled.SQL, attack) {
			t.Fatal("request value interpolated into SQL")
		}
		if strings.Count(compiled.SQL, "?") != len(compiled.Args) {
			t.Fatalf("placeholder mismatch: %s %#v", compiled.SQL, compiled.Args)
		}
		if strings.Count(compiled.SQL, " WHERE ") != 1 || !strings.Contains(compiled.SQL, " AND positionCaseInsensitiveUTF8(key, ?) > 0") {
			t.Fatal("predicates did not compose into one WHERE clause")
		}
		if compiled.Args[2] != attack {
			t.Fatal("tenant binding lost")
		}
		if !strings.HasSuffix(compiled.SQL, query.SearchSettings) {
			t.Fatal("search budget lost")
		}
		if compiled.Args[len(compiled.Args)-2] != 101 || compiled.Args[len(compiled.Args)-1] != 2 {
			t.Fatal("pagination not bound")
		}
	}
}

func TestBuilderRejectsUnknownTableAndInvalidPagination(t *testing.T) {
	if _, err := query.Select(query.Table(99), "TraceId", query.Filter{}, "tenant", false); err == nil {
		t.Fatal("unknown table accepted")
	}
	for _, page := range [][2]int{{0, 0}, {502, 0}, {10, -1}, {10, 5001}} {
		b, err := query.Select(query.Traces, "TraceId", query.Filter{}, "tenant", false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.Page(page[0], page[1]).Compile("test"); err == nil {
			t.Fatal("invalid pagination accepted")
		}
	}
	var zero query.SelectBuilder
	if _, err := zero.Compile("test"); err == nil {
		t.Fatal("zero builder bypassed tenant requirement")
	}
}
