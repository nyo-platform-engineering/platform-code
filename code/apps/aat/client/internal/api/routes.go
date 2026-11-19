package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"github.com/gin-gonic/gin"
)

func Handler(config Config) (http.Handler, error) {
	if !validURL(config.AuthURL) || !validURL(config.AggregatorURL) || config.AuthToken == "" || config.AggregatorToken == "" || config.AuthToken == config.AggregatorToken || config.MaxConcurrent < 1 || config.Timeout <= 0 || config.StaleAfter <= 0 {
		return nil, fmt.Errorf("invalid client API configuration")
	}
	client := http.Client{}
	if config.HTTPClient != nil {
		client = *config.HTTPClient
	}
	client.Transport = telemetry.Transport{Base: client.Transport}
	// Never forward internal credentials through redirects.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if config.MediaSharePercent == 0 {
		config.MediaSharePercent = 50
	}
	if config.IdentityCacheTTL == 0 {
		config.IdentityCacheTTL = 10 * time.Second
	}
	if config.MediaSharePercent < 1 || config.MediaSharePercent > 100 {
		return nil, fmt.Errorf("invalid client API configuration")
	}
	u := upstream{config: config, http: &client, ids: &identityCache{ttl: config.IdentityCacheTTL, entries: map[[32]byte]cachedIdentity{}}}
	router := httpkit.Router()
	registerDashboard(router)
	registerAnalytics(router, u)
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	router.GET("/hazards", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || parts[0] != "Bearer" || len(parts[1]) > 256 {
			unauthorized(c)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), config.Timeout)
		defer cancel()
		correlation := c.GetHeader("X-Correlation-ID")
		var identity struct {
			Active bool   `json:"active"`
			ID     string `json:"client_id"`
		}
		err := u.introspectInto(ctx, parts[1], correlation, &identity)
		if err != nil {
			unavailable(c)
			return
		}
		if !identity.Active || !identityKnown(identity.ID) {
			unauthorized(c)
			return
		}
		q, err := url.ParseQuery(c.Request.URL.RawQuery)
		if err != nil || !validQuery(q) {
			c.JSON(400, gin.H{"error": "invalid query; use source, limit (1..1000), after or order=recent with cursor, and fields"})
			return
		}
		requested := []string(nil)
		if q.Has("fields") {
			requested = strings.Split(q.Get("fields"), ",")
			for _, field := range requested {
				if field == "" {
					c.JSON(400, gin.H{"error": "fields must be a comma-separated list"})
					return
				}
				if !allowedField(identity.ID, field) {
					c.JSON(403, gin.H{"error": "field access denied"})
					return
				}
			}
			q.Del("fields") // Projection belongs to this service, never to the store API.
		}
		var page struct {
			Items            []Hazard `json:"items"`
			Next             string   `json:"next_after"`
			Cursor           *string  `json:"next_cursor"`
			Partial          bool     `json:"partial"`
			UnavailableUnits []string `json:"unavailable_units"`
		}
		err = u.get(ctx, "GET", u.endpoint(config.AggregatorURL, "/internal/hazards")+"?"+q.Encode(), config.AggregatorToken, correlation, nil, &page)
		var status upstreamStatusError
		if errors.As(err, &status) && status.Status == http.StatusTooManyRequests {
			c.Header("Retry-After", "1")
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "read capacity reached; retry later"})
			return
		}
		if err != nil || page.Items == nil {
			unavailable(c)
			return
		}
		items := make([]map[string]any, 0, len(page.Items))
		for _, hazard := range page.Items {
			if !hazard.valid() || (q.Get("source") != "" && hazard.Source != q.Get("source")) {
				unavailable(c)
				return
			}
			item := hazard.project(identity.ID)
			if requested != nil {
				selected := make(map[string]any, len(requested))
				for _, field := range requested {
					selected[field] = item[field]
				}
				item = selected
			}
			items = append(items, item)
		}
		statuses := u.statuses(ctx, q.Get("source"), correlation)
		units := page.UnavailableUnits
		if units == nil {
			units = []string{}
		}
		c.JSON(200, gin.H{"items": items, "next_after": page.Next, "next_cursor": page.Cursor, "sources": statuses, "partial": page.Partial, "unavailable_units": units})
	})
	router.GET("/ops/unprocessed", u.ops)
	router.POST("/field-reports", u.fieldReports)
	router.GET("/ops/field-reports/:report_id", u.ops)
	router.GET("/ops/unprocessed/:message_id", u.ops)
	router.POST("/ops/unprocessed/:message_id/replay", u.ops)
	return newAdmission(config.MaxConcurrent, config.MediaSharePercent).wrap(u, router), nil
}

func validQuery(q url.Values) bool {
	if (q.Get("cursor") != "" && q.Get("order") != "recent") || (q.Get("order") == "recent" && q.Get("after") != "") {
		return false
	}
	for key, values := range q {
		if len(values) != 1 {
			return false
		}
		switch key {
		case "source":
			if !knownSource(values[0]) {
				return false
			}
		case "limit":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > 1000 {
				return false
			}
		case "order":
			if values[0] != "id" && values[0] != "recent" {
				return false
			}
		case "cursor":
			if values[0] == "" || len(values[0]) > 4096 {
				return false
			}
		case "fields":
			if len(values[0]) > 1024 {
				return false
			}
		case "after":
			if len(values[0]) > 256 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func unauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", "Bearer")
	c.JSON(401, gin.H{"error": "invalid or expired access token"})
}

func unavailable(c *gin.Context) { c.JSON(503, gin.H{"error": "upstream unavailable"}) }
