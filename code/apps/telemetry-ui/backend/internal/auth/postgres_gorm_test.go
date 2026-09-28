package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Run GORM's execution and row scanning against a controlled SQL connection.
type authConnector struct {
	query   string
	queries []string
	args    []driver.NamedValue
	columns []string
	rows    [][]driver.Value
	err     error
}

func (c *authConnector) Connect(context.Context) (driver.Conn, error) { return &authConnection{c}, nil }
func (c *authConnector) Driver() driver.Driver                        { return authDriver{c} }

type authDriver struct{ c *authConnector }

func (d authDriver) Open(string) (driver.Conn, error) { return d.c.Connect(context.Background()) }

type authConnection struct{ c *authConnector }

func (*authConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*authConnection) Close() error              { return nil }
func (*authConnection) Begin() (driver.Tx, error) { return authTransaction{}, nil }

type authTransaction struct{}

func (authTransaction) Commit() error   { return nil }
func (authTransaction) Rollback() error { return nil }
func (c *authConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.c.query, c.c.args = query, args
	c.c.queries = append(c.c.queries, query)
	if c.c.err != nil {
		return nil, c.c.err
	}
	return &authRows{columns: c.c.columns, rows: c.c.rows}, nil
}

func (c *authConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.c.query, c.c.args = query, args
	c.c.queries = append(c.c.queries, query)
	if c.c.err != nil {
		return nil, c.c.err
	}
	return driver.RowsAffected(1), nil
}

type authRows struct {
	columns []string
	rows    [][]driver.Value
}

func (r *authRows) Columns() []string { return r.columns }
func (*authRows) Close() error        { return nil }
func (r *authRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

func TestPostgresGORMReads(t *testing.T) {
	capture := &authConnector{}
	pool := sql.OpenDB(capture)
	defer pool.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &postgresStore{db: db}
	ctx := context.Background()
	hash := "x' OR 1=1; --"
	expires := time.Now().Add(time.Minute).UTC()
	capture.columns = []string{"token_hash", "provider", "verifier", "expires_at"}
	capture.rows = [][]driver.Value{{hash, "google", "verifier", expires}}
	got, err := store.ConsumeAttempt(ctx, hash, "google")
	if err != nil || got != (loginAttempt{Provider: "google", Verifier: "verifier", Expires: expires}) {
		t.Fatal("deleted state was not returned", got, err)
	}
	if !strings.HasPrefix(capture.query, "DELETE FROM") || !strings.Contains(capture.query, "RETURNING") ||
		!strings.Contains(capture.query, "expires_at > now()") || strings.Contains(capture.query, hash) {
		t.Fatal("state consumption must be atomic, expiry-checked and parameterized", capture.query)
	}
	if len(capture.args) != 2 || capture.args[0].Value != hash || capture.args[1].Value != "google" {
		t.Fatal("incorrect bound state parameters", capture.args)
	}
	capture.rows = nil
	if _, err := store.ConsumeAttempt(ctx, hash, "google"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing state must be rejected", err)
	}
	capture.columns = []string{"provider", "subject", "display_name", "google_domain", "expires_at", "idle_expires_at", "renew_after"}
	capture.rows = [][]driver.Value{{"google", "42", "Test", "example.com", expires, expires, expires}}
	capture.queries = nil
	a, _, err := store.UseSession(ctx, hash, "", time.Now(), time.Minute, time.Minute)
	if err != nil || !reflect.DeepEqual(a, providerIdentity{Provider: "google", Subject: "42", Name: "Test", GoogleDomain: "example.com"}) {
		t.Fatal("session mapping changed", a, err)
	}
	if len(capture.queries) < 2 || strings.Contains(capture.queries[0], hash) || !strings.Contains(capture.queries[0], "idle_expires_at") {
		t.Fatal("session lookup must bind hash and enforce expiry", capture.queries)
	}
	capture.rows = nil
	if _, _, err := store.UseSession(ctx, hash, "", time.Now(), time.Minute, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing session must retain store error contract", err)
	}
	capture.err = errors.New("database unavailable")
	if _, _, err := store.UseSession(ctx, hash, "", time.Now(), time.Minute, time.Minute); !errors.Is(err, capture.err) {
		t.Fatal("storage error lost", err)
	}
	if _, err := store.ConsumeAttempt(ctx, hash, "google"); !errors.Is(err, capture.err) {
		t.Fatal("storage error lost", err)
	}
}
