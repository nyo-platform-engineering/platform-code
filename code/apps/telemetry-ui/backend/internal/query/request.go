package query

import (
	"context"
	"fmt"
)

// Request is passed to either the mock store or the SQL compiler.
// OrganizationScope must come from the authenticated principal. Tenant remains
// as a compatibility field for older internal callers and tests.
type Request struct {
	Filter            Filter
	Kind              string
	Signal            string
	DataSourceID      string
	OrganizationScope string
	Tenant            string
}

type dataSourceContextKey struct{}

func WithDataSource(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, dataSourceContextKey{}, id)
}

func DataSourceFromContext(ctx context.Context) string {
	id, _ := ctx.Value(dataSourceContextKey{}).(string)
	return id
}

func (r Request) Scope() string {
	if r.OrganizationScope != "" {
		return r.OrganizationScope
	}
	return r.Tenant
}

type Result struct {
	Data    []map[string]any
	Summary []map[string]any
}

type MockExecutor interface {
	Execute(context.Context, Request) (Result, error)
}

type Compiler func(Request) ([]CompiledQuery, error)

func (r Request) Table() Table {
	switch r.Kind {
	case "logs", "logs-volume", "logs-keys", "log-services":
		return Logs
	default:
		return Traces
	}
}

func (r Request) ServerOnly() bool {
	return r.Kind == "traces" || r.Kind == "red" || r.Kind == "traces-keys"
}

func (r Request) List() bool {
	switch r.Kind {
	case "traces", "detail", "logs", "traces-keys", "logs-keys":
		return true
	default:
		return false
	}
}

func (r Request) Validate() error {
	switch r.Kind {
	case "traces", "detail", "red", "traces-keys", "logs", "logs-volume", "logs-keys", "services", "trace-services", "log-services":
	default:
		return fmt.Errorf("unsupported query kind: %q", r.Kind)
	}
	signal := "span"
	if r.Table() == Logs {
		signal = "log"
	}
	filter := r.Filter
	if r.Kind == "services" || r.Kind == "trace-services" || r.Kind == "log-services" {
		filter = ServiceFilter(filter)
	}
	if err := filter.ValidateScope(r.Scope(), signal, r.Kind == "traces-keys" || r.Kind == "logs-keys"); err != nil {
		return err
	}
	if r.List() && (r.Filter.Limit < 0 || r.Filter.Limit > 500 || r.Filter.Offset < 0 || r.Filter.Offset > 5000) {
		return fmt.Errorf("invalid pagination")
	}
	return nil
}

// Service discovery ignores filters that only make sense for individual records.
func ServiceFilter(f Filter) Filter {
	f.Search = ""
	f.Attributes = nil
	f.Severities = nil
	f.Statuses = nil
	f.MinDuration = 0
	return f
}
