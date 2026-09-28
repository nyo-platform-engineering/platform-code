package queryinternal

// Keep SQL expressions here. Request values belong in bound parameters.
const severitySQL = "multiIf(" +
	"SeverityNumber>=21,'fatal'," +
	"SeverityNumber>=17,'error'," +
	"SeverityNumber>=13,'warn'," +
	"SeverityNumber>=9,'info'," +
	"SeverityNumber>=5,'debug'," +
	"SeverityNumber>=1,'trace','unspecified')"
const redFields = "count() AS requests, " +
	"countIf(StatusCode = 'Error') AS errors, " +
	"if(count() = 0, 0, countIf(StatusCode = 'Error') / count()) AS errorRate, " +
	"if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[1]/1000000) AS p50Ms, " +
	"if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[2]/1000000) AS p90Ms, " +
	"if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[3]/1000000) AS p95Ms, " +
	"if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[4]/1000000) AS p99Ms"

type selection struct {
	columns, group, order string
}

func selectionFor(r Request) selection {
	switch r.Kind {
	case "traces":
		return selection{
			columns: "Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ServiceName AS service, SpanName AS name, Duration/1000000 AS durationMs, StatusCode AS status",
			order:   "Timestamp DESC, TraceId, SpanId",
		}
	case "detail":
		return selection{
			columns: "Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, " +
				"ParentSpanId AS parentSpanId, ServiceName AS service, " +
				"SpanName AS name, Duration/1000000 AS durationMs, StatusCode AS status, " +
				"StatusMessage AS message, SpanAttributes AS attributes",
			order: "Timestamp, SpanId",
		}
	case "logs":
		return selection{
			columns: "Timestamp AS timestamp, TraceId AS traceId, SpanId AS spanId, ServiceName AS service, " + severitySQL + " AS severity, Body AS body, LogAttributes AS attributes, ResourceAttributes AS resourceAttributes",
			order:   "Timestamp DESC, TraceId, SpanId, Body",
		}
	case "red":
		return selection{
			columns: "toStartOfMinute(Timestamp) AS bucket, " + redFields,
			group:   "bucket",
			order:   "bucket",
		}
	case "logs-volume":
		return selection{
			columns: "toStartOfMinute(Timestamp) AS bucket, " + severitySQL + " AS severity, count() AS records",
			group:   "bucket,severity",
			order:   "bucket,severity",
		}
	case "traces-keys", "logs-keys":
		columns := "DISTINCT arrayJoin(mapKeys(ResourceAttributes)) AS key"
		switch r.Filter.DiscoveryScope {
		case "span":
			columns = "DISTINCT arrayJoin(mapKeys(SpanAttributes)) AS key"
		case "log":
			columns = "DISTINCT arrayJoin(mapKeys(LogAttributes)) AS key"
		}
		return selection{
			columns: columns,
			order:   "key",
		}
	case "trace-services", "log-services":
		return selection{
			columns: "DISTINCT ServiceName AS service",
			order:   "service",
		}
	default:
		return selection{}
	}
}

// Compile returns SQL and bound arguments for a validated request.
func Compile(request Request) ([]CompiledQuery, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Kind == "services" {
		if request.Signal == "traces" {
			request.Kind = "trace-services"
			return Compile(request)
		}
		if request.Signal == "logs" {
			request.Kind = "log-services"
			return Compile(request)
		}
		request.Kind = "trace-services"
		traces, err := Compile(request)
		if err != nil {
			return nil, err
		}
		request.Kind = "log-services"
		logs, err := Compile(request)
		if err != nil {
			return nil, err
		}
		return append(traces, logs...), nil
	}
	services := request.Kind == "trace-services" || request.Kind == "log-services"
	if services {
		request.Filter = ServiceFilter(request.Filter)
	}
	f := request.Filter
	spec := selectionFor(request)
	builder, err := selectQuery(request.Table(), spec.columns, f, request.Scope(), request.ServerOnly())
	if err != nil {
		return nil, err
	}
	builder.groupBy(spec.group).orderBy(spec.order)
	switch {
	case request.Kind == "traces-keys" || request.Kind == "logs-keys":
		builder.containsKey(f.KeySearch).page(51, 0).withSearchBudget()
	case services:
		builder.page(500, 0)
	case request.List():
		builder.page(f.Limit+1, f.Offset)
	}
	compiled, err := builder.compile(request.Kind)
	if err != nil {
		return nil, err
	}
	result := []CompiledQuery{compiled}
	if request.Kind == "red" {
		summary, err := selectQuery(Traces, redFields, f, request.Scope(), true)
		if err != nil {
			return nil, err
		}
		compiled, err := summary.compile("red-summary")
		if err != nil {
			return nil, err
		}
		result = append(result, compiled)
	}
	return result, nil
}
