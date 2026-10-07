package main

import (
	"context"
	"net/http"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"aat/internal/httpkit"
)

func main() {
	token := httpkit.Secret("AGGREGATOR_TOKEN")
	startupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Connect to PostgreSQL and limit the number of open connections.
	databaseURL := httpkit.Secret("DATABASE_URL")
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		panic(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		panic(err)
	}
	sqlDB.SetMaxOpenConns(httpkit.Integer("DB_MAX_CONNS", 5))
	defer sqlDB.Close()

	// Check the connection and create or update the model tables.
	ctrl := &controller.Controller{DB: db}
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
	pollClient := &http.Client{Timeout: pollTimeout}

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

	router := controller.NewHandler(ctrl, token)
	httpkit.Serve("Aggregator", "8083", router)
}
