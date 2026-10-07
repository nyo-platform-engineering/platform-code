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

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"aat/internal/httpkit"
)

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
	ctrl       *controller.Controller
	client     *http.Client
}

func newSourcePoller(
	source, baseURL, header, credential string,
	endpoints []pollEndpoint,
	ctrl *controller.Controller,
	client *http.Client,
) sourcePoller {
	requestURL, err := url.ParseRequestURI(baseURL)
	if err != nil || requestURL.Scheme != "http" || requestURL.Host == "" || credential == "" {
		panic("invalid polling configuration for " + source)
	}
	return sourcePoller{
		source:     source,
		baseURL:    strings.TrimRight(baseURL, "/"),
		header:     header,
		credential: credential,
		endpoints:  endpoints,
		ctrl:       ctrl,
		client:     client,
	}
}

func (p sourcePoller) Run(ctx context.Context, interval time.Duration) {
	// Fetch immediately, then repeat at the configured interval.
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
	state, err := p.ctrl.GetPollState(ctx, p.source)
	if err != nil {
		slog.Error("poll state unavailable", "source", p.source, "error", err)
		return
	}

	// Capture the caller time before fetching; save it only if the whole poll succeeds.
	callerTime := time.Now().UTC()
	correlationID := httpkit.ID()

	since := callerTime.Add(-180 * time.Second)
	if state.LastSuccessCallerTime != nil {
		since = *state.LastSuccessCallerTime
	}

	// Fetch every endpoint before saving anything.
	batch := make(map[string][]model.Record, len(p.endpoints))
	for _, endpoint := range p.endpoints {
		records, err := p.fetch(ctx, endpoint.path, &since, correlationID)
		if err != nil {
			p.markFailure(ctx, callerTime, err)
			return
		}
		batch[endpoint.key] = records
	}

	// Save the batch and save this source's caller time only after ingestion succeeds.
	changedEvents, err := p.ctrl.IngestBatch(ctx, p.source, batch)
	if err != nil {
		p.markFailure(ctx, callerTime, fmt.Errorf("ingest: %w", err))
		return
	}

	if err := p.ctrl.MarkPollSuccess(ctx, p.source, callerTime); err != nil {
		slog.Error("poll status write failed",
			"source", p.source,
			"correlation_id", correlationID,
			"error", err,
		)
		return
	}
	slog.Info("poll succeeded",
		"source", p.source,
		"correlation_id", correlationID,
		"changed", len(changedEvents),
	)
}

func (p sourcePoller) fetch(ctx context.Context, path string, since *time.Time, correlationID string) ([]model.Record, error) {
	requestURL, err := url.Parse(p.baseURL + path)
	if err != nil {
		return nil, err
	}

	// Use the last successful caller time, or the initial 180-second lookback.
	if since != nil {
		query := requestURL.Query()
		query.Set("since", since.UTC().Format(time.RFC3339Nano))
		requestURL.RawQuery = query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set(p.header, p.credential)
	request.Header.Set("X-Correlation-ID", correlationID)

	requestStartedAt := time.Now()
	response, err := p.client.Do(request)
	latency := time.Since(requestStartedAt)
	if err != nil {
		slog.Warn("poll request failed",
			"source", p.source,
			"path", path,
			"correlation_id", correlationID,
			"latency_ms", latency.Milliseconds(),
			"error", err,
		)
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", path, response.Status)
	}

	// Limit each response to 2 MiB before decoding the source records.
	const maxResponseBytes = 2 << 20
	var records []model.Record
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(&records); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	slog.Info("poll request",
		"source", p.source,
		"path", path,
		"correlation_id", correlationID,
		"latency_ms", latency.Milliseconds(),
		"records", len(records),
	)
	return records, nil
}

func (p sourcePoller) markFailure(ctx context.Context, callerTime time.Time, err error) {
	slog.Warn("poll failed", "source", p.source, "error", err)
	if statusError := p.ctrl.MarkPollFailure(ctx, p.source, callerTime, err.Error()); statusError != nil {
		slog.Error("poll failure write failed", "source", p.source, "error", statusError)
	}
}
