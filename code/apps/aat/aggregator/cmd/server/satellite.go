package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"aat/aggregator/internal/controller"
	"aat/internal/httpkit"
)

// satelliteConsumer follows the satellite feed's SSE stream. Only the instance
// holding the SATELIT poll session reads it; the others wait to take over.
// Progress is the feed offset, saved with the detections in one transaction,
// so a reconnect, crash, or takeover resumes exactly after the last applied event.
type satelliteConsumer struct {
	baseURL    string
	token      string
	ctrl       *controller.Controller
	client     *http.Client
	batchSize  int
	flushEvery time.Duration
	// idleTimeout reconnects when the stream sends nothing, not even a heartbeat.
	idleTimeout time.Duration
}

var (
	errFeedGap       = errors.New("satellite feed skipped offsets")
	errFeedIdle      = errors.New("satellite feed idle")
	errFeedRejected  = errors.New("satellite feed rejected the request")
	errFeedThrottled = errors.New("satellite feed: too many connections")
)

func newSatelliteConsumer(baseURL, token string, ctrl *controller.Controller) satelliteConsumer {
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		panic("invalid SATELLITE_FEED_URL")
	}
	return satelliteConsumer{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		ctrl:    ctrl,
		// No overall timeout: the stream stays open. The idle watchdog covers stalls.
		client:      &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second, Proxy: http.ProxyFromEnvironment}},
		batchSize:   200,
		flushEvery:  500 * time.Millisecond,
		idleTimeout: 30 * time.Second,
	}
}

func (c satelliteConsumer) Run(ctx context.Context) {
	var session *controller.PollSession
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		if session == nil {
			acquireCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			session, err = c.ctrl.AcquirePollSession(acquireCtx, controller.SatelliteSource)
			cancel()
			if err != nil {
				if !errors.Is(err, controller.ErrPollBusy) {
					slog.Warn("satellite feed ownership unavailable", "error", err)
				}
				if !wait(ctx, 2*time.Second) {
					return
				}
				continue
			}
			slog.Info("satellite feed ownership acquired", "epoch", session.Fence.Epoch, "owner", session.Fence.Owner)
		}

		owned := c
		owned.ctrl = session.Controller
		progressed, err := owned.follow(ctx)
		if ctx.Err() != nil {
			return
		}
		if progressed {
			backoff = time.Second
		}
		slog.Warn("satellite feed disconnected", "error", err, "retry_in_ms", backoff.Milliseconds())

		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		ownerErr := session.Controller.ValidatePollOwner(checkCtx, controller.SatelliteSource)
		cancel()
		if ownerErr != nil {
			slog.Warn("satellite feed ownership lost", "epoch", session.Fence.Epoch, "error", ownerErr)
			session.Close()
			session = nil
		}

		pause := backoff
		switch {
		case errors.Is(err, errFeedThrottled):
			pause = max(pause, 5*time.Second)
		case errors.Is(err, errFeedRejected):
			pause = 30 * time.Second
		}
		// Full jitter keeps several groups' retries from lining up.
		if !wait(ctx, pause/2+rand.N(pause/2+1)) {
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// follow streams from the stored offset until the connection ends. It reports
// whether any event was committed, which resets the reconnect backoff.
func (c satelliteConsumer) follow(ctx context.Context) (progressed bool, err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	offset, err := c.ctrl.SatelliteFeedOffset(ctx)
	if err != nil {
		return false, err
	}
	correlationID := httpkit.ID()
	ctx = httpkit.WithCorrelationID(ctx, correlationID)

	response, err := c.open(ctx, offset, correlationID)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	slog.Info("satellite feed connected", "from", offsetText(offset), "correlation_id", correlationID,
		"epoch", c.ctrl.PollOwner.Epoch)

	events := make(chan controller.FeedEvent, 4*c.batchSize)
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	readErr := make(chan error, 1)
	go func() {
		readErr <- readSSE(response.Body, func(event controller.FeedEvent) error {
			lastRead.Store(time.Now().UnixNano())
			select {
			case events <- event:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}, func() { lastRead.Store(time.Now().UnixNano()) })
		close(events)
	}()

	// expected tracks the last offset accepted into a batch, to catch gaps.
	expected := offset
	pending := []controller.FeedEvent{}
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		started := time.Now()
		result, err := c.ctrl.IngestFeedBatch(ctx, pending)
		if err != nil {
			return err
		}
		oldest := pending[0].ReceivedAt
		slog.Info("satellite batch committed",
			"correlation_id", correlationID,
			"offset", result.Offset,
			"events", len(pending),
			"applied", result.Applied,
			"skipped", result.Skipped,
			"rejected", result.Rejected,
			"changed", len(result.Changed),
			"commit_ms", time.Since(started).Milliseconds(),
			// Arrival of the oldest event in the batch to commit: the worst ingest latency in it.
			"ingest_latency_ms", time.Since(oldest).Milliseconds(),
		)
		pending = pending[:0]
		progressed = true
		return nil
	}

	tick := time.NewTicker(c.flushEvery)
	defer tick.Stop()
	session := time.NewTicker(10 * time.Second)
	defer session.Stop()
	lastCheck := time.Now()
	c.recordSession(ctx)

	for {
		select {
		case event, ok := <-events:
			if !ok {
				if err := flush(); err != nil {
					return progressed, err
				}
				return progressed, <-readErr
			}
			if expected != nil && event.Offset <= *expected {
				continue // replayed after reconnect; already applied or pending
			}
			if expected != nil && event.Offset > *expected+1 {
				// The feed promises no gaps; reconnect from the last good offset.
				if err := flush(); err != nil {
					return progressed, err
				}
				slog.Error("satellite feed gap", "correlation_id", correlationID,
					"expected", *expected+1, "received", event.Offset)
				return progressed, errFeedGap
			}
			o := event.Offset
			expected = &o
			pending = append(pending, event)
			if len(pending) >= c.batchSize {
				if err := flush(); err != nil {
					return progressed, err
				}
			}
		case <-tick.C:
			// Events already waiting mean the consumer is behind (they queued during the
			// last commit): take them first and let them fill a full batch, instead of
			// committing the few pending ones as a batch of their own. When the feed is
			// live the channel is empty, so the tick flushes as before.
			if len(events) == 0 || len(pending) >= c.batchSize {
				if err := flush(); err != nil {
					return progressed, err
				}
			}
			if time.Since(time.Unix(0, lastRead.Load())) > c.idleTimeout {
				return progressed, errFeedIdle
			}
			// Touch the owner session well within its idle timeout.
			if time.Since(lastCheck) >= 5*time.Second {
				if err := c.ctrl.ValidatePollOwner(ctx, controller.SatelliteSource); err != nil {
					return progressed, err
				}
				lastCheck = time.Now()
			}
		case <-session.C:
			c.recordSession(ctx)
		case <-ctx.Done():
			return progressed, ctx.Err()
		}
	}
}

func (c satelliteConsumer) open(ctx context.Context, offset *int64, correlationID string) (*http.Response, error) {
	// from is the stored offset; anything at or below it is skipped on arrival,
	// so this is safe whether the feed treats from as inclusive or exclusive.
	from := int64(0)
	if offset != nil {
		from = *offset
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/stream?from="+strconv.FormatInt(from, 10), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("X-Correlation-ID", correlationID)
	if offset != nil {
		request.Header.Set("Last-Event-ID", strconv.FormatInt(*offset, 10))
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK {
		return response, nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	response.Body.Close()
	err = fmt.Errorf("stream returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		return nil, errors.Join(errFeedThrottled, err)
	case http.StatusUnauthorized, http.StatusBadRequest, http.StatusForbidden:
		return nil, errors.Join(errFeedRejected, err)
	}
	return nil, err
}

// recordSession saves the feed's own position so lag can be shown. Failures only log.
func (c satelliteConsumer) recordSession(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/session", nil)
	if err != nil {
		return
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(request)
	if err != nil {
		slog.Warn("satellite session check failed", "error", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		slog.Warn("satellite session check failed", "status", response.StatusCode)
		return
	}
	var session struct {
		LatestOffset *int64          `json:"latest_offset"`
		VirtualNow   json.RawMessage `json:"virtual_now"`
		Speed        json.RawMessage `json:"speed"`
		Profile      json.RawMessage `json:"profile"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&session); err != nil || session.LatestOffset == nil {
		slog.Warn("satellite session unreadable", "error", err)
		return
	}
	var virtualNow *time.Time
	var stamp time.Time
	if json.Unmarshal(session.VirtualNow, &stamp) == nil && !stamp.IsZero() {
		virtualNow = &stamp
	}
	if err := c.ctrl.RecordFeedSession(ctx, *session.LatestOffset, virtualNow); err != nil {
		slog.Warn("satellite session not recorded", "error", err)
		return
	}
	offset, _ := c.ctrl.SatelliteFeedOffset(ctx)
	lag := *session.LatestOffset
	if offset != nil {
		lag = max(*session.LatestOffset-*offset, 0)
	}
	slog.Info("satellite feed progress", "offset", offsetText(offset), "latest_offset", *session.LatestOffset,
		"lag_events", lag, "speed", string(session.Speed), "profile", string(session.Profile))
}

// readSSE parses a text/event-stream body. Each dispatched event goes to emit;
// any line read (comments included) calls alive, for the idle watchdog.
// Events without an id cannot be resumed from, so only heartbeats may omit it.
func readSSE(body io.Reader, emit func(controller.FeedEvent) error, alive func()) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	var id, kind string
	var data strings.Builder
	hasData := false
	reset := func() { id, kind, hasData = "", "", false; data.Reset() }
	for scanner.Scan() {
		alive()
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if hasData || kind != "" {
				event, err := sseEvent(id, kind, data.String())
				reset()
				if err != nil {
					return err
				}
				if event != nil {
					if err := emit(*event); err != nil {
						return err
					}
				}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment, often used as keep-alive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			kind = value
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func sseEvent(id, kind, data string) (*controller.FeedEvent, error) {
	if kind == "" {
		kind = "message"
	}
	offset, hasOffset := int64(0), false
	if id != "" {
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid event id %q", id)
		}
		offset, hasOffset = parsed, true
	} else if data != "" {
		// Fall back to an offset carried in the payload.
		var body struct {
			Offset *int64 `json:"offset"`
		}
		if json.Unmarshal([]byte(data), &body) == nil && body.Offset != nil {
			offset, hasOffset = *body.Offset, true
		}
	}
	if !hasOffset {
		if kind == "heartbeat" || kind == "message" {
			return nil, nil // keep-alive without a position
		}
		return nil, fmt.Errorf("%s event without an offset", kind)
	}
	return &controller.FeedEvent{Offset: offset, Type: kind, Data: []byte(data), ReceivedAt: time.Now()}, nil
}

func offsetText(offset *int64) string {
	if offset == nil {
		return "start"
	}
	return strconv.FormatInt(*offset, 10)
}
