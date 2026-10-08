package main

import (
	"time"

	"aat/internal/httpkit"
	"aat/pvmbg/internal/mock"
)

func main() {
	start, err := time.Parse(time.RFC3339Nano, httpkit.Secret("START_TIME"))
	if err != nil {
		panic("START_TIME must be an RFC3339 timestamp: " + err.Error())
	}
	interval := httpkit.Integer("EVENT_INTERVAL_SECONDS", 10)
	min := httpkit.Integer("PVMBG_DELAY_MIN_MS", 500)
	max := httpkit.Integer("PVMBG_DELAY_MAX_MS", 3000)
	if interval < 1 || interval > 10 || min < 0 || max < min || max > 10000 {
		panic("invalid interval (1..10s) or delay (0..10000ms)")
	}

	httpkit.Serve(
		"PVMBG",
		"8082",
		mock.NewHandler(
			httpkit.Secret("PVMBG_TOKEN"),
			start,
			time.Duration(interval)*time.Second,
			time.Duration(min)*time.Millisecond,
			time.Duration(max)*time.Millisecond,
		),
	)
}
