import { formatNumber, type TelemetryData } from '../../_shared/telemetry/types'
import { Summary } from '../../_shared/telemetry/SummaryMetric'

export function LogSummary({ data }: { data?: TelemetryData }) {
  const total = data?.logBuckets.reduce((sum, bucket) => sum + bucket.records, 0)
  const errors = data?.logBuckets
    .filter((bucket) => bucket.severity === 'error' || bucket.severity === 'fatal')
    .reduce((sum, bucket) => sum + bucket.records, 0)
  const warnings = data?.logBuckets
    .filter((bucket) => bucket.severity === 'warn')
    .reduce((sum, bucket) => sum + bucket.records, 0)
  return (
    <div className="grid grid-cols-3 divide-x divide-border rounded-ui border border-border bg-surface">
      <Summary
        label="Log records"
        value={formatNumber(total, 0)}
        detail="Across the selected window"
        tone="info"
      />
      <Summary
        label="Errors & fatal"
        value={formatNumber(errors, 0)}
        detail="Records requiring attention"
        tone="error"
      />
      <Summary
        label="Warnings"
        value={formatNumber(warnings, 0)}
        detail="Warning-level records"
        tone="warn"
      />
    </div>
  )
}
