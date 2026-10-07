package controller

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

func (ctrl *Controller) Hazards(c *gin.Context) {
	query := c.Request.URL.Query()
	source := query.Get("source")
	limit := 100
	var err error
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
	}

	if err != nil || limit < 1 || limit > 1000 || (source != "" && source != "BMKG" && source != "PVMBG") {
		c.JSON(400, map[string]string{"error": "invalid source or limit (1..1000)"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	items, err := ctrl.List(ctx, source, query.Get("after"), limit)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}

	nextAfter := ""
	if len(items) == limit {
		nextAfter = items[len(items)-1].ID
	}

	c.JSON(200, map[string]any{"items": items, "next_after": nextAfter})
}
