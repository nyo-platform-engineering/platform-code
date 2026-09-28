package controller

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/controller/common"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func Services(traceStore, logStore query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	traces, logs := handler(traceStore), handler(logStore)
	execute := func(ctx context.Context, request query.Request) (query.Result, error) {
		request.Filter = query.ServiceFilter(request.Filter)
		if request.Signal == "traces" {
			request.Kind = "trace-services"
			return traces(ctx, request)
		}
		if request.Signal == "logs" {
			request.Kind = "log-services"
			return logs(ctx, request)
		}
		request.Kind = "trace-services"
		traceResult, err := traces(ctx, request)
		if err != nil {
			return query.Result{}, err
		}
		request.Kind = "log-services"
		logResult, err := logs(ctx, request)
		if err != nil {
			return query.Result{}, err
		}
		return query.Result{Data: model.MergeServices(traceResult.Data, logResult.Data)}, nil
	}
	return common.Endpoint("services", slots, execute, common.WriteResponse)
}

func handler(store query.QueryStore) common.RequestHandler {
	mock, isMock := store.(query.MockExecutor)
	var mockHandler common.RequestHandler
	if isMock {
		mockHandler = mock.Execute
	}
	return common.Handler(isMock, mockHandler, common.QueryHandler(store, model.CompileQueries))
}
