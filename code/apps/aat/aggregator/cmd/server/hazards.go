package main

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *server) hazards(c *gin.Context) {
	q := c.Request.URL.Query()
	source := q.Get("source")
	limit := 100
	var e error
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
	}

	if e != nil || limit < 1 || limit > 1000 || (source != "" && source != "BMKG" && source != "PVMBG") {
		c.JSON(400, map[string]string{"error": "invalid source or limit (1..1000)"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	items, e := s.store.List(ctx, source, q.Get("after"), limit)
	if e != nil {
		fail(c.Writer, c.Request, e)
		return
	}

	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}

	c.JSON(200, map[string]any{"items": items, "next_after": next})
}
