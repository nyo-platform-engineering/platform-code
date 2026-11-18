// Package telemetry configures tracing without coupling application behavior to
// collector availability. Attributes deliberately omit credentials and payloads.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var Propagation = propagation.TraceContext{}

// Init is called once at service startup. An absent endpoint leaves local
// development unexported; configuring OTEL_* enables OTLP HTTP/protobuf.
func Init(ctx context.Context, service string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if os.Getenv("OTEL_SDK_DISABLED") == "true" || (os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "") {
		return noop, nil
	}
	if protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"); protocol != "" && protocol != "http/protobuf" {
		return noop, fmt.Errorf("tracing requires OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf")
	}
	name := os.Getenv("OTEL_SERVICE_NAME")
	if name == "" {
		name = service
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithAttributes(attribute.String("service.name", name)))
	if err != nil {
		return noop, err
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithTimeout(2*time.Second))
	if err != nil {
		return noop, err
	}
	sampler := sdktrace.ParentBased(sdktrace.AlwaysSample())
	switch os.Getenv("OTEL_TRACES_SAMPLER") {
	case "", "parentbased_always_on":
	case "parentbased_traceidratio":
		ratio, err := strconv.ParseFloat(os.Getenv("OTEL_TRACES_SAMPLER_ARG"), 64)
		if err != nil || ratio < 0 || ratio > 1 {
			_ = exporter.Shutdown(ctx)
			return noop, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG must be between 0 and 1")
		}
		sampler = sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	default:
		_ = exporter.Shutdown(ctx)
		return noop, fmt.Errorf("unsupported OTEL_TRACES_SAMPLER")
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSampler(sampler), sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(2*time.Second)))
	otel.SetTracerProvider(provider)
	slog.Info("tracing configured", "service", name, "protocol", "http/protobuf")
	return provider.Shutdown, nil
}

func Start(ctx context.Context, name string, kind trace.SpanKind) (context.Context, trace.Span) {
	return otel.Tracer("aat").Start(ctx, name, trace.WithSpanKind(kind))
}

func Fail(span trace.Span, err error) {
	// Error details can contain SQL or credentials. Record a generic status only.
	if err != nil {
		span.SetStatus(codes.Error, "operation failed")
	}
}

func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

func Extract(ctx context.Context, headers http.Header) context.Context {
	return Propagation.Extract(ctx, propagation.HeaderCarrier(headers))
}
