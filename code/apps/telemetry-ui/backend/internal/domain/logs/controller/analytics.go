package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/controller/common"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/view"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func List(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(store, common.Operation{Signal: "log", Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "logs", tenant)
	}}, slots)
}
func Volume(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(store, common.Operation{Signal: "log", Buckets: view.Buckets, Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "logs-volume", tenant)
	}}, slots)
}
func Attributes(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(store, common.Operation{Signal: "log", Discovery: true, Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "logs-keys", tenant)
	}}, slots)
}
