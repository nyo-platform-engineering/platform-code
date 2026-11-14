package model

import (
	"fmt"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func CompileQueries(request query.Request) ([]query.CompiledQuery, error) {
	if request.Operation == query.OperationServices && (request.Signal == query.SignalTraces || request.Signal == query.SignalLogs) {
		return query.Compile(request)
	}
	return nil, fmt.Errorf("unsupported metadata query: signal=%q operation=%q", request.Signal, request.Operation)
}
