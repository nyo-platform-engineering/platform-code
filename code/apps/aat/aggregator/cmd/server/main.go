package main

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"aat/aggregator/internal/aggregate"
	"aat/internal/httpkit"
)

func main() {
	token := httpkit.Secret("AGGREGATOR_TOKEN")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, e := pgxpool.ParseConfig(httpkit.Secret("DATABASE_URL"))
	if e != nil {
		panic("invalid DATABASE_URL")
	}

	config.MaxConns = int32(httpkit.Integer("DB_MAX_CONNS", 5))
	pool, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		panic(e)
	}

	defer pool.Close()
	store := aggregate.Store{Pool: pool}
	if e = store.Init(ctx); e != nil {
		panic(e)
	}
	pollInterval := time.Duration(httpkit.Integer("POLL_INTERVAL_SECONDS", 3)) * time.Second
	if pollInterval < time.Second {
		panic("POLL_INTERVAL_SECONDS must be positive")
	}
	pollClient := &http.Client{Timeout: time.Duration(httpkit.Integer("POLL_TIMEOUT_MS", 4000)) * time.Millisecond}
	if pollClient.Timeout <= 0 {
		panic("POLL_TIMEOUT_MS must be positive")
	}
	go newSourcePoller("BMKG", httpkit.Env("BMKG_URL", "http://bmkg:8081"), "X-BMKG-Key", httpkit.Secret("BMKG_API_KEY"), []pollEndpoint{
		{path: "/seismic-events", key: "seismic_events"},
		{path: "/tsunami-warnings", key: "tsunami_warnings"},
	}, store, pollClient).Run(context.Background(), pollInterval)
	go newSourcePoller("PVMBG", httpkit.Env("PVMBG_URL", "http://pvmbg:8082"), "Authorization", "Bearer "+httpkit.Secret("PVMBG_TOKEN"), []pollEndpoint{
		{path: "/volcanic-reports", key: "volcanic_reports"},
	}, store, pollClient).Run(context.Background(), pollInterval)

	httpkit.Serve("Aggregator", "8083", handler(store, token))
}
