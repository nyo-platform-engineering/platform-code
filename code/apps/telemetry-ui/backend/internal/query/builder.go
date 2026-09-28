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

// selectBuilder keeps predicates and their parameters together. SQL expressions
// (projection, grouping, ordering) are code-owned constants, never request input.
// User values enter through Filter, containsKey, and pagination arguments only.
type selectBuilder struct {
	table, columns, pre, where, group, order string
	preArgs, whereArgs                       []any
	limit, offset                            int
	paginated, searchBudget                  bool
}

func selectQuery(table Table, columns string, f Filter, tenant string, serverOnly bool) (*selectBuilder, error) {
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
	return &selectBuilder{table: name, columns: columns, pre: pre, preArgs: preArgs, where: where, whereArgs: whereArgs, searchBudget: f.Settings() != ""}, nil
}

func (b *selectBuilder) containsKey(value string) *selectBuilder {
	if value != "" {
		if b.where != "" {
			b.where += " AND "
		}
		b.where += "positionCaseInsensitiveUTF8(key, ?) > 0"
		b.whereArgs = append(b.whereArgs, value)
	}
	return b
}
func (b *selectBuilder) groupBy(expression string) *selectBuilder { b.group = expression; return b }
func (b *selectBuilder) orderBy(expression string) *selectBuilder { b.order = expression; return b }
func (b *selectBuilder) page(limit, offset int) *selectBuilder {
	b.limit, b.offset, b.paginated = limit, offset, true
	return b
}
func (b *selectBuilder) withSearchBudget() *selectBuilder { b.searchBudget = true; return b }

func (b *selectBuilder) compile(name string) (CompiledQuery, error) {
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
	return CompiledQuery{Name: name, SQL: sql.String(), Args: args}, nil
}
