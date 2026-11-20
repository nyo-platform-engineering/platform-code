package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/mapper"
	"aat/aggregator/internal/model"
	"aat/internal/httpkit"
)

// breaker is a circuit breaker for one dependency. Closed: requests flow. After
// threshold consecutive failures it opens and no request is sent until the open
// period ends; then one trial request (half-open) decides between closing and
// reopening for twice as long, up to maxOpen, with jitter so retries do not align.
type breaker struct {
	threshold        int
	baseOpen         time.Duration
	maxOpen          time.Duration
	state            string // closed, open, half_open
	failures         int
	opens            int
	openUntil        time.Time
	onTransition     func(from, to string, reason string)
	random           func() float64
	lastFailure      string
	consecutiveTotal int
}

func newBreaker(threshold int, baseOpen, maxOpen time.Duration) *breaker {
	return &breaker{threshold: threshold, baseOpen: baseOpen, maxOpen: maxOpen, state: "closed", random: rand.Float64}
}

func (b *breaker) move(to, reason string) {
	if b.state != to && b.onTransition != nil {
		b.onTransition(b.state, to, reason)
	}
	b.state = to
}

// allow reports whether a request may be sent now.
func (b *breaker) allow(now time.Time) bool {
	switch b.state {
	case "closed":
		return true
	case "open":
		if now.Before(b.openUntil) {
			return false
		}
		b.move("half_open", "open period over; sending one trial request")
		return true
	}
	return true // half_open: the single trial request
}

func (b *breaker) success() {
	b.failures, b.opens, b.consecutiveTotal = 0, 0, 0
	b.move("closed", "request succeeded")
}

func (b *breaker) failure(now time.Time, reason string) {
	b.failures++
	b.consecutiveTotal++
	b.lastFailure = reason
	if b.state == "half_open" || b.failures >= b.threshold {
		period := b.baseOpen << min(b.opens, 10)
		period = min(period, b.maxOpen)
		period = time.Duration(float64(period) * (0.75 + 0.5*b.random()))
		b.opens++
		b.failures = 0
		b.openUntil = now.Add(period)
		b.move("open", reason)
	}
}

// sensorPoller reads Sensor Pantai. It is a bulkhead: its own goroutine, HTTP client
// with a small connection cap and short timeout, and its own database session, so a
// slow or broken sensor cannot hold resources BMKG, PVMBG, or the satellite feed use.
type sensorPoller struct {
	baseURL  string
	token    string
	ctrl     *controller.Controller
	client   *http.Client
	interval time.Duration
	pageSize int
	// staleAfter is measured in sensor time (generated_at): with no new reading for
	// this long the data is reported stale even when the sensor answers 200.
	staleAfter time.Duration
	// frozenAfter: generated_at not advancing for this long in real time means the
	// sensor is replaying an old snapshot.
	frozenAfter     time.Duration
	stationsRefresh time.Duration
	breaker         *breaker

	// freshness tracking, in memory for the current owner
	lastGenerated     time.Time
	lastGeneratedSeen time.Time
	newestObserved    time.Time
	stationsFetchedAt time.Time
	requests          int64
	lastSuccess       *time.Time
	lastError         string
	stale             bool
	staleReason       string
	lastStatusLog     time.Time
}

var errSensorBadResponse = errors.New("sensor response unreadable")

func newSensorPoller(baseURL, token string, ctrl *controller.Controller, timeout time.Duration) *sensorPoller {
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || token == "" {
		panic("invalid SENSOR_PANTAI_URL or PANTAI_AUTH_TOKEN")
	}
	p := &sensorPoller{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		ctrl:    ctrl,
		client: &http.Client{Timeout: timeout, Transport: &http.Transport{
			MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, ResponseHeaderTimeout: timeout,
			IdleConnTimeout: 30 * time.Second, Proxy: http.ProxyFromEnvironment,
		}},
		interval:        time.Second,
		pageSize:        500,
		staleAfter:      15 * time.Minute,
		frozenAfter:     15 * time.Second,
		stationsRefresh: 5 * time.Minute,
		breaker:         newBreaker(3, 2*time.Second, 30*time.Second),
	}
	p.breaker.onTransition = func(from, to, reason string) {
		level := slog.LevelWarn
		if to == "closed" {
			level = slog.LevelInfo
		}
		slog.Log(context.Background(), level, "sensor breaker", "from", from, "to", to, "reason", reason,
			"open_until", p.breaker.openUntil.UTC().Format(time.RFC3339Nano))
	}
	return p
}

func (p *sensorPoller) Run(ctx context.Context) {
	var session *controller.PollSession
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	lastCheck := time.Time{}
	for ctx.Err() == nil {
		if session == nil {
			acquireCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			session, err = p.ctrl.AcquirePollSession(acquireCtx, controller.SensorSource)
			cancel()
			if err != nil {
				if !errors.Is(err, controller.ErrPollBusy) {
					slog.Warn("sensor ownership unavailable", "error", err)
				}
				if !wait(ctx, 2*time.Second) {
					return
				}
				continue
			}
			slog.Info("sensor ownership acquired", "epoch", session.Fence.Epoch, "owner", session.Fence.Owner)
			p.resume(ctx, session.Controller)
		}
		owned := session.Controller
		if time.Since(lastCheck) >= 5*time.Second {
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := owned.ValidatePollOwner(checkCtx, controller.SensorSource)
			cancel()
			if err != nil {
				slog.Warn("sensor ownership lost", "epoch", session.Fence.Epoch, "error", err)
				session.Close()
				session = nil
				continue
			}
			lastCheck = time.Now()
		}

		more := p.step(ctx, owned)
		if !more && !wait(ctx, p.interval) {
			return
		}
	}
}

// resume continues an open breaker after a takeover instead of probing at once.
func (p *sensorPoller) resume(ctx context.Context, ctrl *controller.Controller) {
	state, err := ctrl.SensorState(ctx)
	if err != nil {
		return
	}
	p.requests = state.Requests
	p.lastSuccess = state.LastSuccessAt
	if state.Breaker == "open" && state.OpenUntil != nil && state.OpenUntil.After(time.Now()) {
		p.breaker.state, p.breaker.openUntil = "open", *state.OpenUntil
	}
}

// step does one poll when the breaker allows it and reports whether another page
// is waiting (a full page), so a backlog is read without the poll delay.
func (p *sensorPoller) step(ctx context.Context, ctrl *controller.Controller) (more bool) {
	now := time.Now()
	defer p.record(ctx, ctrl)
	if !p.breaker.allow(now) {
		return false
	}
	correlationID := httpkit.ID()
	ctx = httpkit.WithCorrelationID(ctx, correlationID)

	// Readings are validated against the station list, so it is needed first.
	if p.stationsFetchedAt.IsZero() || now.Sub(p.stationsFetchedAt) > p.stationsRefresh {
		if err := p.refreshStations(ctx, ctrl, correlationID); err != nil {
			p.fail(now, "stations: "+err.Error())
			return false
		}
	}
	more, err := p.poll(ctx, ctrl, correlationID)
	if err != nil {
		p.fail(now, err.Error())
		return false
	}
	p.breaker.success()
	succeeded := time.Now().UTC()
	p.lastSuccess, p.lastError = &succeeded, ""
	return more
}

func (p *sensorPoller) fail(now time.Time, reason string) {
	p.lastError = reason
	p.breaker.failure(now, reason)
}

func (p *sensorPoller) get(ctx context.Context, path, correlationID string) ([]byte, error) {
	p.requests++
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	started := time.Now()
	status := 0
	defer func() {
		slog.Info("sensor_request", "path", strings.SplitN(path, "?", 2)[0], "status", status,
			"latency_ms", time.Since(started).Milliseconds(), "breaker", p.breaker.state, "correlation_id", correlationID)
	}()
	response, err := p.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	status = response.StatusCode
	body, err := io.ReadAll(io.LimitReader(response.Body, controller.MaxCaptureBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", strings.SplitN(path, "?", 2)[0], response.StatusCode)
	}
	if len(body) > controller.MaxCaptureBytes {
		return nil, fmt.Errorf("response larger than %d bytes", controller.MaxCaptureBytes)
	}
	return body, nil
}

func (p *sensorPoller) refreshStations(ctx context.Context, ctrl *controller.Controller, correlationID string) error {
	body, err := p.get(ctx, "/v1/stations", correlationID)
	if err != nil {
		return err
	}
	var page struct {
		Items []mapper.Station `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil || len(page.Items) == 0 {
		return errSensorBadResponse
	}
	if err := ctrl.SaveSensorStations(ctx, page.Items); err != nil {
		return err
	}
	p.stationsFetchedAt = time.Now()
	return nil
}

func (p *sensorPoller) poll(ctx context.Context, ctrl *controller.Controller, correlationID string) (more bool, err error) {
	state, err := ctrl.SensorState(ctx)
	if err != nil {
		return false, err
	}
	path := fmt.Sprintf("/v1/observations?limit=%d", p.pageSize)
	if state.Cursor != "" {
		path += "&cursor=" + url.QueryEscape(state.Cursor)
	}
	body, err := p.get(ctx, path, correlationID)
	if err != nil {
		return false, err
	}

	// Read only what the poller needs; full validation happens per record in the inbox.
	var page struct {
		GeneratedAt time.Time `json:"generated_at"`
		NextCursor  string    `json:"next_cursor"`
		Items       []struct {
			ObservedAt time.Time `json:"observed_at"`
		} `json:"items"`
	}
	readable := json.Unmarshal(body, &page) == nil && page.Items != nil && !page.GeneratedAt.IsZero()
	cursor := ""
	if readable {
		cursor = page.NextCursor
	}
	// The raw response is stored even when unreadable, so it is parked with a reason.
	if err := ctrl.CaptureSensorPoll(ctx, body, cursor); err != nil {
		return false, fmt.Errorf("capture: %w", err)
	}
	if _, err := ctrl.ProcessInbox(ctx, controller.SensorSource, 1000); err != nil {
		slog.Warn("sensor inbox processing incomplete", "correlation_id", correlationID, "error", err)
	}
	if !readable {
		return false, errSensorBadResponse
	}

	p.observeFreshness(page.GeneratedAt, len(page.Items) == p.pageSize, func(yield func(time.Time) bool) {
		for _, item := range page.Items {
			if !yield(item.ObservedAt) {
				return
			}
		}
	})
	slog.Info("sensor poll", "correlation_id", correlationID, "items", len(page.Items),
		"generated_at", page.GeneratedAt.UTC().Format(time.RFC3339), "stale", p.stale, "stale_reason", p.staleReason)
	return len(page.Items) == p.pageSize && cursor != "", nil
}

// observeFreshness decides whether the sensor's data is current. A sensor that keeps
// answering 200 and /healthz ok can still be serving hours-old data; both signs are
// judged in the sensor's own clock so the check works at any simulation speed.
func (p *sensorPoller) observeFreshness(generated time.Time, catchingUp bool, observed func(func(time.Time) bool)) {
	now := time.Now()
	// A sensor clock that goes back is a restarted sensor replaying its scenario from
	// the start, not a frozen clock: judge freshness from the new clock and its own
	// readings. Otherwise a restart stays "frozen" until the new clock passes the old one.
	if !p.lastGenerated.IsZero() && generated.Before(p.lastGenerated) {
		slog.Info("sensor clock went back; treating it as a sensor restart",
			"previous_generated_at", p.lastGenerated.UTC().Format(time.RFC3339),
			"generated_at", generated.UTC().Format(time.RFC3339))
		p.lastGenerated, p.lastGeneratedSeen, p.newestObserved = time.Time{}, time.Time{}, time.Time{}
	}
	for t := range observed {
		if t.After(p.newestObserved) {
			p.newestObserved = t
		}
	}
	switch {
	case !generated.After(p.lastGenerated) && !p.lastGenerated.IsZero():
		if now.Sub(p.lastGeneratedSeen) > p.frozenAfter {
			p.stale, p.staleReason = true, fmt.Sprintf("sensor clock frozen at %s", p.lastGenerated.UTC().Format(time.RFC3339))
		}
		return
	default:
		p.lastGenerated, p.lastGeneratedSeen = generated, now
	}
	if catchingUp {
		p.stale, p.staleReason = false, ""
		return
	}
	if !p.newestObserved.IsZero() && generated.Sub(p.newestObserved) > p.staleAfter {
		p.stale, p.staleReason = true, fmt.Sprintf("newest reading is %s behind the sensor clock", generated.Sub(p.newestObserved).Round(time.Second))
		return
	}
	p.stale, p.staleReason = false, ""
}

func (p *sensorPoller) record(ctx context.Context, ctrl *controller.Controller) {
	state := model.SensorState{
		Breaker: p.breaker.state, ConsecutiveFailures: p.breaker.consecutiveTotal, LastSuccessAt: p.lastSuccess,
		LastError: p.lastError, Stale: p.stale, StaleReason: p.staleReason, Requests: p.requests,
	}
	attempt := time.Now().UTC()
	state.LastAttemptAt = &attempt
	if p.breaker.state == "open" {
		until := p.breaker.openUntil.UTC()
		state.OpenUntil = &until
	}
	if !p.lastGenerated.IsZero() {
		g := p.lastGenerated.UTC()
		state.GeneratedAt = &g
	}
	if !p.newestObserved.IsZero() {
		o := p.newestObserved.UTC()
		state.NewestObservedAt = &o
	}
	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := ctrl.RecordSensorState(writeCtx, state); err != nil && ctx.Err() == nil {
		slog.Warn("sensor state not recorded", "error", err)
	}
	// A numeric status line every few seconds feeds the dashboard panels.
	if time.Since(p.lastStatusLog) >= 5*time.Second {
		p.lastStatusLog = time.Now()
		open := map[string]float64{"closed": 0, "half_open": 0.5, "open": 1}[p.breaker.state]
		stale := 0
		if p.stale {
			stale = 1
		}
		lag := 0.0
		if !p.lastGenerated.IsZero() && !p.newestObserved.IsZero() {
			lag = p.lastGenerated.Sub(p.newestObserved).Seconds()
		}
		slog.Info("sensor status", "breaker", p.breaker.state, "breaker_open", open, "data_stale", stale,
			"stale_reason", p.staleReason, "data_lag_sensor_seconds", lag, "requests_total", p.requests, "last_error", p.lastError)
	}
}
