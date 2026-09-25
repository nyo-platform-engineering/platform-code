import { filterValues } from './filters'
import type {
  LogBucket,
  LogRecord,
  QueryResult,
  SpanRecord,
  TelemetryData,
  TelemetryMode,
  TraceBucket,
  TraceSummary,
} from './types'
import { windowMinutes } from './types'

async function query<T>(
  path: string,
  params: URLSearchParams,
  signal: AbortSignal,
): Promise<QueryResult<T>> {
  const response = await fetch(`/api/v1/${path}?${params}`, {
    signal: AbortSignal.any([signal, AbortSignal.timeout(8_000)]),
  })
  if (!response.ok) {
    const error = (await response.json().catch(() => ({}))) as { message?: string; error?: string }
    throw new Error(error.message ?? error.error ?? `Query failed (${response.status})`)
  }
  return response.json() as Promise<QueryResult<T>>
}

function without(params: URLSearchParams, ...keys: string[]) {
  const copy = new URLSearchParams(params)
  keys.forEach((key) => copy.delete(key))
  return copy
}

function queryParameters(mode: TelemetryMode, search: URLSearchParams) {
  const fixedTo = search.get('to')
  const to = fixedTo && !Number.isNaN(Date.parse(fixedTo)) ? new Date(fixedTo) : new Date()
  const params = new URLSearchParams({
    environment: 'local',
    from: new Date(to.getTime() - windowMinutes(search) * 60_000).toISOString(),
    to: to.toISOString(),
    limit: '100',
  })
  for (const service of filterValues(search, 'service')) params.append('service', service)
  const filters =
    mode === 'logs'
      ? ['q', 'attr', 'severity', 'traceId']
      : ['q', 'attr', 'status', 'minDurationMs']
  for (const key of filters) {
    for (const value of search.getAll(key).filter(Boolean)) params.append(key, value)
  }
  return params
}

export async function fetchTelemetry(
  mode: TelemetryMode,
  search: URLSearchParams,
  signal: AbortSignal,
): Promise<TelemetryData> {
  const chartParams = queryParameters(mode, search)
  const listParams = new URLSearchParams(chartParams)
  listParams.set('offset', search.get('offset') ?? '0')
  const serviceParams = without(
    chartParams,
    'service',
    'traceId',
    'severity',
    'status',
    'minDurationMs',
    'q',
    'attr',
  )
  const serviceRequest = query<{ service: string }>('services', serviceParams, signal)

  if (mode === 'logs') {
    const [rows, chart, services] = await Promise.all([
      query<LogRecord>('logs', listParams, signal),
      query<LogBucket>('logs/volume', chartParams, signal),
      serviceRequest,
    ])
    return {
      traces: [],
      logs: rows.data,
      traceBuckets: [],
      logBuckets: chart.data,
      services: services.data.map((row) => row.service),
      page: { truncated: rows.truncated, nextOffset: rows.nextOffset },
      from: chart.from,
      to: chart.to,
    }
  }

  const [rows, chart, services] = await Promise.all([
    query<SpanRecord>('traces', listParams, signal),
    query<TraceBucket>('traces/red', chartParams, signal) as Promise<
      QueryResult<TraceBucket> & { summary: TraceSummary }
    >,
    serviceRequest,
  ])
  const data: TelemetryData = {
    traces: rows.data,
    logs: [],
    traceBuckets: chart.data,
    logBuckets: [],
    summary: chart.summary,
    services: services.data.map((row) => row.service),
    page: { truncated: rows.truncated, nextOffset: rows.nextOffset },
    from: chart.from,
    to: chart.to,
  }

  return data
}

export async function fetchTraceDetail(search: URLSearchParams, signal: AbortSignal) {
  const traceId = search.get('traceId') ?? ''
  const params = without(
    queryParameters('traces', search),
    'service',
    'status',
    'minDurationMs',
    'q',
    'attr',
  )
  params.set('limit', '500')
  // One detail request at a time leaves room for the three overview requests.
  const detail = await query<SpanRecord>(`traces/${encodeURIComponent(traceId)}`, params, signal)
  params.set('traceId', traceId)
  const correlatedLogs = await query<LogRecord>('logs', params, signal)
  return { detail, correlatedLogs }
}

export type SQLPreviewQuery = {
  name: string
  sql: string
  parameters: { position: number; type: string; value: string }[]
}

export async function fetchSQLPreview(
  mode: TelemetryMode,
  search: URLSearchParams,
  signal: AbortSignal,
) {
  const params = queryParameters(mode, search)
  params.set('preview', '1')
  const rows = new URLSearchParams(params)
  rows.set('offset', search.get('offset') ?? '0')
  const paths = mode === 'logs' ? ['logs', 'logs/volume'] : ['traces', 'traces/red']
  const results = await Promise.all(
    paths.map(async (path, index) => {
      const response = await fetch(`/api/v1/${path}?${index === 0 ? rows : params}`, {
        signal: AbortSignal.any([signal, AbortSignal.timeout(8_000)]),
        cache: 'no-store',
      })
      const body = await response.json()
      if (!response.ok) throw new Error(body.message ?? body.error ?? 'SQL preview unavailable')
      return body as { queries: SQLPreviewQuery[]; from: string; to: string }
    }),
  )
  return {
    queries: results.flatMap((result) => result.queries),
    from: results[0].from,
    to: results[0].to,
  }
}

export async function fetchAttributeKeys(
  mode: TelemetryMode,
  search: URLSearchParams,
  scope: string,
  term: string,
  signal: AbortSignal,
) {
  const params = without(queryParameters(mode, search), 'q', 'attr', 'traceId')
  params.set('scope', scope)
  params.set('keySearch', term)
  return query<{ key: string }>(`${mode}/attributes`, params, signal)
}
