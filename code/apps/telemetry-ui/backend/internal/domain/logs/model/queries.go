package model

import (
	"fmt"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func CompileQueries(request query.Request) ([]query.CompiledQuery, error) {
	switch request.Kind {
	case "logs", "logs-volume", "logs-keys":
		return query.Compile(request)
	default:
		return nil, fmt.Errorf("unsupported logs query kind: %q", request.Kind)
	}
}
