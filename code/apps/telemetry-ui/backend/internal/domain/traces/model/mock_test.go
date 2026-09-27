package model

import (
	"context"
	"testing"
	"time"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestMockServiceLatencyHasDistinctPercentiles(t *testing.T) {
	end := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, service := range []string{"api-gateway", "checkout", "payments"} {
		t.Run(service, func(t *testing.T) {
			filter := query.Filter{From: end.Add(-5 * time.Minute), To: end, Services: []string{service}}
			plans, _, err := CompileQueries(filter, "red", "local")
			if err != nil {
				t.Fatal(err)
			}
			for _, plan := range plans {
				rows, err := query.Execute(context.Background(), MockStore{}, plan)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) == 0 {
					t.Fatal("no latency samples")
				}
				for _, row := range rows {
					p50, p95, p99 := row["p50Ms"].(float64), row["p95Ms"].(float64), row["p99Ms"].(float64)
					if !(p50 < p95 && p95 < p99) {
						t.Errorf("%s: p50=%v p95=%v p99=%v", plan.Name, p50, p95, p99)
					}
				}
			}
		})
	}
}
