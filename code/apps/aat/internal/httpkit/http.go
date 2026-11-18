// Package httpkit contains transport helpers only, never shared business logic.
package httpkit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"aat/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/gin-gonic/gin"
)

type correlationKey struct{}

func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

func Secret(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(key + " is required")
	}

	return v
}

func Integer(key string, fallback int) int {
	v, e := strconv.Atoi(Env(key, strconv.Itoa(fallback)))
	if e != nil {
		panic(e)
	}

	return v
}

func ID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}

	return hex.EncodeToString(b[:])
}

func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func Decode(w http.ResponseWriter, r *http.Request, body any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	if e := d.Decode(body); e != nil {
		return e
	}

	var rest any
	if e := d.Decode(&rest); e != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}

	return nil
}

// Router supplies the same recovery and routing behavior to every service.
func Router() *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.HandleMethodNotAllowed = true
	router.RedirectTrailingSlash = false
	return router
}

func Auth(header, value string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if subtle.ConstantTimeCompare([]byte(c.GetHeader(header)), []byte(value)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		c.Next()
	}
}

type response struct {
	http.ResponseWriter
	status int
}

func (w *response) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func Observe(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, span := telemetry.Start(telemetry.Extract(r.Context(), r.Header), r.Method+" "+r.URL.Path, trace.SpanKindServer)
		defer span.End()
		r = r.WithContext(ctx)
		span.SetAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.path", r.URL.Path))
		if id := telemetry.TraceID(ctx); id != "" {
			w.Header().Set("X-Trace-ID", id)
		}
		id := r.Header.Get("X-Correlation-ID")
		if id == "" {
			id = ID()
		}

		r.Header.Set("X-Correlation-ID", id)
		r = r.WithContext(WithCorrelationID(r.Context(), id))
		w.Header().Set("X-Correlation-ID", id)
		rw := &response{w, 200}
		next.ServeHTTP(rw, r)
		span.SetAttributes(attribute.Int("http.response.status_code", rw.status))
		if rw.status >= 500 {
			span.SetStatus(codes.Error, "request failed")
		}
		slog.Info(
			"request",
			"trace_id", telemetry.TraceID(r.Context()),
			"service",
			service,
			"correlation_id",
			id,
			"method",
			r.Method,
			"path",
			r.URL.Path,
			"status",
			rw.status,
			"latency_ms",
			time.Since(start).Milliseconds(),
		)
	})
}

func Since(r *http.Request) (time.Time, error) {
	v := r.URL.Query().Get("since")
	if v == "" {
		return time.Time{}, nil
	}

	return time.Parse(time.RFC3339Nano, v)
}

func Sleep(r *http.Request, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func Serve(service, port string, h http.Handler) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ServeContext(ctx, service, port, h)
}

// ServeContext waits for active HTTP requests to finish when ctx is canceled.
func ServeContext(ctx context.Context, service, port string, h http.Handler) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	shutdown, err := telemetry.Init(context.Background(), service)
	if err != nil {
		panic(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			slog.Warn("trace shutdown failed")
		}
	}()
	s := http.Server{
		Addr:              ":" + Env("PORT", port),
		Handler:           Observe(service, h),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	slog.Info("listening", "service", service, "address", s.Addr)
	result := make(chan error, 1)
	go func() { result <- s.ListenAndServe() }()
	select {
	case err := <-result:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP drain failed", "service", service, "error", err)
			_ = s.Close()
		}
		if err := <-result; err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	}
}
