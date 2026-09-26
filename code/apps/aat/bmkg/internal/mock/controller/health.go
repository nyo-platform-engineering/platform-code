package controller

import "github.com/gin-gonic/gin"

func (ctrl *Controller) Health(c *gin.Context) {
	c.JSON(200, gin.H{"ok": true, "service": "BMKG"})
}
