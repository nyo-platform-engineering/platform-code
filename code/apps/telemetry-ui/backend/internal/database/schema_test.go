package database

import (
	"database/sql"
	"sync"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

func TestSchemaRequirements(t *testing.T) {
	for _, model := range []any{&LoginAttempt{}, &Session{}} {
		expected, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		columns := make([]gorm.ColumnType, 0, len(expected.Fields))
		for _, field := range expected.Fields {
			columns = append(columns, migrator.ColumnType{
				NameValue:     sql.NullString{String: field.DBName, Valid: true},
				DataTypeValue: sql.NullString{String: string(field.DataType), Valid: true},
				NullableValue: sql.NullBool{Bool: false, Valid: true},
			})
		}
		indexes := []schemaIndex{{Name: "primary", PrimaryKey: true, Usable: true, Columns: []string{"token_hash"}}}
		for _, index := range expected.ParseIndexes() {
			indexes = append(indexes, schemaIndex{Name: index.Name, Usable: true, Columns: []string{"expires_at"}})
		}
		t.Run(expected.Table, func(t *testing.T) {
			if err := checkColumns(expected, columns); err != nil {
				t.Fatal(err)
			}
			if err := checkIndexes(expected, indexes); err != nil {
				t.Fatal(err)
			}
			if err := checkColumns(expected, columns[1:]); err == nil {
				t.Fatal("missing column accepted")
			}
			changed := append([]gorm.ColumnType(nil), columns...)
			wrong := columns[0].(migrator.ColumnType)
			wrong.DataTypeValue.String = "varchar"
			changed[0] = wrong
			if err := checkColumns(expected, changed); err == nil {
				t.Fatal("wrong type accepted")
			}
			wrong = columns[0].(migrator.ColumnType)
			wrong.NullableValue.Bool = true
			changed[0] = wrong
			if err := checkColumns(expected, changed); err == nil {
				t.Fatal("nullable required column accepted")
			}
			if err := checkIndexes(expected, indexes[1:]); err == nil {
				t.Fatal("missing primary key accepted")
			}
			if err := checkIndexes(expected, indexes[:1]); err == nil {
				t.Fatal("missing expiry index accepted")
			}
			changedIndexes := append([]schemaIndex(nil), indexes...)
			changedIndexes[1].Columns = []string{"provider"}
			if err := checkIndexes(expected, changedIndexes); err == nil {
				t.Fatal("wrong index columns accepted")
			}
			changedIndexes[1] = indexes[1]
			changedIndexes[1].Usable = false
			if err := checkIndexes(expected, changedIndexes); err == nil {
				t.Fatal("invalid or partial index accepted")
			}
			changedIndexes = append([]schemaIndex(nil), indexes...)
			changedIndexes[0].Columns = []string{"token_hash", "provider"}
			if err := checkIndexes(expected, changedIndexes); err == nil {
				t.Fatal("composite primary key accepted")
			}
		})
	}
}
