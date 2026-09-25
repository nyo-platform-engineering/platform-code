import { SeverityBadge } from './SeverityBadge'
import { LuChevronRight } from 'react-icons/lu'
import type { LogRecord } from '../../_shared/telemetry/types'

function logTime(timestamp: string) {
  const date = new Date(timestamp)
  if (!Number.isFinite(date.getTime())) return timestamp
  return date.toLocaleTimeString([], {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    fractionalSecondDigits: 3,
  })
}

export function CorrelatedLogs({ rows }: { rows: LogRecord[] }) {
  if (!rows.length)
    return <p className="px-5 py-6 text-xs text-muted">No correlated logs in this time window.</p>

  return (
    <section className="min-w-0 px-4 pt-3 pb-6 max-sm:p-2.5" aria-label="Correlated log records">
      <p className="mb-3 text-[11px] text-muted">
        Newest first · timestamps in local time · click a row to expand
      </p>
      {rows.map((row, index) => (
        <details
          className="group/log border border-border bg-surface not-first:border-t-0"
          key={`${row.timestamp}-${row.spanId}-${index}`}
        >
          <summary className="grid cursor-pointer list-none grid-cols-[14px_100px_72px_minmax(0,1fr)] items-center gap-2.5 p-3 font-mono text-[11px] leading-relaxed group-open/log:bg-accent-soft hover:bg-surface-hover max-sm:grid-cols-[14px_100px_minmax(0,1fr)] max-sm:gap-1.5 max-sm:p-2.5 [&::-webkit-details-marker]:hidden">
            <LuChevronRight
              className="text-muted group-open/log:rotate-90"
              aria-hidden="true"
              size={14}
            />
            <time
              className="whitespace-nowrap text-muted tabular-nums"
              dateTime={row.timestamp}
              title={row.timestamp}
            >
              {logTime(row.timestamp)}
            </time>
            <SeverityBadge severity={row.severity} />
            <span className="truncate max-sm:col-[2/-1]">{row.body || '(empty message)'}</span>
          </summary>
          <div className="border-t border-border p-4 max-sm:p-3">
            <div className="mb-1.5 text-[10px] text-muted">Message</div>
            <pre className="max-h-80 overflow-auto rounded border border-border bg-canvas p-3 font-mono text-[11px] leading-relaxed wrap-anywhere whitespace-pre-wrap">
              {row.body || '(empty message)'}
            </pre>
            <dl className="my-4 grid grid-cols-2 gap-4 max-sm:grid-cols-1 [&_dd]:font-mono [&_dd]:text-[11px] [&_dd]:leading-relaxed [&_dd]:wrap-anywhere [&_dt]:mb-1.5 [&_dt]:text-[10px] [&_dt]:text-muted">
              <div>
                <dt>Timestamp</dt>
                <dd>{row.timestamp}</dd>
              </div>
              <div>
                <dt>Service</dt>
                <dd>{row.service || '—'}</dd>
              </div>
              <div>
                <dt>Severity</dt>
                <dd>{row.severity}</dd>
              </div>
              <div>
                <dt>Trace ID</dt>
                <dd>{row.traceId || '—'}</dd>
              </div>
              <div>
                <dt>Span ID</dt>
                <dd>{row.spanId || '—'}</dd>
              </div>
            </dl>
            <details className="[&_pre]:mt-3 [&_pre]:max-h-80 [&_pre]:overflow-auto [&_pre]:font-mono [&_pre]:text-[11px] [&_pre]:leading-relaxed [&_pre]:wrap-anywhere [&_pre]:whitespace-pre-wrap [&>summary]:text-[11px] [&>summary]:text-muted">
              <summary>Raw record</summary>
              <pre>{JSON.stringify(row, null, 2)}</pre>
            </details>
          </div>
        </details>
      ))}
    </section>
  )
}
