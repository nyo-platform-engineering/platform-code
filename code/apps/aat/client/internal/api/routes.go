package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"aat/internal/httpkit"
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
	// Never forward internal credentials through redirects.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u := upstream{config: config, http: &client}
	router := httpkit.Router()
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
		err := u.get(ctx, "POST", u.endpoint(config.AuthURL, "/internal/introspect"), config.AuthToken, correlation, map[string]string{"token": parts[1]}, &identity)
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
			c.JSON(400, gin.H{"error": "invalid query; use source, limit (1..1000), after, and fields"})
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
			Items []Hazard `json:"items"`
			Next  string   `json:"next_after"`
		}
		err = u.get(ctx, "GET", u.endpoint(config.AggregatorURL, "/internal/hazards")+"?"+q.Encode(), config.AggregatorToken, correlation, nil, &page)
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
		c.JSON(200, gin.H{"items": items, "next_after": page.Next, "sources": statuses})
	})
	return httpkit.Limit(config.MaxConcurrent, router), nil
}

func validQuery(q url.Values) bool {
	for key, values := range q {
		if len(values) != 1 {
			return false
		}
		switch key {
		case "source":
			if values[0] != "BMKG" && values[0] != "PVMBG" {
				return false
			}
		case "limit":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > 1000 {
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
