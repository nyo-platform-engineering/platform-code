import { Button } from '../../../components/Button'
import { CorrelatedLogs } from '../../logs/sections/CorrelatedLogs'
import { useEffect, useState } from 'react'
import { DetailDrawer } from '../../../components/DetailDrawer'
import { fetchTraceDetail } from '../../_shared/telemetry/api'
import { TraceFlamegraph } from './TraceFlamegraph'
import { SpanTable } from './SpanTable'
import type { TraceLink } from '../../_shared/telemetry/types'

type Detail = Awaited<ReturnType<typeof fetchTraceDetail>>
type Tab = 'flamegraph' | 'spans' | 'logs'

export function TraceDrawer({
  traceId,
  search,
  link,
  onClose,
}: {
  traceId: string
  search: URLSearchParams
  link: TraceLink
  onClose: () => void
}) {
  const [tab, setTab] = useState<Tab>('flamegraph')
  const [result, setResult] = useState<{ key: string; data: Detail }>()
  const [problem, setProblem] = useState<string>()
  const [loading, setLoading] = useState(true)
  const [revision, setRevision] = useState(0)
  const queryKey = search.toString()
  const data = result?.key === queryKey ? result.data : undefined

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setProblem(undefined)
    fetchTraceDetail(new URLSearchParams(queryKey), controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) setResult({ key: queryKey, data })
      })
      .catch((error) => {
        if (!controller.signal.aborted) setProblem((error as Error).message)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [queryKey, revision])

  return (
    <DetailDrawer title="Inspect trace" eyebrow="Trace details" onClose={onClose}>
      <div className="bg-surface px-6 pb-5 max-sm:px-4 max-sm:pb-4 [&_a]:text-accent-strong [&_a]:hover:underline [&_code]:text-xs [&_code]:wrap-anywhere [&>div]:mt-3 [&>div]:flex [&>div]:flex-wrap [&>div]:items-center [&>div]:gap-3 [&>div]:text-xs [&>div]:text-muted">
        <code>{traceId}</code>
        <div>
          <span>
            {data
              ? `${data.detail.data.length} spans · ${data.correlatedLogs.data.length} logs`
              : 'Selected trace'}
          </span>
          <Button disabled={loading} onClick={() => setRevision((value) => value + 1)}>
            {loading ? 'Loading…' : 'Refresh trace'}
          </Button>
          <a href={link(traceId, 'logs')}>Open logs ↗</a>
        </div>
      </div>
      <div
        className="flex gap-2 border-y border-border bg-surface px-6 py-3 max-sm:px-4 [&_button[aria-pressed=true]]:border-accent [&_button[aria-pressed=true]]:bg-accent-soft [&_button[aria-pressed=true]]:text-ink"
        role="group"
        aria-label="Trace detail view"
      >
        {(['flamegraph', 'spans', 'logs'] as const).map((value) => (
          <Button key={value} aria-pressed={tab === value} onClick={() => setTab(value)}>
            {value === 'flamegraph'
              ? 'Flamegraph'
              : value === 'spans'
                ? 'Span table'
                : 'Correlated logs'}
          </Button>
        ))}
      </div>
      <div className="min-h-0 flex-1 overflow-auto overscroll-contain" aria-busy={loading}>
        {problem && (
          <div
            className="rounded-ui border border-danger px-4 py-3 text-xs text-danger"
            role="alert"
          >
            {problem} Use Refresh trace to retry.
          </div>
        )}
        {!data && !problem && (
          <p className="px-5 py-6 text-xs text-muted" role="status">
            Loading trace details…
          </p>
        )}
        {data && (
          <>
            {data.detail.truncated && (
              <p className="px-5 py-6 text-xs text-muted">Trace truncated at 500 spans.</p>
            )}
            {!data.detail.data.length && tab !== 'logs' && (
              <p className="px-5 py-6 text-xs text-muted">
                No spans in this window yet. Allow a few seconds for ingestion, then refresh.
              </p>
            )}
            {tab === 'flamegraph' && data.detail.data.length > 0 && (
              <TraceFlamegraph spans={data.detail.data} />
            )}
            {tab === 'spans' && data.detail.data.length > 0 && (
              <SpanTable rows={data.detail.data} />
            )}
            {tab === 'logs' && (
              <>
                <CorrelatedLogs rows={data.correlatedLogs.data} />
                {data.correlatedLogs.truncated && (
                  <p className="px-5 py-6 text-xs text-muted">
                    Showing the first 500 logs. Open logs to browse more.
                  </p>
                )}
              </>
            )}
          </>
        )}
      </div>
    </DetailDrawer>
  )
}
