import { SEVERITIES, type LogBucket, type TraceBucket } from './types'

export type TraceMetric = 'requests' | 'errors' | 'duration'
export type ChartSeries = { key: string; label: string; color: string; dash?: string }
export type ChartPoint = {
  timestamp: string
  partial: boolean
  values: Record<string, number | null>
  trace?: TraceBucket
}

export const SEVERITY_SERIES: ChartSeries[] = SEVERITIES.map((severity) => ({
  key: severity,
  label: severity === 'unspecified' ? 'Unspecified' : severity[0].toUpperCase() + severity.slice(1),
  color: `var(--chart-${severity})`,
}))

export const TRACE_METRICS: Record<
  TraceMetric,
  { label: string; unit: string; series: ChartSeries[] }
> = {
  requests: {
    label: 'Request frequency',
    unit: 'requests / min',
    series: [{ key: 'requests', label: 'Requests', color: 'var(--chart-info)' }],
  },
  errors: {
    label: 'Error rate',
    unit: 'percent',
    series: [{ key: 'errorPercent', label: 'Error rate', color: 'var(--chart-error)' }],
  },
  duration: {
    label: 'Latency percentiles',
    unit: 'milliseconds',
    series: [
      { key: 'p50Ms', label: 'P50', color: 'var(--chart-p50)', dash: '3 4' },
      { key: 'p90Ms', label: 'P90', color: 'var(--chart-p90)', dash: '5 3' },
      { key: 'p95Ms', label: 'P95', color: 'var(--chart-p95)' },
      { key: 'p99Ms', label: 'P99', color: 'var(--chart-p99)', dash: '7 3' },
    ],
  },
}

export function tracePoints(buckets: TraceBucket[]): ChartPoint[] {
  return buckets.map((bucket) => ({
    timestamp: bucket.bucket,
    partial: bucket.partial,
    trace: bucket,
    values: {
      requests: bucket.requests,
      errors: bucket.errors,
      errorPercent: bucket.requests ? bucket.errorRate * 100 : null,
      p50Ms: bucket.p50Ms,
      p90Ms: bucket.p90Ms,
      p95Ms: bucket.p95Ms,
      p99Ms: bucket.p99Ms,
    },
  }))
}

export function logPoints(buckets: LogBucket[]): ChartPoint[] {
  const grouped = new Map<string, ChartPoint>()
  for (const bucket of buckets) {
    let point = grouped.get(bucket.bucket)
    if (!point) {
      point = { timestamp: bucket.bucket, partial: bucket.partial, values: {} }
      grouped.set(bucket.bucket, point)
    }
    point.values[bucket.severity] = bucket.records
  }
  return [...grouped.values()].sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp))
}

/** A readable four-step axis; count axes never imply fractional events. */
export function axisTicks(maximum: number, integer: boolean): number[] {
  if (maximum <= 0) return [0, 1]
  const rawStep = maximum / 4
  const magnitude = 10 ** Math.floor(Math.log10(rawStep))
  const normalized = rawStep / magnitude
  const step = [1, 2, 2.5, 5, 10].find((candidate) => candidate >= normalized)! * magnitude
  const interval = integer ? Math.max(1, Math.ceil(step)) : step
  const count = Math.ceil(maximum / interval)
  return Array.from({ length: count + 1 }, (_, index) => index * interval)
}

/** Missing latency buckets start a new segment instead of drawing a false zero. */
export function linePath(
  points: ChartPoint[],
  key: string,
  x: (index: number) => number,
  y: (value: number) => number,
) {
  let connected = false
  return points
    .map((point, index) => {
      const value = point.values[key]
      if (value == null) {
        connected = false
        return ''
      }
      const command = connected ? 'L' : 'M'
      connected = true
      return `${command}${x(index).toFixed(2)},${y(value).toFixed(2)}`
    })
    .join(' ')
}

/** Total volume is independent of which severity series are visible in the other chart. */
export function logVolumePoints(buckets: LogBucket[]): ChartPoint[] {
  return logPoints(buckets).map((point) => ({
    ...point,
    values: {
      records: Object.values(point.values).reduce<number>((sum, value) => sum + (value ?? 0), 0),
    },
  }))
}

export const LOG_VOLUME_SERIES: ChartSeries[] = [
  { key: 'records', label: 'Total records', color: 'var(--chart-info)' },
]
