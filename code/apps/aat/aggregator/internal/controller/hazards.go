package controller

import (
	"context"
	"strconv"
	"time"

	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"errors"

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

	if err != nil || limit < 1 || limit > 1000 || (source != "" && !model.ValidSource(source)) {
		c.JSON(400, map[string]string{"error": "invalid source or limit (1..1000)"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	if query.Get("order") != "" && query.Get("order") != "id" && query.Get("order") != "recent" {
		c.JSON(400, map[string]string{"error": "order must be id or recent"})
		return
	}
	q := storage.HazardQuery{Source: source, After: query.Get("after"), Cursor: query.Get("cursor"), Order: query.Get("order"), Limit: limit}
	if len(q.Cursor) > 4096 || len(q.After) > 256 || (q.Cursor != "" && q.Order != "recent") || (q.Order == "recent" && q.After != "") {
		c.JSON(400, map[string]string{"error": "invalid cursor or mixed pagination parameters"})
		return
	}
	if pager, ok := ctrl.HazardStore().(storage.Pager); ok {
		page, err := pager.Page(ctx, q)
		if errors.Is(err, storage.ErrInvalidCursor) {
			c.JSON(400, map[string]string{"error": "invalid cursor"})
			return
		}
		if err != nil {
			fail(c.Writer, c.Request, err)
			return
		}
		c.JSON(200, page)
		return
	}
	items, err := ctrl.List(ctx, source, q.After, limit)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	c.JSON(200, map[string]any{"items": items, "next_after": next, "partial": false, "unavailable_units": []string{}})
}
