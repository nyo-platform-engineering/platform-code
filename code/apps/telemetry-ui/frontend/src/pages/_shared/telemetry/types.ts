export type TelemetryMode = 'traces' | 'logs'
export type Severity = 'unspecified' | 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export type TraceSummary = {
  requests: number
  errors: number
  errorRate: number
  p50Ms: number | null
  p90Ms: number | null
  p95Ms: number | null
  p99Ms: number | null
}

export type TraceBucket = TraceSummary & { bucket: string; partial: boolean }
export type LogBucket = { bucket: string; severity: Severity; records: number; partial: boolean }

export type SpanRecord = {
  timestamp: string
  traceId: string
  spanId: string
  parentSpanId?: string
  service: string
  name: string
  durationMs: number
  status: string
  attributes?: Record<string, string>
}

export type LogRecord = {
  timestamp: string
  traceId: string
  spanId: string
  service: string
  severity: Severity
  body: string
  attributes?: Record<string, string>
  resourceAttributes?: Record<string, string>
}

export type QueryResult<T> = {
  data: T[]
  from: string
  to: string
  truncated: boolean
  nextOffset?: number
}

export type TelemetryData = {
  traces: SpanRecord[]
  logs: LogRecord[]
  traceBuckets: TraceBucket[]
  logBuckets: LogBucket[]
  summary?: TraceSummary
  services: string[]
  page: { truncated: boolean; nextOffset?: number }
  from: string
  to: string
}

export type TraceLink = (traceId: string, target: TelemetryMode) => string

export const SEVERITIES: Severity[] = [
  'unspecified',
  'trace',
  'debug',
  'info',
  'warn',
  'error',
  'fatal',
]

export function formatNumber(value: number | null | undefined, digits = 1) {
  return value == null ? '—' : value.toLocaleString(undefined, { maximumFractionDigits: digits })
}

export function formatTime(value: string, seconds = false) {
  return new Date(value).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    ...(seconds ? { second: '2-digit' } : {}),
  })
}

export function windowMinutes(search: URLSearchParams) {
  const value = search.get('minutes') ?? '30'
  return ['5', '30', '60', '1440'].includes(value) ? Number(value) : 30
}
