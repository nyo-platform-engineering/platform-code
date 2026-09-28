package common

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	view "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/view/common"
)

type RequestHandler func(context.Context, model.Request) (model.Result, error)

func Handler(isMock bool, mockHandler, queryHandler RequestHandler) RequestHandler {
	if isMock {
		return mockHandler
	}
	return queryHandler
}

// Endpoint handles HTTP parsing, previews, and resource limits.
func Endpoint(kind string, slots chan struct{}, execute RequestHandler, respond Responder) gin.HandlerFunc {
	return func(c *gin.Context) {
		request, ok := ParseRequest(c, kind)
		if !ok {
			return
		}
		f := request.Filter
		if c.Query("preview") == "1" {
			queries, err := model.Compile(request)
			if err != nil {
				c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
				return
			}
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
		result, err := execute(ctx, request)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "query_unavailable", "message": "Query unavailable or resource limit reached. Narrow the time range or service filter and retry."})
			return
		}

		respond(c, request, result)
	}
}

func QueryHandler(store model.QueryStore, compile model.Compiler) RequestHandler {
	return func(ctx context.Context, request model.Request) (model.Result, error) {
		ctx = model.WithDataSource(ctx, request.DataSourceID)
		queries, err := compile(request)
		if err != nil {
			return model.Result{}, err
		}
		result := model.Result{}
		for i, q := range queries {
			rows, err := model.Execute(ctx, store, q)
			if err != nil {
				return model.Result{}, err
			}
			if i == 0 {
				result.Data = rows
			} else {
				result.Summary = rows
			}
		}
		return result, nil
	}
}
