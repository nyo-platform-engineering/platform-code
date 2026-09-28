package queryinternal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Exercise GORM's real SQL execution and scanning without a ClickHouse server.
type captureConnector struct {
	sql  string
	args []driver.NamedValue
	rows [][]driver.Value
	err  error
}

func (c *captureConnector) Connect(context.Context) (driver.Conn, error) {
	return &captureConnection{c}, nil
}
func (c *captureConnector) Driver() driver.Driver { return captureDriver{c} }

type captureDriver struct{ c *captureConnector }

func (d captureDriver) Open(string) (driver.Conn, error) { return d.c.Connect(context.Background()) }

type captureConnection struct{ c *captureConnector }

func (*captureConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*captureConnection) Close() error { return nil }
func (*captureConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *captureConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.c.sql, c.c.args = query, args
	if c.c.err != nil {
		return nil, c.c.err
	}
	return &captureRows{rows: c.c.rows}, nil
}

type captureRows struct {
	rows   [][]driver.Value
	offset int
}

func (*captureRows) Columns() []string {
	return []string{"timestamp", "attributes", "durationMs", "optional"}
}
func (*captureRows) Close() error { return nil }
func (r *captureRows) Next(dest []driver.Value) error {
	if r.offset == len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.offset])
	r.offset++
	return nil
}
func (*captureRows) ColumnTypeScanType(index int) reflect.Type {
	return []reflect.Type{reflect.TypeOf(time.Time{}), reflect.TypeOf(map[string]string{}), reflect.TypeOf(float64(0)), reflect.TypeOf(float64(0))}[index]
}

func TestGORMExecutionBindsValuesAndScansClickHouseTypes(t *testing.T) {
	timestamp := time.Date(2026, 9, 27, 12, 0, 0, 123456789, time.UTC)
	attributes := map[string]string{"key": "value"}
	capture := &captureConnector{rows: [][]driver.Value{{timestamp, attributes, float64(42.5), nil}}}
	pool := sql.OpenDB(capture)
	defer pool.Close()
	orm, err := openGORM(pool, false)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: pool, orm: orm}
	attack := "x' OR 1=1; -- ?"
	compiled, err := Compile(Request{Signal: SignalTraces, Operation: OperationRecords, OrganizationScope: attack, Filter: Filter{From: timestamp.Add(-time.Hour), To: timestamp, Search: attack, Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	q := compiled[0]
	rows, err := Execute(context.Background(), store, q)
	if err != nil {
		t.Fatal(err)
	}
	if capture.sql != q.SQL || len(capture.args) != len(q.Args) {
		t.Fatal("execution diverged from preview")
	}
	for i, arg := range capture.args {
		expected, err := driver.DefaultParameterConverter.ConvertValue(q.Args[i])
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(arg.Value, expected) {
			t.Fatalf("argument %d changed: %#v vs %#v", i, arg.Value, expected)
		}
	}
	expected := []map[string]any{{"timestamp": timestamp, "attributes": attributes, "durationMs": float64(42.5), "optional": nil}}
	if !reflect.DeepEqual(rows, expected) {
		t.Fatalf("result types changed: %#v", rows)
	}
	capture.rows = nil
	rows, err = Execute(context.Background(), store, q)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty results must remain []: %#v %v", rows, err)
	}
	capture.err = errors.New("database unavailable")
	if _, err := Execute(context.Background(), store, q); !errors.Is(err, capture.err) {
		t.Fatal("database error lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Execute(ctx, store, q); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestGORMCompilationDoesNotShareRequestState(t *testing.T) {
	var wg sync.WaitGroup
	for _, tenant := range []string{"tenant-a", "tenant-b", "tenant-c"} {
		wg.Add(1)
		go func(tenant string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				queries, err := Compile(Request{Signal: SignalLogs, Operation: OperationRecords, OrganizationScope: tenant, Filter: Filter{Limit: 3}})
				if err != nil {
					t.Error(err)
					return
				}
				if queries[0].Args[2] != tenant {
					t.Error("tenant leaked between statements")
				}
			}
		}(tenant)
	}
	wg.Wait()
}
