package dbtrace

import (
	"context"
	"os"
	"strings"
	"testing"

	"aat/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresQueryVisibility(t *testing.T) {
	url := os.Getenv("AAT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AAT_TEST_DATABASE_URL for PostgreSQL span integration tests")
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.Tracer = New()
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = Register(db); err != nil {
		t.Fatal(err)
	}
	ctx, parent := telemetry.Start(context.Background(), "visibility integration", trace.SpanKindInternal)
	defer parent.End()
	// A temporary table stays within this dedicated connection, never app schemas.
	if err = db.WithContext(ctx).Exec("CREATE TEMP TABLE trace_visibility (id int PRIMARY KEY, note text)").Error; err != nil {
		t.Fatal(err)
	}
	secret := "do-not-export-this-payload"
	if err = db.WithContext(ctx).Table("trace_visibility").Create(map[string]any{"id": 1, "note": secret}).Error; err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID   int
		Note string
	}
	if err = db.WithContext(ctx).Table("trace_visibility").Where("id = ?", 1).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("query: %+v %v", rows, err)
	}
	var selected sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), secret) {
				t.Fatal("query value exposed", attr)
			}
		}
		if span.Name() == "SELECT trace_visibility" {
			selected = span
		}
	}
	if selected == nil {
		t.Fatal("missing named query span")
	}
	attrs := attributesMap(selected.Attributes())
	if attrs["db.collection.name"] != "trace_visibility" || attrs["db.namespace"] != config.Database || attrs["server.address"] != config.Host || attrs["server.port"] != int64(config.Port) || attrs["db.operation.name"] != "SELECT" || attrs["db.response.returned_rows"] != int64(1) {
		t.Fatalf("metadata: %+v", attrs)
	}
	if selected.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("query parent lost")
	}
	if text, ok := attrs["db.query.text"].(string); !ok || !strings.Contains(text, "$1") {
		t.Fatalf("parameterized SQL missing: %+v", attrs)
	}
}
