package controller

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/controller/common"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func Services(traceStore, logStore query.QueryStore, slots chan struct{}) gin.HandlerFunc {
	return common.Handler(traceStore, common.Operation{Signal: "", Services: true, Execute: func(ctx context.Context, queries []query.CompiledQuery) ([]map[string]any, error) {
		return model.ExecuteServices(ctx, traceStore, logStore, queries)
	}, Compile: func(f query.Filter, tenant string) ([]query.CompiledQuery, bool, error) {
		return model.CompileQueries(f, "services", tenant)
	}}, slots)
}
