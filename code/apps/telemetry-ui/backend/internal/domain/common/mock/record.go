package mock

import "time"

// Record is the in-memory telemetry shape. Maps are only used at the API boundary.
type Record struct {
	Timestamp          time.Time
	TraceID            string
	SpanID             string
	ParentSpanID       string
	Service            string
	Name               string
	DurationMs         float64
	Status             string
	Message            string
	Attributes         map[string]string
	ResourceAttributes map[string]string
	Server             bool
	Severity           string
	Body               string
}

func (r Record) row() map[string]any {
	row := map[string]any{
		"timestamp":          r.Timestamp,
		"traceId":            r.TraceID,
		"spanId":             r.SpanID,
		"parentSpanId":       r.ParentSpanID,
		"service":            r.Service,
		"name":               r.Name,
		"durationMs":         r.DurationMs,
		"status":             r.Status,
		"message":            r.Message,
		"attributes":         r.Attributes,
		"resourceAttributes": r.ResourceAttributes,
		"server":             r.Server,
	}
	if r.Severity != "" {
		row["severity"] = r.Severity
		row["body"] = r.Body
	}
	return row
}

func responseRows(records []Record) []map[string]any {
	rows := make([]map[string]any, 0, len(records))
	for _, record := range records {
		rows = append(rows, record.row())
	}
	return rows
}
