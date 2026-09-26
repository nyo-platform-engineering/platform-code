package main

import (
	"errors"
	"log/slog"
	"net/http"

	"aat/aggregator/internal/aggregate"
	"aat/internal/httpkit"
)

func fail(w http.ResponseWriter, r *http.Request, e error) {
	var invalid aggregate.Invalid
	if errors.As(e, &invalid) {
		httpkit.JSON(w, 400, map[string]string{"error": invalid.Error()})
		return
	}

	slog.Error("store operation failed", "correlation_id", r.Header.Get("X-Correlation-ID"), "error", e)
	httpkit.JSON(w, 503, map[string]string{"error": "store unavailable"})
}
