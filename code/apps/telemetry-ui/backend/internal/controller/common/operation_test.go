package common

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	view "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/view/common"
)

func analyticsHandler(store model.QueryStore, kind string, slots chan struct{}) gin.HandlerFunc {
	compiler := traces.CompileQueries
	signal := "span"
	if strings.HasPrefix(kind, "logs") {
		compiler = logs.CompileQueries
		signal = "log"
	}
	if kind == "services" {
		compiler = metadata.CompileQueries
	}
	mock, isMock := store.(model.MockExecutor)
	var mockHandler RequestHandler
	if isMock {
		mockHandler = mock.Execute
	}
	execute := Handler(isMock, mockHandler, QueryHandler(store, compiler))
	respond := WriteResponse

	if kind == "services" {
		execute = func(ctx context.Context, request model.Request) (model.Result, error) {
			request.Kind = "trace-services"
			traces, err := QueryHandler(store, metadata.CompileQueries)(ctx, request)
			if err != nil {
				return model.Result{}, err
			}
			request.Kind = "log-services"
			logs, err := QueryHandler(store, metadata.CompileQueries)(ctx, request)
			if err != nil {
				return model.Result{}, err
			}
			return model.Result{Data: metadata.MergeServices(traces.Data, logs.Data)}, nil
		}
	}
	if kind == "red" || kind == "logs-volume" {
		respond = func(c *gin.Context, request model.Request, result model.Result) {
			result.Data = view.FillBuckets(result.Data, request.Filter, signal == "log")
			WriteResponse(c, request, result)
		}
	}

	return Endpoint(kind, slots, execute, respond)
}
