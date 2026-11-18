package controller

import (
	"github.com/gin-gonic/gin"

	"aat/internal/httpkit"
)

type stateRequest struct {
	Enabled *bool `json:"enabled"`
}

func (ctrl *Controller) SetOutage(c *gin.Context) {
	ctrl.setState(c, ctrl.Feed.SetOutage)
}

func (ctrl *Controller) SetSchemaVersion(c *gin.Context) {
	var body struct {
		Enabled *bool  `json:"enabled"`
		Mode    string `json:"mode"`
	}
	if err := httpkit.Decode(c.Writer, c.Request, &body); err != nil || (body.Enabled == nil) == (body.Mode == "") {
		c.JSON(400, gin.H{"error": "provide either mode or enabled"})
		return
	}
	if body.Enabled != nil {
		body.Mode = "original"
		if *body.Enabled {
			body.Mode = "additive"
		}
	}
	if !ctrl.Feed.SetSchemaMode(body.Mode) {
		c.JSON(400, gin.H{"error": "unknown schema mode"})
		return
	}
	c.JSON(200, ctrl.Feed.State())
}

func (ctrl *Controller) setState(c *gin.Context, set func(bool)) {
	var body stateRequest
	if err := httpkit.Decode(c.Writer, c.Request, &body); err != nil || body.Enabled == nil {
		c.JSON(400, gin.H{"error": "enabled boolean required"})
		return
	}

	set(*body.Enabled)
	c.JSON(200, ctrl.Feed.State())
}
