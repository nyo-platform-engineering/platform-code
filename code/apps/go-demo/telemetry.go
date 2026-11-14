package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type dualHandler struct{ stdout, otlp slog.Handler }

func (h dualHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.stdout.Enabled(ctx, level) || h.otlp.Enabled(ctx, level)
}
func (h dualHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, handler := range []slog.Handler{h.stdout, h.otlp} {
		if handler.Enabled(ctx, r.Level) {
			errs = append(errs, handler.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}
func (h dualHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return dualHandler{h.stdout.WithAttrs(a), h.otlp.WithAttrs(a)}
}
func (h dualHandler) WithGroup(g string) slog.Handler {
	return dualHandler{h.stdout.WithGroup(g), h.otlp.WithGroup(g)}
}

func setupTelemetry(ctx context.Context, logger *slog.Logger) (*slog.Logger, func(), error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return logger, func() {}, nil
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", "go-demo")), resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return nil, nil, err
	}
	te, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, nil, err
	}
	le, err := otlploghttp.New(ctx)
	if err != nil {
		_ = te.Shutdown(ctx)
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithBatcher(te, sdktrace.WithBatchTimeout(time.Second)))
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(le, sdklog.WithExportInterval(time.Second))))
	otel.SetTracerProvider(tp)
	combined := slog.New(dualHandler{logger.Handler(), otelslog.NewHandler("go-demo", otelslog.WithLoggerProvider(lp))})
	return combined, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := errors.Join(tp.Shutdown(shutdownCtx), lp.Shutdown(shutdownCtx)); err != nil {
			logger.Error("telemetry shutdown", "error", err)
		}
	}, nil
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func instrumentRequests(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		// Local demo requests start server-owned traces, without trusting public headers.
		ctx, span := otel.Tracer("go-demo").Start(r.Context(), "HTTP "+r.Method, trace.WithSpanKind(trace.SpanKindServer), trace.WithNewRoot())
		defer span.End()
		request := r.WithContext(ctx)
		out := &statusWriter{ResponseWriter: w}
		w.Header().Set("X-Trace-ID", span.SpanContext().TraceID().String())
		start := time.Now()
		next.ServeHTTP(out, request)
		if out.status == 0 {
			out.status = 200
		}
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		span.SetName(route)
		span.SetAttributes(attribute.String("http.request.method", r.Method), attribute.String("http.route", route), attribute.Int("http.response.status_code", out.status))
		if out.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(out.status))
		}
		logger.InfoContext(ctx, "http request", "route", route, "status", out.status, "duration_ms", time.Since(start).Milliseconds(), "trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	})
}
