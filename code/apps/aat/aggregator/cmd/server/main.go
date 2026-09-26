package main

import (
	"context"
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

	httpkit.Serve("Aggregator", "8083", handler(store, token))
}
