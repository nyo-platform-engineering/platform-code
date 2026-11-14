package model

import (
	"fmt"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func CompileQueries(request query.Request) ([]query.CompiledQuery, error) {
	if request.Signal == query.SignalLogs {
		switch request.Operation {
		case query.OperationRecords, query.OperationMetrics, query.OperationAttributes:
			return query.Compile(request)
		}
	}
	return nil, fmt.Errorf("unsupported logs query: signal=%q operation=%q", request.Signal, request.Operation)
}
