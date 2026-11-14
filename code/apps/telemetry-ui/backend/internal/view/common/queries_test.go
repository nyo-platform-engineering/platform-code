package common

import (
	"testing"
	"time"

	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestGapFillingKeepsMissingLatencyNull(t *testing.T) {
	from := time.Date(2026, 9, 25, 1, 0, 30, 0, time.UTC)
	f := model.Filter{From: from, To: from.Add(2 * time.Minute)}
	rows := FillBuckets(nil, f, false)
	if len(rows) != 3 || rows[0]["partial"] != true || rows[1]["partial"] != false || rows[2]["partial"] != true || rows[1]["p90Ms"] != nil || rows[1]["p95Ms"] != nil || rows[1]["requests"] != 0 {
		t.Fatalf("unexpected buckets: %#v", rows)
	}
}
