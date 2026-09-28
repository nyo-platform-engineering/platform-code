package query

import (
	"strings"
	"testing"
	"time"
)

func TestBuilderBindsHostileValuesAndKeepsClauseOrder(t *testing.T) {
	attack := "x' OR 1=1; -- ? \\"
	f := Filter{From: time.Now().Add(-time.Minute), To: time.Now(), Search: attack, Services: []string{attack}, Attributes: []AttributeFilter{{Scope: "resource", Key: attack, Op: "eq", Value: attack}}}
	for _, table := range []Table{Traces, Logs} {
		b, err := selectQuery(table, "TraceId", f, attack, false)
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := b.containsKey(attack).orderBy("TraceId").page(101, 2).compile("test")
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
		if !strings.HasSuffix(compiled.SQL, SearchSettings) {
			t.Fatal("search budget lost")
		}
		if compiled.Args[len(compiled.Args)-2] != 101 || compiled.Args[len(compiled.Args)-1] != 2 {
			t.Fatal("pagination not bound")
		}
	}
}

func TestBuilderRejectsUnknownTableAndInvalidPagination(t *testing.T) {
	if _, err := selectQuery(Table(99), "TraceId", Filter{}, "tenant", false); err == nil {
		t.Fatal("unknown table accepted")
	}
	for _, page := range [][2]int{{0, 0}, {502, 0}, {10, -1}, {10, 5001}} {
		b, err := selectQuery(Traces, "TraceId", Filter{}, "tenant", false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.page(page[0], page[1]).compile("test"); err == nil {
			t.Fatal("invalid pagination accepted")
		}
	}
	var zero selectBuilder
	if _, err := zero.compile("test"); err == nil {
		t.Fatal("zero builder bypassed tenant requirement")
	}
}
