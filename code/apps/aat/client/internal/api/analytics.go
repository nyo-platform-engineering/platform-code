package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aat/internal/analytics"
	"github.com/gin-gonic/gin"
)

func registerAnalytics(router *gin.Engine, u upstream) {
	router.GET("/analytics", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || parts[0] != "Bearer" || len(parts[1]) > 256 {
			unauthorized(c)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), u.config.Timeout)
		defer cancel()
		var identity struct {
			Active bool   `json:"active"`
			ID     string `json:"client_id"`
		}
		correlation := c.GetHeader("X-Correlation-ID")
		if err := u.introspectInto(ctx, parts[1], correlation, &identity); err != nil {
			unavailable(c)
			return
		}
		if !identity.Active || !identityKnown(identity.ID) {
			unauthorized(c)
			return
		}
		if identity.ID != "analyst" {
			c.JSON(403, gin.H{"error": "analytics requires BNPB Internal access"})
			return
		}
		values, err := url.ParseQuery(c.Request.URL.RawQuery)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid query encoding"})
			return
		}
		q, err := analytics.Parse(values, time.Now())
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		var result analytics.Result
		if err := u.get(ctx, "GET", u.endpoint(u.config.AggregatorURL, "/internal/analytics")+"?"+q.Values().Encode(), u.config.AggregatorToken, correlation, nil, &result); err != nil || result.Validate() != nil || !result.From.Equal(q.From) || !result.To.Equal(q.To) || result.Interval != q.Interval || result.Source != q.Source {
			var status upstreamStatusError
			if errors.As(err, &status) && status.Status == http.StatusTooManyRequests {
				c.Header("Retry-After", "1")
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "read capacity reached; retry later"})
				return
			}
			unavailable(c)
			return
		}
		c.JSON(200, struct {
			analytics.Result
			Sources []SourceStatus `json:"sources"`
		}{result, u.statuses(ctx, "", correlation)})
	})
	// The browser uses same-origin endpoints. Internal service credentials never
	// enter HTML or JavaScript; dashboard login is the existing analyst account.
	router.POST("/session", u.session(false))
	router.POST("/session/refresh", u.session(true))
}

func (u upstream) session(refresh bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		var input struct {
			Secret  string `json:"client_secret"`
			Refresh string `json:"refresh_token"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			c.JSON(400, gin.H{"error": "expected one JSON document"})
			return
		}
		path := "/auth/token"
		body := map[string]string{"client_id": "analyst", "client_secret": input.Secret}
		if refresh {
			path = "/auth/refresh"
			body = map[string]string{"refresh_token": input.Refresh}
		}
		if (!refresh && (input.Secret == "" || len(input.Secret) > 256 || input.Refresh != "")) || (refresh && (input.Refresh == "" || len(input.Refresh) > 256 || input.Secret != "")) {
			c.JSON(400, gin.H{"error": "invalid credentials or token"})
			return
		}
		raw, _ := json.Marshal(body)
		ctx, cancel := context.WithTimeout(c.Request.Context(), u.config.Timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", u.endpoint(u.config.AuthURL, path), strings.NewReader(string(raw)))
		if err != nil {
			unavailable(c)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Correlation-ID", c.GetHeader("X-Correlation-ID"))
		res, err := u.http.Do(req)
		if err != nil {
			unavailable(c)
			return
		}
		defer res.Body.Close()
		if res.StatusCode == 401 {
			c.JSON(401, gin.H{"error": "invalid credentials or token"})
			return
		}
		if res.StatusCode == 429 {
			c.Header("Retry-After", "1")
			c.JSON(429, gin.H{"error": "session capacity reached"})
			return
		}
		if res.StatusCode != 200 {
			unavailable(c)
			return
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 8193))
		if err != nil || len(data) > 8192 {
			unavailable(c)
			return
		}
		var pair struct {
			Access         string `json:"access_token"`
			Refresh        string `json:"refresh_token"`
			Type           string `json:"token_type"`
			Expires        int64  `json:"expires_in"`
			RefreshExpires int64  `json:"refresh_expires_in"`
		}
		if json.Unmarshal(data, &pair) != nil || pair.Access == "" || pair.Refresh == "" || pair.Type != "Bearer" || pair.Expires < 1 {
			unavailable(c)
			return
		}
		c.JSON(200, pair)
	}
}
