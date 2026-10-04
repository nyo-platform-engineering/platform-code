package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aat/aggregator/internal/aggregate"
	"aat/internal/httpkit"
)

type pollStore interface {
	PollState(context.Context, string) (aggregate.PollState, error)
	Ingest(context.Context, string, map[string][]aggregate.Record) ([]aggregate.Hazard, error)
	MarkPollSuccess(context.Context, string, time.Time) error
	MarkPollFailure(context.Context, string, string) error
}

type pollEndpoint struct {
	path string
	key  string
}

type sourcePoller struct {
	source     string
	baseURL    string
	header     string
	credential string
	endpoints  []pollEndpoint
	store      pollStore
	client     *http.Client
}

func newSourcePoller(source, baseURL, header, credential string, endpoints []pollEndpoint, store pollStore, client *http.Client) sourcePoller {
	u, e := url.ParseRequestURI(baseURL)
	if e != nil || u.Scheme != "http" || u.Host == "" || credential == "" {
		panic("invalid polling configuration for " + source)
	}
	return sourcePoller{source: source, baseURL: strings.TrimRight(baseURL, "/"), header: header, credential: credential, endpoints: endpoints, store: store, client: client}
}

func (p sourcePoller) Run(ctx context.Context, interval time.Duration) {
	p.Poll(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Poll(ctx)
		}
	}
}

func (p sourcePoller) Poll(ctx context.Context) {
	state, e := p.store.PollState(ctx, p.source)
	if e != nil {
		slog.Error("poll state unavailable", "source", p.source, "error", e)
		return
	}

	started := time.Now().UTC()
	correlation := httpkit.ID()
	body := make(map[string][]aggregate.Record, len(p.endpoints))
	for _, endpoint := range p.endpoints {
		records, e := p.fetch(ctx, endpoint.path, state.Cursor, correlation)
		if e != nil {
			p.failure(ctx, e)
			return
		}
		body[endpoint.key] = records
	}

	changed, e := p.store.Ingest(ctx, p.source, body)
	if e != nil {
		p.failure(ctx, fmt.Errorf("ingest: %w", e))
		return
	}
	if e = p.store.MarkPollSuccess(ctx, p.source, started); e != nil {
		slog.Error("poll status write failed", "source", p.source, "correlation_id", correlation, "error", e)
		return
	}
	slog.Info("poll succeeded", "source", p.source, "correlation_id", correlation, "changed", len(changed))
}

func (p sourcePoller) fetch(ctx context.Context, path string, since *time.Time, correlation string) ([]aggregate.Record, error) {
	u, e := url.Parse(p.baseURL + path)
	if e != nil {
		return nil, e
	}
	if since != nil {
		q := u.Query()
		q.Set("since", since.UTC().Format(time.RFC3339Nano))
		u.RawQuery = q.Encode()
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set(p.header, p.credential)
	req.Header.Set("X-Correlation-ID", correlation)
	start := time.Now()
	response, e := p.client.Do(req)
	latency := time.Since(start)
	if e != nil {
		slog.Warn("poll request failed", "source", p.source, "path", path, "correlation_id", correlation, "latency_ms", latency.Milliseconds(), "error", e)
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", path, response.Status)
	}
	var records []aggregate.Record
	if e = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&records); e != nil {
		return nil, fmt.Errorf("decode %s: %w", path, e)
	}
	slog.Info("poll request", "source", p.source, "path", path, "correlation_id", correlation, "latency_ms", latency.Milliseconds(), "records", len(records))
	return records, nil
}

func (p sourcePoller) failure(ctx context.Context, e error) {
	slog.Warn("poll failed", "source", p.source, "error", e)
	if statusError := p.store.MarkPollFailure(ctx, p.source, e.Error()); statusError != nil {
		slog.Error("poll failure write failed", "source", p.source, "error", statusError)
	}
}

