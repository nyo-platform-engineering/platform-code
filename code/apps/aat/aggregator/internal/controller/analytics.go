package controller

import (
	"context"
	"net/url"
	"time"

	"aat/aggregator/internal/storage"
	"aat/internal/analytics"

	"github.com/gin-gonic/gin"
)

func (ctrl *Controller) Analytics(c *gin.Context) {
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
	store, ok := ctrl.HazardStore().(storage.AnalyticsReader)
	if !ok {
		c.JSON(503, gin.H{"error": "analytics storage unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2200*time.Millisecond)
	defer cancel()
	result, err := store.Analytics(ctx, q)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, result)
}
