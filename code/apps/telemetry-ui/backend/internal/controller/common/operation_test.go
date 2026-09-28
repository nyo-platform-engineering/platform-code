package common

import (
	"github.com/gin-gonic/gin"
	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	view "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/view/common"
)

func analyticsHandler(store model.QueryStore, signal model.Signal, operation model.Operation, slots chan struct{}) gin.HandlerFunc {
	compiler := traces.CompileQueries
	if signal == model.SignalLogs {
		compiler = logs.CompileQueries
	}
	if operation == model.OperationServices {
		compiler = metadata.CompileQueries
	}
	mock, isMock := store.(model.MockExecutor)
	var mockHandler RequestHandler
	if isMock {
		mockHandler = mock.Execute
	}
	execute := Handler(isMock, mockHandler, QueryHandler(store, compiler))
	respond := WriteResponse

	if operation == model.OperationMetrics {
		respond = func(c *gin.Context, request model.Request, result model.Result) {
			result.Data = view.FillBuckets(result.Data, request.Filter, signal == model.SignalLogs)
			WriteResponse(c, request, result)
		}
	}

	return Endpoint(signal, operation, slots, execute, respond)
}
