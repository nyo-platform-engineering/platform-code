package controller

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"errors"

	"github.com/gin-gonic/gin"
)

// CacheFillPolicy identifies releases safe for repeated write invalidations.
const CacheFillPolicy = "coalesced-until-deadline-v2"

func (ctrl *Controller) Hazards(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	q, ok := hazardQuery(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	ctrl.hazardPage(c, ctx, q)
}

// Cache hits need a small revision lookup, not a distributed-query admission
// slot. The outer HTTP cap and sql.DB pool still bound their concurrency.
func (ctrl *Controller) CachedHazards(c *gin.Context) {
	c.Header("X-Hazard-Cache-Policy", CacheFillPolicy)
	q, ok := hazardQuery(c)
	if !ok {
		c.Abort()
		return
	}
	versions, versioned := ctrl.HazardStore().(storage.RevisionReader)
	if !versioned || ctrl.PageCache == nil || q.Order != "recent" || q.Cursor != "" || q.After != "" || q.Limit > 100 {
		c.Next()
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	// Writes can invalidate consecutive fills. Keep followers coalesced until
	// a current page is available or the existing request deadline expires;
	// falling through after a fixed retry count stampedes the read-slot gate.
	for {
		revision, err := ctrl.PageCache.readRevision(ctx, versions)
		if err != nil {
			break
		}
		if cached := ctrl.PageCache.get(q, revision); cached != nil {
			c.Header("Cache-Control", "no-store")
			c.Header("X-Hazard-Cache", "hit")
			c.Abort()
			cached.write(c)
			return
		}
		// One reader fills an identical cold page; followers wait instead of
		// stampeding Citus or receiving 429 while the same result is computed.
		done, leader := ctrl.PageCache.beginFill(q)
		if leader {
			defer ctrl.PageCache.endFill(q)
			c.Next()
			return
		}
		select {
		case <-done:
			// Revalidate after waiting; never serve a completed fill blindly.
		case <-ctx.Done():
			c.Next()
			return
		}
	}
	c.Next()
}

func hazardQuery(c *gin.Context) (storage.HazardQuery, bool) {
	query := c.Request.URL.Query()
	source := query.Get("source")
	limit := 100
	var err error
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
	}

	if err != nil || limit < 1 || limit > 1000 || (source != "" && !model.ValidSource(source)) {
		c.JSON(400, map[string]string{"error": "invalid source or limit (1..1000)"})
		return storage.HazardQuery{}, false
	}

	if query.Get("order") != "" && query.Get("order") != "id" && query.Get("order") != "recent" {
		c.JSON(400, map[string]string{"error": "order must be id or recent"})
		return storage.HazardQuery{}, false
	}
	q := storage.HazardQuery{Source: source, After: query.Get("after"), Cursor: query.Get("cursor"), Order: query.Get("order"), Limit: limit}
	if len(q.Cursor) > 4096 || len(q.After) > 256 || (q.Cursor != "" && q.Order != "recent") || (q.Order == "recent" && q.After != "") {
		c.JSON(400, map[string]string{"error": "invalid cursor or mixed pagination parameters"})
		return q, false
	}
	return q, true
}

func (ctrl *Controller) hazardPage(c *gin.Context, ctx context.Context, q storage.HazardQuery) {
	if pager, ok := ctrl.HazardStore().(storage.Pager); ok {
		versions, versioned := pager.(storage.RevisionReader)
		cacheable := versioned && ctrl.PageCache != nil && q.Order == "recent" && q.Cursor == "" && q.After == "" && q.Limit <= 100
		var revision int64
		if cacheable {
			var err error
			revision, err = versions.Revision(ctx)
			cacheable = err == nil
			if cacheable {
				if cached := ctrl.PageCache.get(q, revision); cached != nil {
					c.Header("X-Hazard-Cache", "hit")
					cached.write(c)
					return
				}
			}
		}
		page, err := pager.Page(ctx, q)
		if errors.Is(err, storage.ErrInvalidCursor) {
			c.JSON(400, map[string]string{"error": "invalid cursor"})
			return
		}
		if err != nil {
			fail(c.Writer, c.Request, err)
			return
		}
		if cacheable && !page.Partial && len(page.UnavailableUnits) == 0 {
			// Do not cache a page if a writer committed during its distributed read.
			if after, err := versions.Revision(ctx); err == nil && after == revision {
				if body, err := json.Marshal(page); err == nil {
					if cached := ctrl.PageCache.put(q, revision, body); cached != nil {
						c.Header("X-Hazard-Cache", "miss")
						cached.write(c)
						return
					}
				}
			}
		}
		c.JSON(200, page)
		return
	}
	items, err := ctrl.List(ctx, q.Source, q.After, q.Limit)
	if err != nil {
		fail(c.Writer, c.Request, err)
		return
	}
	next := ""
	if len(items) == q.Limit {
		next = items[len(items)-1].ID
	}
	c.JSON(200, map[string]any{"items": items, "next_after": next, "partial": false, "unavailable_units": []string{}})
}
