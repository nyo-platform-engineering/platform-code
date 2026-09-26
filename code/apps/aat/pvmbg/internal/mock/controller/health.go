package controller

import "github.com/gin-gonic/gin"

func (ctrl *Controller) Health(c *gin.Context) {
	outage := ctrl.Feed.IsOutage()
	code := 200
	if outage {
		code = 503
	}

	c.JSON(code, gin.H{"ok": !outage, "service": "PVMBG"})
}
