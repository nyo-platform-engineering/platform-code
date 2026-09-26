package controller

import (
	"math/rand/v2"
	"time"

	"github.com/gin-gonic/gin"

	"aat/internal/httpkit"
)

func (ctrl *Controller) VolcanicReports(c *gin.Context) {
	if ctrl.Feed.IsOutage() {
		c.JSON(503, map[string]string{"error": "simulated outage"})
		return
	}

	since, e := httpkit.Since(c.Request)
	if e != nil {
		c.JSON(400, map[string]string{"error": "invalid since"})
		return
	}

	result := ctrl.Feed.VolcanicReports(since, time.Now())

	delay := ctrl.MinDelay
	if ctrl.MaxDelay > ctrl.MinDelay {
		delay += time.Duration(rand.Int64N(int64(ctrl.MaxDelay-ctrl.MinDelay) + 1))
	}

	if httpkit.Sleep(c.Request, delay) {
		if ctrl.Feed.IsOutage() {
			c.JSON(503, map[string]string{"error": "simulated outage"})
			return
		}

		c.JSON(200, result)
	}
}
