package mock

import (
	"math"
	"testing"
)

func TestSummaryInterpolatesTailPercentiles(t *testing.T) {
	result := summary([]Record{{DurationMs: 1000}, {DurationMs: 10}, {DurationMs: 30}, {DurationMs: 20}})
	for key, expected := range map[string]float64{"p50Ms": 25, "p90Ms": 709, "p95Ms": 854.5, "p99Ms": 970.9} {
		if actual := result[key].(float64); math.Abs(actual-expected) > 0.000001 {
			t.Errorf("%s = %v, want %v", key, actual, expected)
		}
	}
}

func TestSummaryEmptyAndSingleSample(t *testing.T) {
	if summary(nil)["p99Ms"] != nil {
		t.Fatal("empty windows must have no percentile")
	}
	result := summary([]Record{{DurationMs: 42}})
	if result["p50Ms"] != float64(42) || result["p99Ms"] != float64(42) {
		t.Fatal("single-sample percentiles must equal the sample")
	}
}
