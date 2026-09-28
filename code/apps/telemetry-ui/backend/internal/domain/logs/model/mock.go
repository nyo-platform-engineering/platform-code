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
			records = append(records, mockLog(end, traceIndex, spanIndex))
		}
	}
	return records
}

func mockLog(epoch time.Time, traceIndex, spanIndex int) mock.Record {
	traceID := fmt.Sprintf("%032x", traceIndex+1)
	timestamp := epoch.Add(-time.Duration(traceIndex)*mockTraceInterval - 5*time.Second)
	// Regular failures and slow successes make the charts useful at every time scale.
	failed := (traceIndex/3+traceIndex%3*7)%37 == 0
	services := []string{"api-gateway", "checkout", "payments"}
	service := services[(traceIndex+spanIndex)%len(services)]
	duration := float64(25+(traceIndex*37)%360) / float64(spanIndex+1)
	status, message, severity := "Ok", "Request completed", "info"
	if failed {
		status, message, severity = "Error", "Payment provider timed out", "error"
		duration += 650
	}
	if !failed && traceIndex%7 == 0 {
		severity, message = "warn", "Slow response; retry succeeded"
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
		Severity:           severity,
		Body:               fmt.Sprintf(`{"message":%q,"order":{"id":%q,"total":%d},"mock":true}`, message, attrs["order.id"], 40+traceIndex%160),
	}
}
