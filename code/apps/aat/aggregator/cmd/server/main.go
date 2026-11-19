package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
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
	workerCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	token := httpkit.Secret("AGGREGATOR_TOKEN")
	startupCtx, cancel := context.WithTimeout(workerCtx, 90*time.Second)
	defer cancel()
	startupCtx = httpkit.WithCorrelationID(startupCtx, httpkit.ID())

	databaseURL := httpkit.Secret("DATABASE_URL")
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		panic("invalid DATABASE_URL")
	}
	config.Tracer = dbtrace.New()
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	maxConns := httpkit.Integer("DB_MAX_CONNS", 5)
	if maxConns < 3 {
		panic("DB_MAX_CONNS must be at least 3: two polling sessions plus API connections")
	}
	sqlDB.SetMaxOpenConns(maxConns)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		panic(err)
	}

	if err := dbtrace.Register(db); err != nil {
		panic(err)
	}

	auditToken := os.Getenv("AUDIT_TOKEN")
	if auditToken != "" && (auditToken == token || auditToken == os.Getenv("BMKG_API_KEY") || auditToken == os.Getenv("PVMBG_TOKEN")) {
		panic("AUDIT_TOKEN must differ from application tokens")
	}
	ctrl := &controller.Controller{DB: db, Store: storage.NewPostgres(db, storage.Identity{
		Node: os.Getenv("STORE_NODE_NAME"), Unit: httpkit.Env("STORE_UNIT", "postgres"),
	})}
	schemaPolicy := httpkit.Env("PVMBG_SCHEMA_POLICY", "strict")
	if schemaPolicy != "strict" && schemaPolicy != "v2" {
		panic("PVMBG_SCHEMA_POLICY must be strict or v2")
	}
	ctrl.AcceptPVMBGV2 = schemaPolicy == "v2"
	lateness := httpkit.Integer("SATELLITE_ALLOWED_LATENESS_MINUTES", int(controller.DefaultSatelliteLateness/time.Minute))
	if lateness < 1 {
		panic("SATELLITE_ALLOWED_LATENESS_MINUTES must be positive")
	}
	ctrl.SatelliteLateness = time.Duration(lateness) * time.Minute
	if err := waitForDatabase(startupCtx, ctrl.Ping); err != nil {
		panic(err)
	}
	if err := model.Migrate(startupCtx, db); err != nil {
		panic(err)
	}

	if os.Getenv("STORE_BACKEND") == "citus" {
		members, err := storage.ReadCitusMembers(os.Getenv("CITUS_MEMBERS_FILE"))
		if err != nil {
			panic(err)
		}
		if err := storage.EnableCitus(startupCtx, db); err != nil {
			panic(err)
		}
		ctrl.Store = storage.NewCitus(db, members)
	} else if mode := os.Getenv("STORE_BACKEND"); mode != "" && mode != "postgres" {
		panic("STORE_BACKEND must be postgres or citus")
	}

	pollInterval := time.Duration(httpkit.Integer("POLL_INTERVAL_SECONDS", 3)) * time.Second
	if pollInterval < time.Second || pollInterval >= controller.PollSessionIdleTimeout/2 {
		panic("POLL_INTERVAL_SECONDS must be between 1 and 9")
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

	// The satellite consumer runs only when a feed token is configured.
	satelliteToken := os.Getenv("SATELLITE_GROUP_TOKEN")
	sensorToken := os.Getenv("PANTAI_AUTH_TOKEN")
	// One connection per owned source session (BMKG, PVMBG, satellite, sensor) plus the API.
	need := 3
	if satelliteToken != "" {
		need++
	}
	if sensorToken != "" {
		need++
	}
	if maxConns < need+1 {
		panic(fmt.Sprintf("DB_MAX_CONNS must be at least %d: one per owned source session plus API connections", need+1))
	}
	// Reserve source sessions and one connection for health and background work.
	ctrl.ReadConcurrency = maxConns - need

	natsURL := httpkit.Env("NATS_URL", "nats://nats:4222")
	var workers sync.WaitGroup
	workers.Go(func() { bmkgPoller.Run(workerCtx, pollInterval) })
	workers.Go(func() { pvmbgPoller.Run(workerCtx, pollInterval) })
	if satelliteToken != "" {
		consumer := newSatelliteConsumer(httpkit.Env("SATELLITE_FEED_URL", "https://satellite.naufarrel.dev"), satelliteToken, ctrl)
		consumer.batchSize = httpkit.Integer("SATELLITE_BATCH_SIZE", consumer.batchSize)
		if consumer.batchSize < 1 || consumer.batchSize > 1000 {
			panic("SATELLITE_BATCH_SIZE must be between 1 and 1000")
		}
		workers.Go(func() { consumer.Run(workerCtx) })
	} else {
		slog.Warn("satellite feed disabled: SATELLITE_GROUP_TOKEN is empty")
	}
	// Sensor Pantai runs only when its token is configured.
	if sensorToken != "" {
		sensor := newSensorPoller(httpkit.Env("SENSOR_PANTAI_URL", "http://sensor-pantai:8080"), sensorToken, ctrl,
			time.Duration(httpkit.Integer("SENSOR_TIMEOUT_MS", 2000))*time.Millisecond)
		sensor.interval = time.Duration(httpkit.Integer("SENSOR_POLL_INTERVAL_MS", 1000)) * time.Millisecond
		sensor.staleAfter = time.Duration(httpkit.Integer("SENSOR_STALE_AFTER_SECONDS", 900)) * time.Second
		if sensor.interval < 100*time.Millisecond || sensor.interval >= controller.PollSessionIdleTimeout/2 || sensor.staleAfter <= 0 {
			panic("SENSOR_POLL_INTERVAL_MS must be 100..9999 and SENSOR_STALE_AFTER_SECONDS positive")
		}
		workers.Go(func() { sensor.Run(workerCtx) })
	} else {
		slog.Warn("sensor pantai disabled: PANTAI_AUTH_TOKEN is empty")
	}
	workers.Go(func() { runOutboxRelay(workerCtx, ctrl, natsURL) })
	processReports, err := strconv.ParseBool(httpkit.Env("FIELD_REPORT_PROCESSOR_ENABLED", "true"))
	if err != nil {
		panic("FIELD_REPORT_PROCESSOR_ENABLED must be true or false")
	}
	if processReports {
		workers.Go(func() { runFieldReportProcessor(workerCtx, ctrl) })
	}

	router := internal.NewHandlerWithAudit(ctrl, token, auditToken)
	httpkit.ServeContext(workerCtx, "Aggregator", "8083", router)
	stop()
	workers.Wait()
}
