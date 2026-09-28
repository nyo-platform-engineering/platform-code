package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/controller/common"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func Services(traceStore, logStore query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	traces := common.Endpoint(query.SignalTraces, query.OperationServices, slots, handler(traceStore), common.WriteResponse)
	logs := common.Endpoint(query.SignalLogs, query.OperationServices, slots, handler(logStore), common.WriteResponse)
	return func(c *gin.Context) {
		switch query.Signal(c.Query("signal")) {
		case query.SignalTraces:
			traces(c)
		case query.SignalLogs:
			logs(c)
		default:
			c.JSON(400, gin.H{"error": "invalid_query", "message": "Signal must be traces or logs"})
		}
	}
}

func handler(store query.QueryStore) common.RequestHandler {
	mock, isMock := store.(query.MockExecutor)
	var mockHandler common.RequestHandler
	if isMock {
		mockHandler = mock.Execute
	}
	return common.Handler(isMock, mockHandler, common.QueryHandler(store, model.CompileQueries))
}
