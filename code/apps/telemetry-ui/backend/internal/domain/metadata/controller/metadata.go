package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	view "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/view"
)

func Metadata(version string) gin.HandlerFunc {
	return func(c *gin.Context) {
		actor, _ := auth.FromContext(c.Request.Context())
		c.JSON(http.StatusOK, view.Metadata(version, actor))
	}
}
