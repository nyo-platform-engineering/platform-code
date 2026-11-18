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
	"os/signal"
	"syscall"

	"aat/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"os"
	"strconv"
	"time"

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	s := http.Server{
		Addr:              ":" + Env("PORT", port),
		Handler:           Observe(service, h),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(drain)
	}()
	slog.Info("listening", "service", service, "address", s.Addr)
	if e := s.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		panic(e)
	}
	<-drained
}
