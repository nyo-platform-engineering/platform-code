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
			result, err := (MockStore{}).Execute(context.Background(), query.Request{Filter: filter, Signal: query.SignalTraces, Operation: query.OperationMetrics, OrganizationScope: "local"})
			if err != nil {
				t.Fatal(err)
			}
			for _, rows := range [][]map[string]any{result.Data, result.Summary} {
				if len(rows) == 0 {
					t.Fatal("no latency samples")
				}
				for _, row := range rows {
					p50, p95, p99 := row["p50Ms"].(float64), row["p95Ms"].(float64), row["p99Ms"].(float64)
					if !(p50 < p95 && p95 < p99) {
						t.Errorf("p50=%v p95=%v p99=%v", p50, p95, p99)
					}
				}
			}
		})
	}
}
