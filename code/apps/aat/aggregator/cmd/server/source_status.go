package main

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *server) sourceStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	statuses, e := s.store.SourceStatuses(ctx)
	if e != nil {
		fail(c.Writer, c.Request, e)
		return
	}
	c.JSON(200, map[string]any{"sources": statuses})
}
