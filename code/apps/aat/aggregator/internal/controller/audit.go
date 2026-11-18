package controller

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"github.com/gin-gonic/gin"
)

func auditError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// AuditAuth is separate from the Aggregator/client tokens. An absent token
// disables audit access, allowing existing M1 setups to start safely.
func AuditAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			auditError(c, 503, "unavailable", "audit token is not configured")
			return
		}
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+token)) != 1 {
			auditError(c, 401, "unauthorized", "invalid audit token")
			return
		}
		c.Next()
	}
}

func auditQuery(c *gin.Context) (storage.EventQuery, bool) {
	values := c.Request.URL.Query()
	q := storage.EventQuery{Source: values.Get("source"), Limit: 100, Cursor: values.Get("cursor")}
	if values.Has("source") && !model.ValidSource(q.Source) {
		auditError(c, 400, "bad_request", "invalid source")
		return q, false
	}
	if values.Has("limit") {
		n, err := strconv.Atoi(values.Get("limit"))
		if err != nil || n < 1 || n > 1000 {
			auditError(c, 400, "bad_request", "limit must be between 1 and 1000")
			return q, false
		}
		q.Limit = n
	}
	if values.Has("since") {
		t, err := time.Parse(time.RFC3339Nano, values.Get("since"))
		if err != nil {
			auditError(c, 400, "bad_request", "since must be an ISO 8601 timestamp")
			return q, false
		}
		q.Since = t.UTC()
	}
	return q, true
}

func auditFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalidCursor):
		auditError(c, 400, "bad_request", err.Error())
	case errors.Is(err, storage.ErrNotFound):
		auditError(c, 404, "not_found", "hazard not found")
	default:
		slog.Error("audit operation failed", "error", err, "correlation_id", c.GetHeader("X-Correlation-ID"))
		auditError(c, 503, "unavailable", "audit storage unavailable or not configured")
	}
}

func (ctrl *Controller) AuditEvents(c *gin.Context) {
	q, ok := auditQuery(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	page, err := ctrl.HazardStore().Events(ctx, q)
	if err != nil {
		auditFailure(c, err)
		return
	}
	c.JSON(200, page)
}

func (ctrl *Controller) AuditChecksum(c *gin.Context) {
	q, ok := auditQuery(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	result, err := ctrl.HazardStore().Checksum(ctx, q.Source)
	if err != nil {
		auditFailure(c, err)
		return
	}
	c.JSON(200, result)
}

func (ctrl *Controller) AuditLocation(c *gin.Context) {
	id := c.Query("hazard_id")
	if id == "" {
		auditError(c, 400, "bad_request", "hazard_id is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	result, err := ctrl.HazardStore().Locations(ctx, id)
	if err != nil {
		auditFailure(c, err)
		return
	}
	c.JSON(200, result)
}
