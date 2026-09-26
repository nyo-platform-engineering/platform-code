package main

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *server) health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	if s.store.Pool.Ping(ctx) != nil {
		c.JSON(503, map[string]string{"error": "store unavailable"})
		return
	}

	c.JSON(200, map[string]bool{"ok": true})
}
