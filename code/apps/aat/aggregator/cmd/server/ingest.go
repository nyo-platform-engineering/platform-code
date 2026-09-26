package main

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"aat/aggregator/internal/aggregate"
	"aat/internal/httpkit"
)

func (s *server) ingest(source string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body map[string][]aggregate.Record
		if e := httpkit.Decode(c.Writer, c.Request, &body); e != nil {
			c.JSON(400, map[string]string{"error": e.Error()})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		items, e := s.store.Ingest(ctx, source, body)
		if e != nil {
			fail(c.Writer, c.Request, e)
			return
		}

		c.JSON(200, map[string]any{"changed": len(items), "items": items})
	}
}
