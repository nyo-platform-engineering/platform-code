package controller

import (
	"context"
	"time"

	"aat/aggregator/internal/model"
	"aat/internal/httpkit"

	"github.com/gin-gonic/gin"
)

func (ctrl *Controller) Ingest(source string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body map[string][]model.Record
		if err := httpkit.Decode(c.Writer, c.Request, &body); err != nil {
			c.JSON(400, map[string]string{"error": err.Error()})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		items, err := ctrl.IngestBatch(ctx, source, body)
		if err != nil {
			fail(c.Writer, c.Request, err)
			return
		}

		c.JSON(200, map[string]any{"changed": len(items), "items": items})
	}
}
