package common

import (
	"context"
	"github.com/gin-gonic/gin"
	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	view "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/view/common"
	"strings"
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
	op := Operation{Signal: signal, Discovery: strings.HasSuffix(kind, "-keys"), Services: kind == "services", Summary: kind == "red", Compile: func(f model.Filter, tenant string) ([]model.CompiledQuery, bool, error) {
		return compiler(f, kind, tenant)
	}}
	if kind == "services" {
		op.Execute = func(ctx context.Context, queries []model.CompiledQuery) ([]map[string]any, error) {
			return metadata.ExecuteServices(ctx, store, store, queries)
		}
	}
	if kind == "red" || kind == "logs-volume" {
		op.Buckets = func(rows []map[string]any, f model.Filter) []map[string]any {
			return view.FillBuckets(rows, f, signal == "log")
		}
	}
	return Handler(store, op, slots)
}
