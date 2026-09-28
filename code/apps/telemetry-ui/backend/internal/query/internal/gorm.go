package queryinternal

import (
	"strings"
	"sync"

	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func openGORM(pool gorm.ConnPool, dryRun bool) (*gorm.DB, error) {
	db, err := gorm.Open(clickhouse.New(clickhouse.Config{
		Conn:                      pool,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DryRun:                 dryRun,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		// Never log interpolated SQL containing organization scope or search values.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	db.Callback().Query().Clauses = []string{
		"SELECT", "FROM", "PREWHERE", "WHERE", "GROUP BY", "ORDER BY", "LIMIT", "SETTINGS",
	}
	return db, nil
}

// Shared only as a template. Each compilation gets its own dry-run statement.
// Ping and server-version discovery are disabled, so previews need no database.
var compilerDB = sync.OnceValues(func() (*gorm.DB, error) { return openGORM(nil, true) })

type prewhere struct{ expression clause.Expression }

func (p prewhere) Name() string                 { return "PREWHERE" }
func (p prewhere) Build(builder clause.Builder) { p.expression.Build(builder) }
func (p prewhere) MergeClause(c *clause.Clause) { c.Expression = p.expression }

type searchSettings struct{}

func (searchSettings) Name() string { return "SETTINGS" }
func (searchSettings) Build(builder clause.Builder) {
	builder.WriteString(strings.TrimPrefix(SearchSettings, " SETTINGS "))
}
func (s searchSettings) MergeClause(c *clause.Clause) { c.Expression = s }
