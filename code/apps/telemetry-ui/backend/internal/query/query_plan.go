package query

import (
	"strings"
	"time"
)

type CompiledQuery struct {
	Name       string `json:"name"`
	SQL        string `json:"sql"`
	Args       []any  `json:"-"`
	Filter     Filter `json:"-"`
	Tenant     string `json:"-"`
	Table      Table  `json:"-"`
	ServerOnly bool   `json:"-"`
}

// PREWHERE reads scope/filter columns before loading bodies and evaluating JSON.
func (f Filter) conditions(tenant string, logs, serverOnly bool) (string, []any, string, []any) {
	pre, args := f.baseWhere(tenant, logs)
	suffix, lateArgs := f.lateWhere(logs)
	if logs {
		// Match the log table's leading sorting key, retaining exact nanosecond bounds above.
		// To is exclusive: subtract 1 ns before rounding so an aligned end excludes its next bucket.
		pre += " AND toStartOfFiveMinutes(Timestamp) >= toStartOfFiveMinutes(fromUnixTimestamp64Nano(?)) AND toStartOfFiveMinutes(Timestamp) <= toStartOfFiveMinutes(fromUnixTimestamp64Nano(?))"
		args = append(args, f.From.UnixNano(), f.To.Add(-time.Nanosecond).UnixNano())
	}
	if serverOnly {
		pre += " AND SpanKind = 'Server'"
	}
	return pre, args, strings.TrimPrefix(suffix, " AND "), lateArgs
}

func (f Filter) Conditions(tenant string, logs, serverOnly bool) (string, []any) {
	pre, args, late, values := f.conditions(tenant, logs, serverOnly)
	clause := "PREWHERE " + pre
	if late != "" {
		clause += " WHERE " + late
	}
	return clause, append(args, values...)
}
