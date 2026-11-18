package eventbus

import (
	"aat/internal/telemetry"
	"context"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"testing"
)

func TestNATSTraceContextRoundTrip(t *testing.T) {
	const parent = "00-11223344556677889900aabbccddeeff-1122334455667788-01"
	ctx := telemetry.Propagation.Extract(context.Background(), propagation.MapCarrier{"traceparent": parent, "tracestate": "aat=verification"})
	headers := nats.Header{}
	telemetry.Propagation.Inject(ctx, HeaderCarrier(headers))
	if headers.Get("traceparent") != parent {
		t.Fatal("traceparent not injected")
	}
	// NATS preserves casing when decoding wire headers, unlike net/http.
	for _, key := range []string{"traceparent", "Traceparent", "TraceParent"} {
		wire := []byte("NATS/1.0\r\n" + key + ": " + headers.Get("traceparent") + "\r\ntracestate: " + headers.Get("tracestate") + "\r\n\r\n")
		received, err := nats.DecodeHeadersMsg(wire)
		if err != nil {
			t.Fatal(err)
		}
		extracted := telemetry.Propagation.Extract(context.Background(), HeaderCarrier(received))
		span := trace.SpanContextFromContext(extracted)
		if span.TraceID().String() != "11223344556677889900aabbccddeeff" || span.SpanID().String() != "1122334455667788" || !span.IsSampled() || span.TraceState().String() != "aat=verification" {
			t.Fatalf("%s: trace context lost: %+v", key, span)
		}
	}
}
