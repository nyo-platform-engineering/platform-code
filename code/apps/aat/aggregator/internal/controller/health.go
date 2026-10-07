package controller

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

func (ctrl *Controller) Health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	if ctrl.Ping(ctx) != nil {
		c.JSON(503, map[string]string{"error": "store unavailable"})
		return
	}

	c.JSON(200, map[string]bool{"ok": true})
}
