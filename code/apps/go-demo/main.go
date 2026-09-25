package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func newHandler(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{
			"service": "go-demo",
			"message": "Hello from Go!",
		}); err != nil {
			logger.Error("write response", "error", err)
		}
	})
	for _, path := range []string{"/healthz", "/readyz"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "ok")
		})
	}
	registerDemo(mux, logger)
	return instrumentRequests(mux, logger)
}

func run(ctx context.Context, logger *slog.Logger) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("invalid PORT %q: expected 1-65535", port)
	}
	server := &http.Server{
		Addr:              net.JoinHostPort(os.Getenv("LISTEN_HOST"), port),
		Handler:           newHandler(logger),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("starting server", "address", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErrors:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-serverErrors
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger, shutdown, err := setupTelemetry(ctx, logger)
	if err != nil {
		logger.Error("telemetry setup failed", "error", err)
		os.Exit(1)
	}
	defer shutdown()
	if err := run(ctx, logger); err != nil {
		logger.Error("server stopped", "error", err)
		shutdown()
		os.Exit(1)
	}
}
