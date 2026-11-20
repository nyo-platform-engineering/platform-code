package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	AuthURL, AggregatorURL     string
	AuthToken, AggregatorToken string
	MaxConcurrent              int
	// MediaSharePercent caps the share of MaxConcurrent Media requests may hold;
	// 0 means 50. IdentityCacheTTL caches positive introspections; 0 means 10 s,
	// negative disables the cache.
	MediaSharePercent   int
	IdentityCacheTTL    time.Duration
	Timeout, StaleAfter time.Duration
	HTTPClient          *http.Client
}

type upstream struct {
	config Config
	http   *http.Client
	// retry sends a priority read again on a new connection; pooled connections stay
	// on one Aggregator pod, so a retry on them would meet the same busy pod.
	retry *http.Client
	ids   *identityCache
}

// priorityRetryDelay lets a reserved read slot free up before the one retry.
const priorityRetryDelay = 50 * time.Millisecond

type upstreamStatusError struct{ Status int }

func (err upstreamStatusError) Error() string { return fmt.Sprintf("upstream status %d", err.Status) }

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

// get calls an upstream service. Reads for priority callers carry X-Read-Class:
// priority so the Aggregator serves them from its reserved slots, and a priority read
// that still meets 429 is retried once, on a new connection, after a short delay.
// Media reads are never retried: shedding them is the point under overload.
func (u upstream) get(ctx context.Context, method, endpoint, token, correlation string, body any, result any) error {
	priority := method == http.MethodGet && body == nil && readClass(ctx) == "priority"
	err := u.send(ctx, u.http, priority, false, method, endpoint, token, correlation, body, result)
	var status upstreamStatusError
	if !priority || u.retry == nil || !errors.As(err, &status) || status.Status != http.StatusTooManyRequests {
		return err
	}
	select {
	case <-ctx.Done():
		return err
	case <-time.After(priorityRetryDelay):
	}
	return u.send(ctx, u.retry, priority, true, method, endpoint, token, correlation, body, result)
}

func (u upstream) send(ctx context.Context, client *http.Client, priority, retried bool, method, endpoint, token, correlation string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Correlation-ID", correlation)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if priority {
		r.Header.Set("X-Read-Class", "priority")
	}
	start := time.Now()
	status := 0
	defer func() {
		slog.Info("outbound_request", "service", "Client API", "correlation_id", correlation,
			"method", method, "upstream", r.URL.Host, "path", r.URL.Path, "priority", priority, "retried", retried,
			"status", status, "latency_ms", float64(time.Since(start).Microseconds())/1000)
	}()
	response, err := client.Do(r)
	if err != nil {
		return err
	}
	status = response.StatusCode
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return upstreamStatusError{Status: response.StatusCode}
	}
	maxResponse := 8 << 20
	if strings.HasPrefix(r.URL.Path, "/internal/ops/unprocessed/") {
		maxResponse = 12 << 20
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, int64(maxResponse)+1))
	if err != nil {
		return err
	}
	if len(b) > maxResponse {
		return fmt.Errorf("upstream response too large")
	}
	return json.Unmarshal(b, result)
}

func (u upstream) endpoint(base, path string) string { return strings.TrimRight(base, "/") + path }

// freshConnections returns a transport that does not reuse connections. A custom
// (non-*http.Transport) round tripper, as in tests, is returned unchanged.
func freshConnections(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	t, ok := base.(*http.Transport)
	if !ok {
		return base
	}
	fresh := t.Clone()
	fresh.DisableKeepAlives = true
	return fresh
}
