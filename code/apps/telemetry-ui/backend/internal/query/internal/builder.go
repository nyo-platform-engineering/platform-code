package queryinternal

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Table is a closed allowlist. Request values cannot select arbitrary tables.
type Table uint8

const (
	Traces Table = iota + 1
	Logs
)

// SQL expressions passed to this private builder are fixed in compile.go.
// Request values enter GORM only as arguments, never Select/Order/Group strings.
type selectBuilder struct {
	db  *gorm.DB
	err error
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
	base, err := compilerDB()
	if err != nil {
		return nil, err
	}
	pre, preArgs, where, whereArgs := f.conditions(tenant, table == Logs, serverOnly)
	db := base.Session(&gorm.Session{NewDB: true}).Table(name).Select(columns).
		Clauses(prewhere{expression: clause.Expr{SQL: pre, Vars: preArgs}})
	if where != "" {
		db = db.Where(where, whereArgs...)
	}
	if f.Settings() != "" {
		db = db.Clauses(searchSettings{})
	}
	return &selectBuilder{db: db}, nil
}

func (b *selectBuilder) containsKey(value string) *selectBuilder {
	if value != "" {
		b.db = b.db.Where("positionCaseInsensitiveUTF8(key, ?) > 0", value)
	}
	return b
}
func (b *selectBuilder) groupBy(expression string) *selectBuilder {
	if expression != "" {
		b.db = b.db.Group(expression)
	}
	return b
}
func (b *selectBuilder) orderBy(expression string) *selectBuilder {
	if expression != "" {
		b.db = b.db.Order(expression)
	}
	return b
}
func (b *selectBuilder) page(limit, offset int) *selectBuilder {
	if limit < 1 || limit > 501 || offset < 0 || offset > 5000 {
		b.err = errors.New("invalid pagination")
		return b
	}
	b.db = b.db.Limit(limit).Offset(offset)
	return b
}
func (b *selectBuilder) withSearchBudget() *selectBuilder {
	b.db = b.db.Clauses(searchSettings{})
	return b
}
func (b *selectBuilder) compile(name string) (CompiledQuery, error) {
	if b.err != nil {
		return CompiledQuery{}, b.err
	}
	if b.db == nil {
		return CompiledQuery{}, errors.New("unscoped query builder")
	}
	var rows []map[string]any
	result := b.db.Find(&rows)
	if result.Error != nil {
		return CompiledQuery{}, result.Error
	}
	return CompiledQuery{Name: name, SQL: result.Statement.SQL.String(), Args: append([]any(nil), result.Statement.Vars...)}, nil
}
