package main

import (
	"aat/client/internal/api"
	"aat/internal/httpkit"
	"time"
)

func main() {
	handler, err := api.Handler(api.Config{
		AuthURL:         httpkit.Env("AUTH_URL", "http://auth:8084"),
		AggregatorURL:   httpkit.Env("AGGREGATOR_URL", "http://aggregator:8083"),
		AuthToken:       httpkit.Secret("AUTH_INTERNAL_TOKEN"),
		AggregatorToken: httpkit.Secret("AGGREGATOR_TOKEN"),
		MaxConcurrent:   httpkit.Integer("CLIENT_MAX_CONCURRENT", 16),
		Timeout:         time.Duration(httpkit.Integer("UPSTREAM_TIMEOUT_MS", 3000)) * time.Millisecond,
		StaleAfter:      time.Duration(httpkit.Integer("STALE_AFTER_SECONDS", 60)) * time.Second,
	})
	if err != nil {
		panic(err)
	}
	httpkit.Serve("Client API", "8080", handler)
}
