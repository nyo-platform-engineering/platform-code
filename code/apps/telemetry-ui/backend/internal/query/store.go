package query

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type QueryStore interface {
	Query(context.Context, string, ...any) ([]map[string]any, error)
}
type clickHouseStore struct{ db *sql.DB }

// OpenStore retains the shared connection settings for integration tools.
func OpenStore() *clickHouseStore { return openStore("") }

// OpenStores shares a pool when both signals resolve to the same connection.
func OpenStores() (*clickHouseStore, *clickHouseStore) {
	traceOptions, logOptions := connectionOptions("TRACES_"), connectionOptions("LOGS_")
	traces := openOptions(traceOptions)
	if traceOptions.Addr[0] == logOptions.Addr[0] && traceOptions.Auth == logOptions.Auth {
		return traces, traces
	}
	return traces, openOptions(logOptions)
}

func connectionOptions(signal string) *clickhouse.Options {
	value := func(key, fallback string) string {
		shared := envOr("CLICKHOUSE_"+key, fallback)
		if key == "PASSWORD" {
			if v, ok := os.LookupEnv("CLICKHOUSE_PASSWORD"); ok {
				shared = v
			}
		}
		if signal != "" {
			// An explicitly empty signal password is valid and must not inherit a secret.
			if v, ok := os.LookupEnv("CLICKHOUSE_" + signal + key); ok && (v != "" || key == "PASSWORD") {
				return v
			}
		}
		return shared
	}
	return &clickhouse.Options{
		Addr:        []string{value("ADDR", "127.0.0.1:9000")},
		Auth:        clickhouse.Auth{Database: "otel", Username: value("USER", "app"), Password: value("PASSWORD", "local-clickhouse-app")},
		DialTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
	}
}
func openStore(signal string) *clickHouseStore {
	return openOptions(connectionOptions(signal))
}
func openOptions(options *clickhouse.Options) *clickHouseStore {
	db := clickhouse.OpenDB(options)

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	return &clickHouseStore{db}
}
func (s *clickHouseStore) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, col := range columns {
			row[col] = values[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *clickHouseStore) Close() error { return s.db.Close() }
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (s *clickHouseStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// CheckConnection probes real database pools; QueryStore fakes still exercise the
// table probes in readiness without needing to implement driver-specific behavior.
func CheckConnection(ctx context.Context, store QueryStore) error {
	if connection, ok := store.(interface{ Ping(context.Context) error }); ok {
		return connection.Ping(ctx)
	}
	return nil
}
