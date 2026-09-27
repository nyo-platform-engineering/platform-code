package query

import (
	"errors"
	"fmt"
	"strings"
)

// Table is a closed allowlist. Request values cannot select arbitrary tables.
type Table uint8

const (
	Traces Table = iota + 1
	Logs
)

// SelectBuilder keeps predicates and their parameters together. SQL expressions
// (projection, grouping, ordering) are code-owned constants, never request input.
// User values enter through Filter, ContainsKey, and pagination arguments only.
type SelectBuilder struct {
	filter                                   Filter
	tenant                                   string
	signalTable                              Table
	serverOnly                               bool
	table, columns, pre, where, group, order string
	preArgs, whereArgs                       []any
	limit, offset                            int
	paginated, searchBudget                  bool
}

func Select(table Table, columns string, f Filter, tenant string, serverOnly bool) (*SelectBuilder, error) {
	var name, signal string
	switch table {
	case Traces:
		name, signal = "otel.otel_traces", "span"
	case Logs:
		name, signal = "otel.otel_logs", "log"
	default:
		return nil, errors.New("invalid telemetry table")
	}
	if err := f.ValidateScope(tenant, signal, false); err != nil {
		return nil, err
	}
	pre, preArgs, where, whereArgs := f.conditions(tenant, table == Logs, serverOnly)
	return &SelectBuilder{filter: f, tenant: tenant, signalTable: table, serverOnly: serverOnly, table: name, columns: columns, pre: pre, preArgs: preArgs, where: where, whereArgs: whereArgs, searchBudget: f.Settings() != ""}, nil
}

func (b *SelectBuilder) ContainsKey(value string) *SelectBuilder {
	if value != "" {
		if b.where != "" {
			b.where += " AND "
		}
		b.where += "positionCaseInsensitiveUTF8(key, ?) > 0"
		b.whereArgs = append(b.whereArgs, value)
	}
	return b
}
func (b *SelectBuilder) GroupBy(expression string) *SelectBuilder { b.group = expression; return b }
func (b *SelectBuilder) OrderBy(expression string) *SelectBuilder { b.order = expression; return b }
func (b *SelectBuilder) Page(limit, offset int) *SelectBuilder {
	b.limit, b.offset, b.paginated = limit, offset, true
	return b
}
func (b *SelectBuilder) SearchBudget() *SelectBuilder { b.searchBudget = true; return b }

func (b *SelectBuilder) Compile(name string) (CompiledQuery, error) {
	if b.table == "" || b.pre == "" {
		return CompiledQuery{}, errors.New("unscoped query builder")
	}
	if b.paginated && (b.limit < 1 || b.limit > 501 || b.offset < 0 || b.offset > 5000) {
		return CompiledQuery{}, errors.New("invalid pagination")
	}
	var sql strings.Builder
	fmt.Fprintf(&sql, "SELECT %s FROM %s PREWHERE %s", b.columns, b.table, b.pre)
	args := append([]any{}, b.preArgs...)
	if b.where != "" {
		sql.WriteString(" WHERE " + b.where)
		args = append(args, b.whereArgs...)
	}
	if b.group != "" {
		sql.WriteString(" GROUP BY " + b.group)
	}
	if b.order != "" {
		sql.WriteString(" ORDER BY " + b.order)
	}
	if b.paginated {
		sql.WriteString(" LIMIT ? OFFSET ?")
		args = append(args, b.limit, b.offset)
	}
	if b.searchBudget {
		sql.WriteString(SearchSettings)
	}
	return CompiledQuery{Name: name, SQL: sql.String(), Args: args, Filter: b.filter, Tenant: b.tenant, Table: b.signalTable, ServerOnly: b.serverOnly}, nil
}

// Services returns one bounded query per connection. The metadata model merges them.
func Services(f Filter, tenant string) ([]CompiledQuery, error) {
	f.Search = ""
	f.Attributes = nil
	f.Severities = nil
	f.Statuses = nil
	f.MinDuration = 0
	result := make([]CompiledQuery, 0, 2)
	for _, table := range []Table{Traces, Logs} {
		builder, err := Select(table, "DISTINCT ServiceName AS service", f, tenant, false)
		if err != nil {
			return nil, err
		}
		name := "trace-services"
		if table == Logs {
			name = "log-services"
		}
		compiled, err := builder.OrderBy("service").Page(500, 0).Compile(name)
		if err != nil {
			return nil, err
		}
		result = append(result, compiled)
	}
	return result, nil
}
