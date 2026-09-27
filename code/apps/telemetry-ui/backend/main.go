package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	logmodel "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	tracemodel "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/policy"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/routes"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const version = "0.1.0"

type config struct {
	Mock                    bool
	Port                    int
	ListenHost              string
	TraceStore              model.QueryStore
	LogStore                model.QueryStore
	WebDistDir              string
	AuthMode                string
	Authenticator           auth.Authenticator
	BackendOTLPEnabled      bool
	TelemetryShutdownPeriod time.Duration
}

func loadConfig() (config, error) {
	port := 8080
	if value := os.Getenv("PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			return config{}, fmt.Errorf("invalid PORT %q: expected 1-65535", value)
		}
		port = parsed
	}

	mockEnabled, err := strconv.ParseBool(envOr("MOCK", "false"))
	if err != nil {
		return config{}, fmt.Errorf("invalid MOCK: expected a boolean")
	}

	authMode := envOr("AUTH_MODE", "local")
	if authMode != "local" {
		return config{}, fmt.Errorf("unsupported AUTH_MODE %q: only local is available in the scaffold", authMode)
	}

	return config{
		Mock:                    mockEnabled,
		Port:                    port,
		ListenHost:              os.Getenv("LISTEN_HOST"),
		WebDistDir:              envOr("WEB_DIST_DIR", "frontend/dist"),
		AuthMode:                authMode,
		BackendOTLPEnabled:      !mockEnabled && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "",
		TelemetryShutdownPeriod: 5 * time.Second,
	}, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newFrontendHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := strings.TrimPrefix(filepath.Clean(r.URL.Path), string(filepath.Separator))
		if cleanPath == "." {
			cleanPath = ""
		}
		candidate := filepath.Join(root, cleanPath)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		index := filepath.Join(root, "index.html")
		if _, err := os.Stat(index); errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "frontend_not_built"})
			return
		}
		http.ServeFile(w, r, index)
	})
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	}
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		spanContext := trace.SpanContextFromContext(c.Request.Context())
		logger.InfoContext(c.Request.Context(), "http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"route", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
			"trace_id", spanContext.TraceID().String(),
			"span_id", spanContext.SpanID().String(),
		)
	}
}

func newTracerProvider(ctx context.Context, cfg config) (*sdktrace.TracerProvider, error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", "telemetry-ui-api"),
			attribute.String("service.version", version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(1))),
	}
	if cfg.BackendOTLPEnabled {
		exporter, exporterErr := otlptracehttp.New(ctx)
		if exporterErr != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", exporterErr)
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	}

	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider, nil
}

func newHandler(cfg config, logger *slog.Logger) (http.Handler, error) {
	if cfg.AuthMode != "" && cfg.AuthMode != "local" {
		return nil, fmt.Errorf("unsupported AUTH_MODE %q", cfg.AuthMode)
	}
	if cfg.Authenticator == nil {
		cfg.Authenticator = auth.LocalAuthenticator{}
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(
		gin.CustomRecovery(func(c *gin.Context, recovered any) {
			logger.ErrorContext(c.Request.Context(), "panic recovered", "error", recovered)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal_server_error"})
		}),
		securityHeaders(),
		otelgin.Middleware(
			"telemetry-ui-api",
			// Public clients must not choose trace IDs, parent spans, or baggage.
			// Restore trusted propagation only after an authenticated boundary exists.
			otelgin.WithPropagators(propagation.NewCompositeTextMapPropagator()),
			otelgin.WithFilter(func(r *http.Request) bool {
				return strings.HasPrefix(r.URL.Path, "/api/")
			}),
		),
		requestLogger(logger),
	)

	router.Use(policy.Enforce(cfg.Authenticator))
	routes.Register(router, cfg.TraceStore, cfg.LogStore, newFrontendHandler(cfg.WebDistDir), version)
	if err := policy.ValidateRoutes(router.Routes()); err != nil {
		return nil, err
	}
	return router, nil
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	if cfg.Mock {
		cfg.TraceStore, cfg.LogStore = tracemodel.MockStore{}, logmodel.MockStore{}
		cfg.BackendOTLPEnabled = false
		logger.Warn("MOCK enabled: serving synthetic telemetry without database connections")
	} else {
		traceStore, logStore := model.OpenStores()
		defer traceStore.Close()
		if logStore != traceStore {
			defer logStore.Close()
		}
		cfg.TraceStore, cfg.LogStore = traceStore, logStore
	}
	provider, err := newTracerProvider(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.TelemetryShutdownPeriod)
		defer cancel()
		if shutdownErr := provider.Shutdown(shutdownCtx); shutdownErr != nil {
			logger.Error("telemetry shutdown failed", "error", shutdownErr)
		}
	}()

	handler, err := newHandler(cfg, logger)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("starting telemetry UI", "address", server.Addr, "auth_mode", cfg.AuthMode)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case serverErr := <-serverErrors:
		if errors.Is(serverErr, http.ErrServerClosed) {
			return nil
		}
		return serverErr
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			_ = server.Close()
			return shutdownErr
		}
		return nil
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
