package mock

import (
	"slices"
	"strings"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func matches(record Record, plan query.Request) bool {
	filter := plan.Filter
	if record.ResourceAttributes["tenant.id"] != plan.OrganizationScope {
		return false
	}
	if record.Timestamp.Before(filter.From) || !record.Timestamp.Before(filter.To) {
		return false
	}
	if filter.Environment != "" && record.ResourceAttributes["deployment.environment.name"] != filter.Environment {
		return false
	}
	if filter.TraceID != "" && record.TraceID != filter.TraceID {
		return false
	}
	if !includes(filter.Services, record.Service) {
		return false
	}
	if plan.ServerOnly() && !record.Server {
		return false
	}
	if !matchesSignal(record, filter, plan.Table()) {
		return false
	}
	if !matchesSearch(record, filter.Search, plan.Table()) {
		return false
	}
	for _, attribute := range filter.Attributes {
		if !matchAttribute(record, attribute) {
			return false
		}
	}
	return true
}

func matchesSignal(record Record, filter query.Filter, table query.Table) bool {
	if table == query.Logs {
		return includes(filter.Severities, record.Severity)
	}
	status := "ok"
	if record.Status == "Error" {
		status = "error"
	}
	return includes(filter.Statuses, status) && record.DurationMs >= filter.MinDuration
}

func matchesSearch(record Record, search string, table query.Table) bool {
	if query.TraceIDPattern.MatchString(strings.ToLower(search)) {
		return record.TraceID == strings.ToLower(search)
	}
	text := record.Name
	if table == query.Logs {
		text = record.Body
	}
	return contains(text, search) || contains(record.Service, search) || contains(record.TraceID, search)
}

func contains(value, term string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(term))
}

func includes(values []string, value string) bool {
	return len(values) == 0 || slices.Contains(values, value)
}
