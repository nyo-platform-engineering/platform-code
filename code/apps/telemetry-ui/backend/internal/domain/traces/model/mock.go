package model

import (
	"context"
	"fmt"
	"time"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/common/mock"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

// MockStore anchors demo records to the query end time: live windows stay fresh,
// while a fixed end time keeps pagination and correlated records stable.
type MockStore struct{}

func (s MockStore) Query(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	return (mock.Store{}).Query(ctx, sql, args...)
}

func (s MockStore) Execute(ctx context.Context, q query.Request) (query.Result, error) {
	return (mock.Store{Rows: s.rows(q.Filter.To)}).Execute(ctx, q)
}

const (
	mockTraceCount    = 1800
	mockSpansPerTrace = 3
	mockTraceInterval = 2 * time.Second
)

func (s MockStore) rows(end time.Time) []mock.Record {
	records := make([]mock.Record, 0, mockTraceCount*mockSpansPerTrace)
	for traceIndex := 0; traceIndex < mockTraceCount; traceIndex++ {
		for spanIndex := 0; spanIndex < mockSpansPerTrace; spanIndex++ {
			records = append(records, mockSpan(end, traceIndex, spanIndex))
		}
	}
	return records
}

func mockSpan(epoch time.Time, traceIndex, spanIndex int) mock.Record {
	traceID := fmt.Sprintf("%032x", traceIndex+1)
	timestamp := epoch.Add(-time.Duration(traceIndex)*mockTraceInterval - 5*time.Second)
	// Regular failures and slow successes make the charts useful at every time scale.
	failed := (traceIndex/3+traceIndex%3*7)%37 == 0
	services := []string{"api-gateway", "checkout", "payments"}
	service := services[(traceIndex+spanIndex)%len(services)]
	duration := mockLatency(traceIndex) / float64(spanIndex+1)
	status, message := "Ok", "Request completed"
	if failed {
		status, message = "Error", "Payment provider timed out"
		duration += 1500 / float64(spanIndex+1)
	}
	if !failed && traceIndex%7 == 0 {
		message = "Slow response; retry succeeded"
	}
	spanID := fmt.Sprintf("%016x", (traceIndex+1)*16+spanIndex)
	parent := ""
	if spanIndex > 0 {
		parent = fmt.Sprintf("%016x", (traceIndex+1)*16+spanIndex-1)
	}
	attrs := map[string]string{
		"http.request.method":       "POST",
		"http.route":                "/api/checkout",
		"http.response.status_code": "200",
		"order.id":                  fmt.Sprintf("demo-%04d", traceIndex+1),
	}
	if failed {
		attrs["http.response.status_code"] = "504"
	}
	resource := map[string]string{
		"tenant.id":                   "local",
		"deployment.environment.name": "local",
		"service.version":             "1.4.2",
		"host.name":                   "preview",
	}
	return mock.Record{
		Timestamp:          timestamp.Add(time.Duration(spanIndex) * time.Millisecond),
		TraceID:            traceID,
		SpanID:             spanID,
		ParentSpanID:       parent,
		Service:            service,
		Name:               []string{"POST /api/checkout", "checkout.create", "payments.authorize"}[spanIndex],
		DurationMs:         duration,
		Status:             status,
		Message:            message,
		Attributes:         attrs,
		ResourceAttributes: resource,
		Server:             spanIndex == 0,
	}
}

// mockLatency mixes fast requests, slower work, and rare multi-second outliers.
// Each service sees its own deterministic sequence, so filtering by service
// preserves the latency distribution and fixed-window queries stay repeatable.
func mockLatency(traceIndex int) float64 {
	sample := traceIndex / mockSpansPerTrace
	service := traceIndex % mockSpansPerTrace
	rank := (sample*37 + service*17) % 100
	jitter := float64((sample*13 + service*11) % 31)
	var duration float64
	switch {
	case rank < 80:
		duration = 20 + float64(rank) + jitter
	case rank < 95:
		duration = 150 + float64(rank-80)*20 + jitter
	case rank < 99:
		duration = 700 + float64(rank-95)*160 + jitter
	default:
		duration = 2200 + jitter*25
	}
	// A gradual recurring slowdown gives the time series visible shape.
	phase := float64(sample%80) / 80
	return duration * (1 + 0.6*phase)
}
