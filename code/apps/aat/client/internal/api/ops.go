package api

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
)

func (u upstream) ops(c *gin.Context) {
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
	if err := u.get(ctx, "POST", u.endpoint(u.config.AuthURL, "/internal/introspect"), u.config.AuthToken, correlation, map[string]string{"token": parts[1]}, &identity); err != nil {
		unavailable(c)
		return
	}
	if !identity.Active || !identityKnown(identity.ID) {
		unauthorized(c)
		return
	}
	if identity.ID != "analyst" {
		c.JSON(403, gin.H{"error": "Internal Ops access required"})
		return
	}
	endpoint := u.endpoint(u.config.AggregatorURL, "/internal"+c.Request.URL.Path)
	if c.Request.URL.RawQuery != "" {
		endpoint += "?" + c.Request.URL.RawQuery
	}
	var result map[string]any
	if err := u.get(ctx, c.Request.Method, endpoint, u.config.AggregatorToken, correlation, nil, &result); err != nil {
		var status upstreamStatusError
		if errors.As(err, &status) && (status.Status == 400 || status.Status == 404 || status.Status == 409) {
			c.JSON(status.Status, gin.H{"error": "invalid operation or message state"})
			return
		}
		unavailable(c)
		return
	}
	c.JSON(200, result)
}
