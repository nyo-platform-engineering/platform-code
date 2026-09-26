package controller

import (
	"time"

	"github.com/gin-gonic/gin"

	"aat/internal/httpkit"
)

func (ctrl *Controller) SeismicEvents(c *gin.Context) {
	since, err := httpkit.Since(c.Request)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid since"})
		return
	}

	result := ctrl.Feed.SeismicEvents(since, time.Now())
	if httpkit.Sleep(c.Request, ctrl.Delay) {
		c.JSON(200, result)
	}
}
