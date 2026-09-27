package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/controller/common"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/view"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func List(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(store, common.Operation{Signal: "span", Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "traces", tenant)
	}}, slots)
}
func Detail(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(store, common.Operation{Signal: "span", Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "detail", tenant)
	}}, slots)
}
func RED(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(store, common.Operation{Signal: "span", Summary: true, Buckets: view.Buckets, Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "red", tenant)
	}}, slots)
}
func Attributes(store query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(store, common.Operation{Signal: "span", Discovery: true, Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "traces-keys", tenant)
	}}, slots)
}
