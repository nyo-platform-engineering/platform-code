package api

import (
	"bytes"
	"context"
	"encoding/json"
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
	Timeout, StaleAfter        time.Duration
	HTTPClient                 *http.Client
}

type upstream struct {
	config Config
	http   *http.Client
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

func (u upstream) get(ctx context.Context, method, endpoint, token, correlation string, body any, result any) error {
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
	start := time.Now()
	status := 0
	defer func() {
		slog.Info("outbound_request", "service", "Client API", "correlation_id", correlation,
			"method", method, "upstream", r.URL.Host, "path", r.URL.Path,
			"status", status, "latency_ms", float64(time.Since(start).Microseconds())/1000)
	}()
	response, err := u.http.Do(r)
	if err != nil {
		return err
	}
	status = response.StatusCode
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("upstream status %d", response.StatusCode)
	}
	const maxResponse = 8 << 20
	b, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return err
	}
	if len(b) > maxResponse {
		return fmt.Errorf("upstream response too large")
	}
	return json.Unmarshal(b, result)
}

func (u upstream) endpoint(base, path string) string { return strings.TrimRight(base, "/") + path }
