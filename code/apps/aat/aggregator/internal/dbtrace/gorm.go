package dbtrace

import (
	"context"
	"gorm.io/gorm"
)

type queryInfoKey struct{}
type queryInfo struct{ operation, table string }

// Register obtains operation and collection names from GORM's structured query
// metadata rather than guessing them from SQL (especially joins and CTEs).
func Register(db *gorm.DB) error {
	callbacks := []struct {
		operation string
		register  func(string, func(*gorm.DB)) error
	}{
		{"SELECT", db.Callback().Query().Before("gorm:query").Register},
		{"INSERT", db.Callback().Create().Before("gorm:create").Register},
		{"UPDATE", db.Callback().Update().Before("gorm:update").Register},
		{"DELETE", db.Callback().Delete().Before("gorm:delete").Register},
		{"", db.Callback().Row().Before("gorm:row").Register},
	}
	for _, callback := range callbacks {
		if err := callback.register("aat:dbtrace", func(tx *gorm.DB) {
			if tx.Statement.Table != "" {
				tx.Statement.Context = context.WithValue(tx.Statement.Context, queryInfoKey{}, queryInfo{callback.operation, tx.Statement.Table})
			}
		}); err != nil {
			return err
		}
	}
	return nil
}
