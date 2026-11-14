// Package query exposes the shared telemetry query contract.
//
// SQL generation, validation, ClickHouse access, and datasource routing live
// in the nested internal package so callers see only this supported surface.
package query

import (
	"context"

	queryinternal "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query/internal"
	"gorm.io/gorm"
)

type (
	AttributeFilter = queryinternal.AttributeFilter
	CompiledQuery   = queryinternal.CompiledQuery
	Compiler        = queryinternal.Compiler
	Filter          = queryinternal.Filter
	MockExecutor    = queryinternal.MockExecutor
	Operation       = queryinternal.Operation
	QueryStore      = queryinternal.QueryStore
	Request         = queryinternal.Request
	Result          = queryinternal.Result
	RoutedStore     = queryinternal.RoutedStore
	Signal          = queryinternal.Signal
	Store           = queryinternal.Store
	Table           = queryinternal.Table
)

const (
	SignalTraces = queryinternal.SignalTraces
	SignalLogs   = queryinternal.SignalLogs

	OperationRecords    = queryinternal.OperationRecords
	OperationMetrics    = queryinternal.OperationMetrics
	OperationAttributes = queryinternal.OperationAttributes
	OperationServices   = queryinternal.OperationServices
	OperationDetail     = queryinternal.OperationDetail

	Traces         = queryinternal.Traces
	Logs           = queryinternal.Logs
	SearchSettings = queryinternal.SearchSettings
)

var (
	SeverityRanges = queryinternal.SeverityRanges
	TraceIDPattern = queryinternal.TraceIDPattern
)

func ParseAttributes(values []string) ([]AttributeFilter, error) {
	return queryinternal.ParseAttributes(values)
}

func Compile(request Request) ([]CompiledQuery, error) {
	return queryinternal.Compile(request)
}

func ServiceFilter(filter Filter) Filter {
	return queryinternal.ServiceFilter(filter)
}

func WithDataSource(ctx context.Context, id string) context.Context {
	return queryinternal.WithDataSource(ctx, id)
}

func DataSourceFromContext(ctx context.Context) string {
	return queryinternal.DataSourceFromContext(ctx)
}

func OpenStore() *Store {
	return queryinternal.OpenStore()
}

func OpenStores() (*Store, *Store) {
	return queryinternal.OpenStores()
}

func NewRoutedStores(control *gorm.DB) (*RoutedStore, *RoutedStore) {
	return queryinternal.NewRoutedStores(control)
}

func BootstrapDataSources(ctx context.Context, control *gorm.DB, organizationIDs []string) error {
	return queryinternal.BootstrapDataSources(ctx, control, organizationIDs)
}

func BootstrapMockDataSource(ctx context.Context, control *gorm.DB, organizationIDs []string) error {
	return queryinternal.BootstrapMockDataSource(ctx, control, organizationIDs)
}

func CheckConnection(ctx context.Context, store QueryStore) error {
	return queryinternal.CheckConnection(ctx, store)
}

func CheckTable(ctx context.Context, store QueryStore, statement string) error {
	return queryinternal.CheckTable(ctx, store, statement)
}

func Execute(ctx context.Context, store QueryStore, query CompiledQuery) ([]map[string]any, error) {
	return queryinternal.Execute(ctx, store, query)
}
