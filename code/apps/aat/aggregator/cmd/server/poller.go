package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
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
	if interval <= 0 || interval >= controller.PollSessionIdleTimeout/2 {
		panic("poll interval must be positive and below half the owner session idle timeout")
	}
	var session *controller.PollSession
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		if session == nil {
			acquireCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			session, err = p.ctrl.AcquirePollSession(acquireCtx, p.source)
			cancel()
			if err != nil && !errors.Is(err, controller.ErrPollBusy) {
				slog.Warn("poll ownership unavailable", "source", p.source, "error", err)
			}
			if session != nil {
				slog.Info("poll ownership acquired", "source", p.source,
					"epoch", session.Fence.Epoch, "owner", session.Fence.Owner)
			}
		}
		if session != nil {
			owned := p
			owned.ctrl = session.Controller
			_ = owned.Poll(ctx)
			checkCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := session.Controller.ValidatePollOwner(checkCtx, p.source)
			cancel()
			if err != nil {
				slog.Warn("poll ownership lost", "source", p.source, "epoch", session.Fence.Epoch, "error", err)
				session.Close()
				session = nil
			}
		}
		if !wait(ctx, interval) {
			return
		}
	}
}

func (p sourcePoller) Poll(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Direct calls also need ownership.
	if p.ctrl.PollOwner == nil {
		session, err := p.ctrl.AcquirePollSession(ctx, p.source)
		if err != nil {
			return err
		}
		defer session.Close()
		p.ctrl = session.Controller
	}
	correlationID := httpkit.ID()
	ctx = httpkit.WithCorrelationID(ctx, correlationID)
	if err := p.ctrl.ValidatePollOwner(ctx, p.source); err != nil {
		return err
	}
	state, err := p.ctrl.GetPollState(ctx, p.source)
	if err != nil {
		slog.Error("poll state unavailable", "source", p.source, "error", err)
		return err
	}

	// Local time is only used for error timestamps.
	callerTime := time.Now().UTC()
	since := state.FetchCursor

	batch := make(map[string][]model.Record, len(p.endpoints))
	for _, endpoint := range p.endpoints {
		if err := p.ctrl.ValidatePollOwner(ctx, p.source); err != nil {
			return err
		}
		records, err := p.fetch(ctx, endpoint.path, since, correlationID)
		if err != nil {
			p.markFailure(ctx, callerTime, err)
			return err
		}
		batch[endpoint.key] = records
	}

	cursor, err := sourceCursor(p.source, batch, since)
	if err != nil {
		p.markFailure(ctx, callerTime, err)
		return err
	}
	changedEvents, err := p.ctrl.IngestPollBatch(ctx, p.source, batch, cursor)
	if err != nil {
		p.markFailure(ctx, callerTime, fmt.Errorf("ingest: %w", err))
		return err
	}
	slog.Info("poll succeeded",
		"source", p.source,
		"correlation_id", correlationID,
		"changed", len(changedEvents),
		"epoch", p.ctrl.PollOwner.Epoch,
	)
	return nil
}

// sourceCursor uses event time from the current mocks. BMKG fetches warnings
// after seismic events, so future arrival times must not advance the cursor.
// Historical updates need a separate recovery strategy; see docs/m2-polling.md.
func sourceCursor(source string, batch map[string][]model.Record, previous *time.Time) (*time.Time, error) {
	key, field := "seismic_events", "occurred_at"
	if source == "PVMBG" {
		key, field = "volcanic_reports", "reported_at"
	}
	var latest *time.Time
	if previous != nil {
		copy := previous.UTC()
		latest = &copy
	}
	for _, record := range batch[key] {
		var stamp time.Time
		if err := json.Unmarshal(record[field], &stamp); err != nil || stamp.IsZero() {
			return nil, fmt.Errorf("invalid source cursor field %s", field)
		}
		stamp = stamp.UTC()
		if latest == nil || stamp.After(*latest) {
			latest = &stamp
		}
	}
	return latest, nil
}

func (p sourcePoller) fetch(ctx context.Context, path string, since *time.Time, correlationID string) ([]model.Record, error) {
	requestURL, err := url.Parse(p.baseURL + path)
	if err != nil {
		return nil, err
	}

	// Omit since on the initial poll so none of the historical seed is skipped.
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
	if p.ctrl != nil && p.ctrl.PollOwner != nil {
		request.Header.Set("X-Poll-Owner", p.ctrl.PollOwner.Owner)
		request.Header.Set("X-Poll-Epoch", strconv.FormatInt(p.ctrl.PollOwner.Epoch, 10))
	}

	requestStartedAt := time.Now()
	status := 0
	defer func() {
		slog.Info("outbound_request", "source", p.source, "path", path,
			"poll_owner", request.Header.Get("X-Poll-Owner"), "poll_epoch", request.Header.Get("X-Poll-Epoch"),
			"correlation_id", correlationID, "status", status,
			"latency_ms", time.Since(requestStartedAt).Milliseconds())
	}()
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
	status = response.StatusCode

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
	slog.Warn("poll failed", "source", p.source, "correlation_id", httpkit.CorrelationID(ctx), "error", err)
	if statusError := p.ctrl.MarkPollFailure(ctx, p.source, callerTime, err.Error()); statusError != nil {
		slog.Error("poll failure write failed", "source", p.source, "error", statusError)
	}
}
