import { useState } from 'react'
import { TraceDrawer } from '../../traces/sections/TraceDrawer'
import { DetailDrawer } from '../../../components/DetailDrawer'
import type { LogRecord, TraceLink } from '../../_shared/telemetry/types'
import { SeverityBadge } from './SeverityBadge'

function RecordFields({ title, values }: { title: string; values: Record<string, string> }) {
  if (!Object.keys(values).length) return null
  return (
    <section aria-label={title}>
      <h3 className="mb-2 text-xs font-semibold">{title}</h3>
      <dl className="divide-y divide-border rounded border border-border bg-surface text-xs">
        {Object.entries(values).map(([key, value]) => (
          <div
            key={key}
            className="grid gap-1 px-3 py-2 sm:grid-cols-[minmax(120px,1fr)_minmax(0,3fr)] sm:gap-4"
          >
            <dt className="wrap-anywhere text-muted">{key}</dt>
            <dd className="wrap-anywhere whitespace-pre-wrap">{value || '—'}</dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

export function LogDrawer({
  record,
  to,
  link,
  onClose,
}: {
  record: LogRecord
  to: string
  link: TraceLink
  onClose: () => void
}) {
  const [traceSearch, setTraceSearch] = useState<URLSearchParams>()
  function openTrace() {
    const search = new URL(link(record.traceId, 'traces'), window.location.origin).searchParams
    search.set('to', to)
    setTraceSearch(search)
  }
  return (
    <>
      <DetailDrawer title="Inspect log" eyebrow="Log details" onClose={onClose}>
        <div className="min-h-0 flex-1 space-y-5 overflow-auto overscroll-contain p-4 sm:p-5">
          <div className="flex flex-wrap items-center gap-3 text-xs">
            <SeverityBadge severity={record.severity} />
            <span className="text-muted">{record.service}</span>
            {record.traceId && (
              <button
                type="button"
                aria-haspopup="dialog"
                onClick={openTrace}
                className="cursor-pointer text-accent-strong hover:underline focus-visible:outline-2 focus-visible:outline-accent"
              >
                Open related trace →
              </button>
            )}
          </div>
          <section aria-label="Log content">
            <h3 className="mb-2 text-xs font-semibold">Log content</h3>
            <pre className="rounded border border-border bg-surface px-2 py-1.5 font-mono text-xs leading-4 wrap-anywhere whitespace-pre-wrap">
              {record.body || '(empty message)'}
            </pre>
          </section>
          <RecordFields
            title="Record fields"
            values={{
              'Timestamp (UTC)': record.timestamp,
              Service: record.service,
              'Trace ID': record.traceId,
              'Span ID': record.spanId,
            }}
          />
          <RecordFields title="Log attributes" values={record.attributes ?? {}} />
          <RecordFields title="Resource attributes" values={record.resourceAttributes ?? {}} />
        </div>
      </DetailDrawer>
      {traceSearch && (
        <TraceDrawer
          traceId={record.traceId}
          search={traceSearch}
          link={link}
          onClose={() => setTraceSearch(undefined)}
        />
      )}
    </>
  )
}
