package main

import (
	"time"

	"aat/bmkg/internal/mock"
	"aat/internal/httpkit"
)

func main() {
	interval := httpkit.Integer("EVENT_INTERVAL_SECONDS", 10)
	delay := httpkit.Integer("BMKG_DELAY_MS", 100)
	if interval < 1 || interval > 10 || delay < 50 || delay > 150 {
		panic("invalid interval (1..10s) or delay (50..150ms)")
	}

	httpkit.Serve(
		"BMKG",
		"8081",
		mock.NewHandler(httpkit.Secret("BMKG_API_KEY"), time.Duration(interval)*time.Second, time.Duration(delay)*time.Millisecond),
	)
}
