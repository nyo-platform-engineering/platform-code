package model

import (
	"fmt"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func CompileQueries(request query.Request) ([]query.CompiledQuery, error) {
	switch request.Kind {
	case "traces", "detail", "red", "traces-keys":
		return query.Compile(request)
	default:
		return nil, fmt.Errorf("unsupported traces query kind: %q", request.Kind)
	}
}
