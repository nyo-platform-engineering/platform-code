package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/controller/common"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/view"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func List(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Endpoint("traces", slots, handler(store), common.WriteResponse)
}

func Detail(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Endpoint("detail", slots, handler(store), common.WriteResponse)
}

func RED(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Endpoint("red", slots, handler(store), writeBuckets)
}

func Attributes(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Endpoint("traces-keys", slots, handler(store), common.WriteResponse)
}

func handler(store query.QueryStore) common.RequestHandler {
	mock, isMock := store.(query.MockExecutor)
	var mockHandler common.RequestHandler
	if isMock {
		mockHandler = mock.Execute
	}
	return common.Handler(isMock, mockHandler, common.QueryHandler(store, model.CompileQueries))
}

func writeBuckets(c *gin.Context, request query.Request, result query.Result) {
	result.Data = view.Buckets(result.Data, request.Filter)
	common.WriteResponse(c, request, result)
}
