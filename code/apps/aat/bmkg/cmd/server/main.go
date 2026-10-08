package main

import (
	"time"

	"aat/bmkg/internal/mock"
	"aat/internal/httpkit"
)

func main() {
	start, err := time.Parse(time.RFC3339Nano, httpkit.Secret("START_TIME"))
	if err != nil {
		panic("START_TIME must be an RFC3339 timestamp: " + err.Error())
	}
	interval := httpkit.Integer("EVENT_INTERVAL_SECONDS", 10)
	delay := httpkit.Integer("BMKG_DELAY_MS", 100)
	if interval < 1 || interval > 10 || delay < 50 || delay > 150 {
		panic("invalid interval (1..10s) or delay (50..150ms)")
	}

	httpkit.Serve(
		"BMKG",
		"8081",
		mock.NewHandler(httpkit.Secret("BMKG_API_KEY"), start, time.Duration(interval)*time.Second, time.Duration(delay)*time.Millisecond),
	)
}
