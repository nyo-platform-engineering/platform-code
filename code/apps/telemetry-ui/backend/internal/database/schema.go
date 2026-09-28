package database

import (
	"context"
	"fmt"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// CheckSchema verifies the auth schema without changing it.
func CheckSchema(ctx context.Context, db *gorm.DB) error {
	db = db.WithContext(ctx)
	for _, model := range []any{&LoginAttempt{}, &Session{}} {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return err
		}
		columns, err := db.Migrator().ColumnTypes(model)
		if err != nil {
			return fmt.Errorf("inspect auth table %s: %w", stmt.Table, err)
		}
		if err := checkColumns(stmt.Schema, columns); err != nil {
			return err
		}

		indexes, err := readIndexes(db, stmt.Table)
		if err != nil {
			return fmt.Errorf("inspect auth indexes on %s: %w", stmt.Table, err)
		}
		if err := checkIndexes(stmt.Schema, indexes); err != nil {
			return err
		}
	}
	return nil
}

type schemaIndex struct {
	Name       string
	PrimaryKey bool
	Usable     bool
	Columns    []string `gorm:"serializer:json"`
}

// GORM's index metadata does not expose validity or partial-index predicates.
func readIndexes(db *gorm.DB, table string) ([]schemaIndex, error) {
	const query = `
  SELECT
   index_table.relname AS name,
   definition.indisprimary AS primary_key,
   definition.indisvalid
    AND definition.indisready
    AND definition.indpred IS NULL
    AND definition.indexprs IS NULL AS usable,
   to_json(ARRAY(
    SELECT column_info.attname::text
    FROM unnest(definition.indkey) WITH ORDINALITY AS key_column(number, position)
    JOIN pg_attribute AS column_info
     ON column_info.attrelid = definition.indrelid
     AND column_info.attnum = key_column.number
    ORDER BY key_column.position
   )) AS columns
  FROM pg_index AS definition
  JOIN pg_class AS index_table ON index_table.oid = definition.indexrelid
  WHERE definition.indrelid = to_regclass(?)
 `
	var indexes []schemaIndex
	err := db.Raw(query, table).Scan(&indexes).Error
	return indexes, err
}

func checkColumns(expected *schema.Schema, columns []gorm.ColumnType) error {
	columnsByName := make(map[string]gorm.ColumnType, len(columns))
	for _, column := range columns {
		columnsByName[column.Name()] = column
	}
	for _, field := range expected.Fields {
		column, ok := columnsByName[field.DBName]
		if !ok {
			return fmt.Errorf("auth schema: %s.%s is missing", expected.Table, field.DBName)
		}
		if column.DatabaseTypeName() != string(field.DataType) {
			return fmt.Errorf("auth schema: %s.%s must have type %s", expected.Table, field.DBName, field.DataType)
		}
		nullable, known := column.Nullable()
		if field.NotNull && (!known || nullable) {
			return fmt.Errorf("auth schema: %s.%s must be NOT NULL", expected.Table, field.DBName)
		}
	}
	return nil
}

func checkIndexes(expected *schema.Schema, indexes []schemaIndex) error {
	primaryKeyFound := false
	indexesByName := make(map[string]schemaIndex, len(indexes))
	for _, index := range indexes {
		indexesByName[index.Name] = index
		if index.PrimaryKey && index.Usable && slices.Equal(index.Columns, expected.PrimaryFieldDBNames) {
			primaryKeyFound = true
		}
	}
	if !primaryKeyFound {
		return fmt.Errorf("auth schema: %s requires a primary key on %v", expected.Table, expected.PrimaryFieldDBNames)
	}

	for _, required := range expected.ParseIndexes() {
		columns := make([]string, len(required.Fields))
		for i, field := range required.Fields {
			columns[i] = field.DBName
		}
		index, found := indexesByName[required.Name]
		if !found || !index.Usable || !slices.Equal(index.Columns, columns) {
			return fmt.Errorf("auth schema: %s requires usable index %s on %v", expected.Table, required.Name, columns)
		}
	}
	return nil
}
