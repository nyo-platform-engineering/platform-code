package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"aat/aggregator/internal"
	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/dbtrace"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"aat/internal/httpkit"
	"aat/internal/telemetry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	token := httpkit.Secret("AGGREGATOR_TOKEN")
	startupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	startupCtx = httpkit.WithCorrelationID(startupCtx, httpkit.ID())

	// Connect to PostgreSQL and limit the number of open connections.
	databaseURL := httpkit.Secret("DATABASE_URL")
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		panic("invalid DATABASE_URL")
	}
	config.Tracer = dbtrace.New()
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(httpkit.Integer("DB_MAX_CONNS", 5))
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		panic(err)
	}

	// Check the connection and create or update the model tables.
	auditToken := os.Getenv("AUDIT_TOKEN")
	if auditToken != "" && (auditToken == token || auditToken == os.Getenv("BMKG_API_KEY") || auditToken == os.Getenv("PVMBG_TOKEN")) {
		panic("AUDIT_TOKEN must differ from application tokens")
	}
	ctrl := &controller.Controller{DB: db, Store: storage.NewPostgres(db, storage.Identity{
		Node: os.Getenv("STORE_NODE_NAME"), Unit: httpkit.Env("STORE_UNIT", "postgres"),
	})}
	lateness := httpkit.Integer("SATELLITE_ALLOWED_LATENESS_MINUTES", int(controller.DefaultSatelliteLateness/time.Minute))
	if lateness < 1 {
		panic("SATELLITE_ALLOWED_LATENESS_MINUTES must be positive")
	}
	ctrl.SatelliteLateness = time.Duration(lateness) * time.Minute
	if err := ctrl.Ping(startupCtx); err != nil {
		panic(err)
	}
	if err := model.Migrate(startupCtx, db); err != nil {
		panic(err)
	}

	// Both sources share the polling interval and HTTP timeout.
	pollInterval := time.Duration(httpkit.Integer("POLL_INTERVAL_SECONDS", 3)) * time.Second
	if pollInterval < time.Second {
		panic("POLL_INTERVAL_SECONDS must be positive")
	}
	pollTimeout := time.Duration(httpkit.Integer("POLL_TIMEOUT_MS", 4000)) * time.Millisecond
	if pollTimeout <= 0 {
		panic("POLL_TIMEOUT_MS must be positive")
	}
	pollClient := &http.Client{Timeout: pollTimeout, Transport: telemetry.Transport{}}

	bmkgURL := httpkit.Env("BMKG_URL", "http://bmkg:8081")
	bmkgKey := httpkit.Secret("BMKG_API_KEY")
	bmkgEndpoints := []pollEndpoint{
		{path: "/seismic-events", key: "seismic_events"},
		{path: "/tsunami-warnings", key: "tsunami_warnings"},
	}
	bmkgPoller := newSourcePoller(
		"BMKG", bmkgURL, "X-BMKG-Key", bmkgKey,
		bmkgEndpoints, ctrl, pollClient,
	)

	pvmbgURL := httpkit.Env("PVMBG_URL", "http://pvmbg:8082")
	pvmbgToken := "Bearer " + httpkit.Secret("PVMBG_TOKEN")
	pvmbgEndpoints := []pollEndpoint{
		{path: "/volcanic-reports", key: "volcanic_reports"},
	}
	pvmbgPoller := newSourcePoller(
		"PVMBG", pvmbgURL, "Authorization", pvmbgToken,
		pvmbgEndpoints, ctrl, pollClient,
	)

	// Poll each source and publish queued events in the background.
	workerCtx := context.Background()
	natsURL := httpkit.Env("NATS_URL", "nats://nats:4222")
	go bmkgPoller.Run(workerCtx, pollInterval)
	go pvmbgPoller.Run(workerCtx, pollInterval)
	go runOutboxRelay(workerCtx, ctrl, natsURL)

	router := internal.NewHandlerWithAudit(ctrl, token, auditToken)
	httpkit.Serve("Aggregator", "8083", router)
}
