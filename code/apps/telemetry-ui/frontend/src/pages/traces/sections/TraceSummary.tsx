import { formatNumber, type TelemetryData } from '../../_shared/telemetry/types'
import { Summary } from '../../_shared/telemetry/SummaryMetric'

export function TraceSummary({ data }: { data?: TelemetryData }) {
  const summary = data?.summary
  const minutes = data ? (Date.parse(data.to) - Date.parse(data.from)) / 60_000 : 0
  return (
    <div className="grid grid-cols-3 divide-x divide-border rounded-ui border border-border bg-surface">
      <Summary
        label="Request rate"
        value={summary && minutes ? formatNumber(summary.requests / minutes, 2) : '—'}
        unit="req/min"
        detail={`${formatNumber(summary?.requests, 0)} requests in this window`}
        tone="info"
      />
      <Summary
        label="Error rate"
        value={summary ? formatNumber(summary.errorRate * 100, 2) : '—'}
        unit="%"
        detail={`${formatNumber(summary?.errors, 0)} failed requests`}
        tone="error"
      />
      <Summary
        label="P95 duration"
        value={formatNumber(summary?.p95Ms)}
        unit="ms"
        detail={`P50 ${formatNumber(summary?.p50Ms)} ms · P99 ${formatNumber(summary?.p99Ms)} ms`}
        tone="p95"
      />
    </div>
  )
}
