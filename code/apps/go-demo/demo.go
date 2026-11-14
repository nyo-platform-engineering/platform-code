package main

import (
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"net/http"
	"time"
)

func registerDemo(mux *http.ServeMux, logger *slog.Logger) {
	mux.HandleFunc("GET /demo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(demoHTML))
	})
	for _, action := range []string{"success", "slow", "error"} {
		mux.HandleFunc("POST /demo/"+action, func(w http.ResponseWriter, r *http.Request) {
			params, err := parseDemoParameters(r, action)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			attrs := params.attributes(action)
			trace.SpanFromContext(r.Context()).SetAttributes(attrs...)
			ctx, span := otel.Tracer("go-demo").Start(r.Context(), "demo."+action, trace.WithAttributes(attrs...))
			defer span.End()
			eventLogger := logger
			for _, attr := range attrs {
				eventLogger = eventLogger.With(string(attr.Key), attr.Value.AsInterface())
			}
			status := http.StatusOK
			if params.DelayMS > 0 {
				timer := time.NewTimer(time.Duration(params.DelayMS) * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					span.RecordError(ctx.Err())
					span.SetStatus(codes.Error, "cancelled")
					return
				}
			}
			switch action {
			case "slow":
				eventLogger.WarnContext(ctx, "slow work completed", "action", action)
			case "error":
				err := errors.New("intentional demo failure")
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				eventLogger.ErrorContext(ctx, "demo action failed", "action", action, "error", err)
				status = http.StatusInternalServerError
			default:
				eventLogger.InfoContext(ctx, "work completed", "action", action)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"action": action, "trace_id": span.SpanContext().TraceID().String()})
		})
	}
}

const demoHTML = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><link rel="icon" href="data:,"><title>Go demo</title>
<style>body{font:18px system-ui;max-width:760px;margin:10vh auto;padding:24px;background:#10151c;color:#e6f1ef}button,a{font:inherit;padding:12px;margin:8px;color:inherit}button{background:#25433d;border:1px solid #7fcbb5;border-radius:8px;cursor:pointer}pre{white-space:pre-wrap;overflow-wrap:anywhere}a{display:inline-block}</style>
<h1>Generate a trace</h1><p>Each action creates a request trace, a work span, and correlated logs.</p>
<button data-action="success">Success</button><button data-action="slow">Slow · 500 ms</button><button data-action="error">Error · HTTP 500</button>
<pre aria-live="polite" id="result">Choose an action.</pre><a id="trace" hidden>Open trace →</a><a href="http://127.0.0.1:5173/logs">Open logs →</a>
<script>document.querySelectorAll('button').forEach(button=>button.onclick=async()=>{button.disabled=true;try{const response=await fetch('/demo/'+button.dataset.action,{method:'POST'});const data=await response.json();document.querySelector('#result').textContent=JSON.stringify({status:response.status,...data},null,2);const link=document.querySelector('#trace');link.href='http://127.0.0.1:5173/traces?traceId='+encodeURIComponent(data.trace_id);link.hidden=false}catch(error){document.querySelector('#result').textContent=String(error)}finally{button.disabled=false}})</script></html>`
