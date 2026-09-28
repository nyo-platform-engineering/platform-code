package queryinternal

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Changing request strings must change arguments, never SQL structure. Cover
// list, aggregation, discovery, detail and each service backend through the
// public compiler, including map keys and JSON paths containing SQL syntax.
func TestCompileBindsRequestValues(t *testing.T) {
	for _, kind := range []string{"traces", "detail", "red", "traces-keys", "logs", "logs-volume", "logs-keys", "services", "trace-services", "log-services"} {
		t.Run(kind, func(t *testing.T) {
			end := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
			request := func(value string) Request {
				return Request{Kind: kind, Tenant: value, Filter: Filter{
					From: end.Add(-time.Hour), To: end, Limit: 2, Offset: 4,
					Services: []string{value}, Environment: value, TraceID: value,
					Search: value, KeySearch: value, DiscoveryScope: "resource",
					Attributes: []AttributeFilter{
						{Scope: "resource", Key: value, Op: "eq", Value: value},
						{Scope: "resource", Key: value, Path: value, Op: "contains", Value: value},
					},
				}}
			}
			safe, err := Compile(request("ordinary"))
			if err != nil {
				t.Fatal(err)
			}
			for _, attack := range []string{"x' OR 1=1 --", "x'); DROP TABLE otel_traces; --", "x?/* injected */\\"} {
				hostile, err := Compile(request(attack))
				if err != nil {
					t.Fatal(err)
				}
				if len(hostile) != len(safe) {
					t.Fatal("request changed query count")
				}
				for i, q := range hostile {
					if q.SQL != safe[i].SQL || strings.Contains(q.SQL, attack) {
						t.Fatalf("request changed SQL structure: %s", q.SQL)
					}
					if strings.Count(q.SQL, "?") != len(q.Args) {
						t.Fatalf("placeholder mismatch: %s %#v", q.SQL, q.Args)
					}
					if reflect.DeepEqual(q.Args, safe[i].Args) || q.Args[2] != attack {
						t.Fatal("request values not bound")
					}
				}
			}
		})
	}
}

func TestCompileRejectsSQLAsIdentifiers(t *testing.T) {
	for _, request := range []Request{
		{Kind: "logs; DROP TABLE x", Tenant: "local"},
		{Kind: "logs-keys", Tenant: "local", Filter: Filter{DiscoveryScope: "LogAttributes)) FROM x --"}},
		{Kind: "logs", Tenant: "local", Filter: Filter{Attributes: []AttributeFilter{{Scope: "log", Key: "k", Op: "= 1 OR 1=1"}}}},
		{Kind: "logs", Tenant: "local", Filter: Filter{Attributes: []AttributeFilter{{Scope: "LogAttributes; --", Key: "k", Op: "eq"}}}},
	} {
		if _, err := Compile(request); err == nil {
			t.Fatalf("accepted SQL identifier: %+v", request)
		}
	}
}
