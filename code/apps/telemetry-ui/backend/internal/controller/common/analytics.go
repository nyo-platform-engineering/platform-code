package common

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	view "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/view/common"
)

type Operation struct {
	Execute   func(context.Context, []model.CompiledQuery) ([]map[string]any, error)
	Compile   func(model.Filter, string) ([]model.CompiledQuery, bool, error)
	Signal    string
	Discovery bool
	Services  bool
	Summary   bool
	Buckets   func([]map[string]any, model.Filter) []map[string]any
}

func Handler(store model.QueryStore, op Operation, slots chan struct{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		f, err := parseFilter(c)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
			return
		}
		if op.Discovery {
			f.DiscoveryScope = c.DefaultQuery("scope", map[bool]string{true: "log", false: "span"}[op.Signal == "log"])
			f.KeySearch = c.Query("keySearch")
			if (f.DiscoveryScope != "resource" && f.DiscoveryScope != map[bool]string{true: "log", false: "span"}[op.Signal == "log"]) || !utf8.ValidString(f.KeySearch) || utf8.RuneCountInString(f.KeySearch) > 128 || strings.ContainsFunc(f.KeySearch, unicode.IsControl) {
				c.JSON(400, gin.H{"error": "invalid_query", "message": "Invalid attribute scope or key search"})
				return
			}
			f.Attributes = nil
			f.Search = ""
			f.Limit = 50
			f.Offset = 0
		}
		if op.Services {
			f.Attributes = nil
		}
		for _, attribute := range f.Attributes {
			logs := op.Signal == "log"
			if (logs && attribute.Scope == "span") || (!logs && (attribute.Scope == "log" || attribute.Scope == "body")) {
				c.JSON(400, gin.H{"error": "invalid_query", "message": "attribute scope does not match this signal"})
				return
			}
		}
		actor, ok := auth.FromContext(c.Request.Context())
		if !ok || actor.Tenant == "" {
			c.JSON(403, gin.H{"error": "forbidden"})
			return
		}
		queries, list, err := op.Compile(f, actor.Tenant)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
			return
		}
		if op.Discovery {
			list = true
		}
		if c.Query("preview") == "1" {
			c.Header("Cache-Control", "no-store")
			c.JSON(200, gin.H{"queries": view.PreviewQueries(queries), "from": f.From, "to": f.To})
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-c.Request.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
			c.JSON(429, gin.H{"error": "too_many_queries"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		var data []map[string]any
		if op.Execute != nil {
			data, err = op.Execute(ctx, queries)
		} else {
			data, err = model.Execute(ctx, store, queries[0])
		}
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "query_unavailable", "message": "Query unavailable or resource limit reached. Narrow the time range or service filter and retry."})
			return
		}
		truncated := list && len(data) > f.Limit
		if truncated {
			data = data[:f.Limit]
		}
		body := gin.H{"data": data, "from": f.From, "to": f.To, "truncated": truncated}
		if truncated && f.Offset+f.Limit <= 5000 && !op.Discovery {
			body["nextOffset"] = f.Offset + f.Limit
		}
		if op.Summary {
			summary, err := model.Execute(ctx, store, queries[1])
			if err != nil {
				c.JSON(503, gin.H{"error": "query_unavailable"})
				return
			}
			if len(summary) > 0 {
				body["summary"] = summary[0]
			}
		}
		if op.Buckets != nil {
			body["data"] = op.Buckets(data, f)
		}
		c.JSON(200, body)
	}
}
