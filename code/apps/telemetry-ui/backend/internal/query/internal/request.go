package queryinternal

import (
	"context"
	"fmt"
)

// Request is passed to either the mock store or the SQL compiler.
// OrganizationScope must come from the authenticated principal.
type Request struct {
	Filter            Filter
	Signal            Signal
	Operation         Operation
	DataSourceID      string
	OrganizationScope string
}

type Signal string

const (
	SignalTraces Signal = "traces"
	SignalLogs   Signal = "logs"
)

type Operation string

const (
	OperationRecords    Operation = "records"
	OperationMetrics    Operation = "metrics"
	OperationAttributes Operation = "attributes"
	OperationServices   Operation = "services"
	OperationDetail     Operation = "detail"
)

type dataSourceContextKey struct{}

func WithDataSource(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, dataSourceContextKey{}, id)
}

func DataSourceFromContext(ctx context.Context) string {
	id, _ := ctx.Value(dataSourceContextKey{}).(string)
	return id
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
	switch r.Signal {
	case SignalLogs:
		return Logs
	default:
		return Traces
	}
}

func (r Request) ServerOnly() bool {
	return r.Signal == SignalTraces && (r.Operation == OperationRecords || r.Operation == OperationMetrics || r.Operation == OperationAttributes)
}

func (r Request) List() bool {
	switch r.Operation {
	case OperationRecords, OperationDetail, OperationAttributes:
		return true
	default:
		return false
	}
}

func (r Request) Validate() error {
	valid := false
	switch r.Signal {
	case SignalTraces:
		valid = r.Operation == OperationRecords || r.Operation == OperationMetrics || r.Operation == OperationAttributes || r.Operation == OperationServices || r.Operation == OperationDetail
	case SignalLogs:
		valid = r.Operation == OperationRecords || r.Operation == OperationMetrics || r.Operation == OperationAttributes || r.Operation == OperationServices
	}
	if !valid {
		return fmt.Errorf("unsupported query operation: signal=%q operation=%q", r.Signal, r.Operation)
	}
	signal := "span"
	if r.Signal == SignalLogs {
		signal = "log"
	}
	filter := r.Filter
	if r.Operation == OperationServices {
		filter = ServiceFilter(filter)
	}
	if err := filter.ValidateScope(r.OrganizationScope, signal, r.Operation == OperationAttributes); err != nil {
		return err
	}
	if r.List() && (r.Filter.Limit < 0 || r.Filter.Limit > 500 || r.Filter.Offset < 0 || r.Filter.Offset > 5000) {
		return fmt.Errorf("invalid pagination")
	}
	return nil
}

func (r Request) Name() string {
	return string(r.Signal) + "." + string(r.Operation)
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
