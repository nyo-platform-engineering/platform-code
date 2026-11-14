package queryinternal

import (
	"context"
	"crypto/tls"
	"database/sql"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"gorm.io/gorm"
)

type QueryStore interface {
	Query(context.Context, string, ...any) ([]map[string]any, error)
}
type Store struct {
	db      *sql.DB
	orm     *gorm.DB
	initErr error
}

// OpenStore retains the shared connection settings for integration tools.
func OpenStore() *Store { return openStore("") }

// OpenStores shares a pool when both signals resolve to the same connection.
func OpenStores() (*Store, *Store) {
	traceOptions, logOptions := connectionOptions("TRACES_"), connectionOptions("LOGS_")
	traces := openOptions(traceOptions)
	if traceOptions.Addr[0] == logOptions.Addr[0] && traceOptions.Auth == logOptions.Auth && (traceOptions.TLS != nil) == (logOptions.TLS != nil) {
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
	options := &clickhouse.Options{
		Addr:        []string{value("ADDR", "127.0.0.1:9000")},
		Auth:        clickhouse.Auth{Database: value("DATABASE", "otel"), Username: value("USER", "app"), Password: value("PASSWORD", "local-clickhouse-app")},
		DialTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
	}
	secure, _ := strconv.ParseBool(value("SECURE", "false"))
	if secure {
		host, _, err := net.SplitHostPort(options.Addr[0])
		if err != nil {
			host = options.Addr[0]
		}
		options.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	return options
}
func openStore(signal string) *Store {
	return openOptions(connectionOptions(signal))
}
func openOptions(options *clickhouse.Options) *Store {
	db := clickhouse.OpenDB(options)

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	orm, err := openGORM(db, false)
	return &Store{db: db, orm: orm, initErr: err}
}
func (s *Store) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	result := make([]map[string]any, 0)
	err := s.orm.WithContext(ctx).Raw(query, args...).Scan(&result).Error
	return result, err
}

func (s *Store) Close() error { return s.db.Close() }
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (s *Store) Ping(ctx context.Context) error {
	if s.initErr != nil {
		return s.initErr
	}
	return s.db.PingContext(ctx)
}

// CheckConnection probes real database pools; QueryStore fakes still exercise the
// table probes in readiness without needing to implement driver-specific behavior.
func CheckConnection(ctx context.Context, store QueryStore) error {
	if connection, ok := store.(interface{ Ping(context.Context) error }); ok {
		return connection.Ping(ctx)
	}
	return nil
}

func CheckTable(ctx context.Context, store QueryStore, statement string) error {
	if probe, ok := store.(interface {
		Probe(context.Context, string) error
	}); ok {
		return probe.Probe(ctx, statement)
	}
	_, err := store.Query(ctx, statement)
	return err
}

// Execute runs bound SQL against a database store.
func Execute(ctx context.Context, store QueryStore, q CompiledQuery) ([]map[string]any, error) {
	return store.Query(ctx, q.SQL, q.Args...)
}
