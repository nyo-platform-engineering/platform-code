package query

import (
	"strings"
	"testing"
	"time"
)

func TestWhereAlwaysScopesTenantAndBindsValues(t *testing.T) {
	from := time.Now()
	f := Filter{From: from, To: from.Add(time.Minute), Services: []string{"x' OR 1=1 --"}, Environment: "local", TraceID: strings.Repeat("a", 32), Severities: []string{"error"}}
	for _, logs := range []bool{false, true} {
		sql, args := f.Where("tenant-a", logs)
		if !strings.Contains(sql, "ResourceAttributes['tenant.id'] = ?") || strings.Contains(sql, f.Services[0]) || args[2] != "tenant-a" {
			t.Fatalf("unsafe query: %s %#v", sql, args)
		}
	}
}
