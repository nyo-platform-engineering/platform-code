package telemetry

import (
	"io"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, span := Start(request.Context(), request.Method+" "+request.URL.Path, trace.SpanKindClient)
	span.SetAttributes(attribute.String("http.request.method", request.Method), attribute.String("server.address", request.URL.Hostname()))
	clone := request.Clone(ctx)
	Propagation.Inject(ctx, propagation.HeaderCarrier(clone.Header))
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(clone)
	if err != nil {
		Fail(span, err)
		span.End()
		return response, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.StatusCode >= 400 {
		span.SetStatus(codes.Error, "upstream request failed")
	}
	if response.Body == nil {
		span.End()
	} else {
		response.Body = &tracedBody{ReadCloser: response.Body, span: span}
	}
	return response, nil
}

type tracedBody struct {
	io.ReadCloser
	span trace.Span
	once sync.Once
}

func (b *tracedBody) end() { b.once.Do(func() { b.span.End() }) }
func (b *tracedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		if err != io.EOF {
			Fail(b.span, err)
		}
		b.end()
	}
	return n, err
}
func (b *tracedBody) Close() error {
	err := b.ReadCloser.Close()
	Fail(b.span, err)
	b.end()
	return err
}
