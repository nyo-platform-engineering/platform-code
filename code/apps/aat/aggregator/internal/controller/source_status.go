package controller

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

func (ctrl *Controller) SourceStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	statuses, err := ctrl.ListSourceStatuses(ctx)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	c.JSON(200, map[string]any{"sources": statuses})
}
