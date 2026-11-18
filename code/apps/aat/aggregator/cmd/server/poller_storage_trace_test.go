package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	aggregator "aat/aggregator/internal"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"aat/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestOwnedPollPreservesStorageAndTracing(t *testing.T) {
	ctrl := pollController(t)
	ctrl.Store = storage.NewPostgres(ctrl.DB, storage.Identity{Node: "db-worker", Unit: "canonical"})
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = provider.Shutdown(context.Background()) }()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("traceparent") == "" || r.Header.Get("X-Poll-Owner") == "" {
			t.Error("missing trace or ownership headers")
		}
		_, _ = w.Write([]byte("[" + m2VolcanicReport + "]"))
	}))
	defer upstream.Close()
	client := &http.Client{Transport: telemetry.Transport{Base: upstream.Client().Transport}}
	p := newSourcePoller("PVMBG", upstream.URL, "Authorization", "Bearer test",
		[]pollEndpoint{{"/volcanic-reports", "volcanic_reports"}}, ctrl, client)
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var poll, ingest sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case "poll PVMBG":
			poll = span
		case "ingest PVMBG":
			ingest = span
		}
	}
	if poll == nil || ingest == nil || ingest.Parent().SpanID() != poll.SpanContext().SpanID() {
		t.Fatal("poll and ingestion trace disconnected")
	}
	queryTraced := false
	for _, span := range recorder.Ended() {
		for _, attr := range span.Attributes() {
			if attr.Key == "db.collection.name" && attr.Value.AsString() == "hazard_events" && span.Parent().SpanID() == ingest.SpanContext().SpanID() {
				queryTraced = true
			}
		}
	}
	if !queryTraced {
		t.Fatal("owned database connection lost query metadata")
	}
	var message model.HazardOutbox
	if err := ctrl.DB.First(&message).Error; err != nil {
		t.Fatal(err)
	}
	storedContext := telemetry.Propagation.Extract(context.Background(), propagation.MapCarrier{"traceparent": message.TraceParent})
	if trace.SpanContextFromContext(storedContext).SpanID() != ingest.SpanContext().SpanID() {
		t.Fatal("outbox lost ingestion trace context")
	}
	handler := aggregator.NewHandlerWithAudit(ctrl, "app-token", "audit-token")
	request := httptest.NewRequest("GET", "/internal/audit/events?source=PVMBG", nil)
	request.Header.Set("Authorization", "Bearer audit-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var page storage.EventPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || page.Partial || len(page.Items) != 1 || page.Items[0].Ref != "m2-v1" {
		t.Fatalf("audit cannot read owned poll result: status=%d page=%+v err=%v", response.Code, page, err)
	}
}
