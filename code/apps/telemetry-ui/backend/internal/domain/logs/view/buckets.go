package view

import (
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/view/common"
)

func Buckets(rows []map[string]any, f query.Filter) []map[string]any {
	return common.FillBuckets(rows, f, true)
}
