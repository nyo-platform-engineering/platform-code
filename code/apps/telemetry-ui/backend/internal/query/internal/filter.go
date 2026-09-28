package queryinternal

import (
	"regexp"
	"strings"
	"time"
)

type Filter struct {
	Attributes                     []AttributeFilter
	DiscoveryScope, KeySearch      string
	From, To                       time.Time
	Environment, TraceID, Search   string
	Services, Severities, Statuses []string
	Limit, Offset                  int
	MinDuration                    float64
}

var TraceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var SeverityRanges = map[string][2]int{"unspecified": {0, 0}, "trace": {1, 4}, "debug": {5, 8}, "info": {9, 12}, "warn": {13, 16}, "error": {17, 20}, "fatal": {21, 24}}

func (f Filter) baseWhere(organizationScope string, logs bool) (string, []any) {
	clause := "Timestamp >= fromUnixTimestamp64Nano(?) AND Timestamp < fromUnixTimestamp64Nano(?) AND ResourceAttributes['tenant.id'] = ?"
	args := []any{f.From.UnixNano(), f.To.UnixNano(), organizationScope}
	for _, item := range []struct{ column, value string }{{"ResourceAttributes['deployment.environment.name']", f.Environment}, {"TraceId", f.TraceID}} {
		if item.value != "" {
			clause += " AND " + item.column + " = ?"
			args = append(args, item.value)
		}
	}
	if len(f.Services) > 0 {
		placeholders := make([]string, len(f.Services))
		for i, service := range f.Services {
			placeholders[i] = "?"
			args = append(args, service)
		}
		clause += " AND ServiceName IN (" + strings.Join(placeholders, ", ") + ")"
	}
	if logs && len(f.Severities) > 0 {
		bands := make([]string, len(f.Severities))
		for i, severity := range f.Severities {
			band := SeverityRanges[severity]
			bands[i] = "SeverityNumber BETWEEN ? AND ?"
			args = append(args, band[0], band[1])
		}
		clause += " AND (" + strings.Join(bands, " OR ") + ")"
	}
	if !logs {
		if len(f.Statuses) == 1 {
			if f.Statuses[0] == "error" {
				clause += " AND StatusCode = 'Error'"
			} else {
				clause += " AND StatusCode != 'Error'"
			}
		}
		if f.MinDuration > 0 {
			clause += " AND Duration >= ?"
			args = append(args, f.MinDuration*1e6)
		}
	}

	if TraceIDPattern.MatchString(strings.ToLower(f.Search)) {
		clause += " AND TraceId = ?"
		args = append(args, strings.ToLower(f.Search))
	}

	return clause, args
}

func (f Filter) lateWhere(logs bool) (string, []any) {
	clause := ""
	var args []any
	if f.Search != "" && !TraceIDPattern.MatchString(strings.ToLower(f.Search)) {
		column := "SpanName"
		if logs {
			column = "Body"
		}
		clause += " AND (positionCaseInsensitiveUTF8(" + column + ", ?) > 0 OR positionCaseInsensitiveUTF8(ServiceName, ?) > 0 OR positionCaseInsensitiveUTF8(TraceId, ?) > 0)"
		args = append(args, f.Search, f.Search, f.Search)
	}
	for _, attribute := range f.Attributes {
		predicate, values := attribute.sql()
		clause += " AND " + predicate
		args = append(args, values...)
	}
	return clause, args
}

// Fail instead of returning incomplete counts when a search exceeds its read budget.
const SearchSettings = " SETTINGS max_execution_time=3, max_rows_to_read=2000000, max_bytes_to_read=268435456, read_overflow_mode='throw'"

func (f Filter) Settings() string {
	if f.Search != "" || len(f.Attributes) > 0 {
		return SearchSettings
	}
	return ""
}

func (f Filter) Where(organizationScope string, logs bool) (string, []any) {
	base, args := f.baseWhere(organizationScope, logs)
	late, values := f.lateWhere(logs)
	return base + late, append(args, values...)
}
