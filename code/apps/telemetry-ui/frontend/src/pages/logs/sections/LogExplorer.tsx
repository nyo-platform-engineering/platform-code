import { useEffect, useMemo, useState } from 'react'
import { LuArrowRight } from 'react-icons/lu'
import { LogDrawer } from './LogDrawer'
import type { LogRecord, TraceLink } from '../../_shared/telemetry/types'
import { LogFields, type LogField, DEFAULT_FIELDS } from './LogFields'
import { SeverityBadge } from './SeverityBadge'

const STORAGE_KEY = 'telemetry.logs.visible-fields.v2'
function initialFields(): string[] {
  try {
    const current = localStorage.getItem(STORAGE_KEY)
    const stored: unknown = JSON.parse(
      current ?? localStorage.getItem('telemetry.logs.visible-fields') ?? 'null',
    )
    if (Array.isArray(stored) && stored.every((field) => typeof field === 'string')) {
      return current === null ? [...new Set([...stored, 'body'])] : stored
    }
  } catch {
    /* Storage may be unavailable. */
  }
  return DEFAULT_FIELDS
}

function fieldValue(record: LogRecord, field: LogField): string {
  if (field.scope === 'log') return record.attributes?.[field.key] ?? '—'
  if (field.scope === 'resource') return record.resourceAttributes?.[field.key] ?? '—'
  return String(
    record[field.key as 'timestamp' | 'service' | 'traceId' | 'spanId' | 'severity'] || '—',
  )
}

export function LogExplorer({
  rows,
  link,
  to,
}: {
  rows: LogRecord[]
  link: TraceLink
  to: string
}) {
  const [selected, setSelected] = useState<{ record: LogRecord; to: string }>()
  const [visible, setVisible] = useState(initialFields)
  const fields = useMemo(() => {
    const result: LogField[] = [
      { id: 'body', key: 'body', label: 'Log content', scope: 'record' },
      { id: 'timestamp', key: 'timestamp', label: 'Timestamp', scope: 'record' },
      { id: 'severity', key: 'severity', label: 'Severity', scope: 'record' },
      { id: 'service', key: 'service', label: 'Service', scope: 'record' },
      { id: 'traceId', key: 'traceId', label: 'Trace ID', scope: 'record' },
      { id: 'spanId', key: 'spanId', label: 'Span ID', scope: 'record' },
    ]
    for (const scope of ['log', 'resource'] as const) {
      const keys = new Set(
        rows.flatMap((row) =>
          Object.keys((scope === 'log' ? row.attributes : row.resourceAttributes) ?? {}),
        ),
      )
      // Keep selected fields available when a new page has no values for them.
      for (const id of visible) if (id.startsWith(`${scope}:`)) keys.add(id.slice(scope.length + 1))
      for (const key of [...keys].sort())
        result.push({ id: `${scope}:${key}`, key, label: key, scope })
    }
    return result
  }, [rows, visible])
  const selectedFields = fields.filter((field) => field.id !== 'body' && visible.includes(field.id))
  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(visible))
    } catch {
      /* Keep in-memory preferences. */
    }
  }, [visible])

  return (
    <div className="grid min-w-0 items-start md:grid-cols-[200px_minmax(0,1fr)]">
      <LogFields fields={fields} visible={visible} onChange={setVisible} />
      <div
        role="region"
        aria-label="Log messages"
        className="min-w-0 border-t border-border md:border-t-0 md:border-l"
      >
        {!rows.length && (
          <p className="p-6 text-center text-xs text-muted">No logs match these filters.</p>
        )}
        {!!rows.length && !selectedFields.length && !visible.includes('body') && (
          <p className="p-6 text-center text-xs text-muted">
            Select a display field to show log records.
          </p>
        )}
        {(selectedFields.length || visible.includes('body') ? rows : []).map((record, index) => (
          <article
            key={`${record.timestamp}-${record.traceId}-${record.spanId}-${index}`}
            aria-label={`Log record ${index + 1}`}
            onClick={(event) => {
              if (
                (event.target as HTMLElement).closest('a, button') ||
                window.getSelection()?.toString()
              )
                return
              event.currentTarget
                .querySelector<HTMLButtonElement>('button')
                ?.focus({ preventScroll: true })
              setSelected({ record, to })
            }}
            className="group relative min-w-0 cursor-pointer space-y-1 border-b border-l-[3px] border-border border-l-transparent px-3 py-2 pr-11 transition-colors duration-150 last:border-b-0 focus-within:border-l-accent focus-within:bg-accent-soft hover:border-l-accent hover:bg-accent-soft motion-reduce:transition-none"
          >
            <button
              type="button"
              aria-label={`Open log record ${index + 1}`}
              aria-haspopup="dialog"
              onClick={() => setSelected({ record, to })}
              className="absolute top-2 right-2 rounded border border-transparent p-2 text-muted transition-colors group-focus-within:border-accent/40 group-focus-within:bg-accent group-focus-within:text-accent-text group-hover:border-accent/40 group-hover:bg-accent group-hover:text-accent-text focus-visible:outline-2 focus-visible:outline-accent motion-reduce:transition-none"
            >
              <LuArrowRight aria-hidden />
            </button>
            {!!selectedFields.length && (
              <dl className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px]">
                {selectedFields.map((field) => (
                  <div key={field.id} className="flex max-w-full min-w-0 items-baseline gap-1.5">
                    <dt className="shrink-0 text-muted" title={field.scope}>
                      {field.label}
                    </dt>
                    <dd className="min-w-0 wrap-anywhere">
                      {field.id === 'severity' ? (
                        <SeverityBadge severity={record.severity} />
                      ) : field.id === 'traceId' && record.traceId ? (
                        <a
                          className="text-accent-strong hover:underline"
                          href={link(record.traceId, 'traces')}
                        >
                          {record.traceId}
                        </a>
                      ) : (
                        fieldValue(record, field)
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            )}
            {visible.includes('body') && (
              <pre
                aria-label="Log content"
                className="rounded border border-border bg-surface px-2 py-1.5 font-mono text-[11px] leading-4 wrap-anywhere whitespace-pre-wrap group-focus-within:border-accent/50 group-hover:border-accent/50"
              >
                {record.body || '(empty message)'}
              </pre>
            )}
          </article>
        ))}
      </div>
      {selected && (
        <LogDrawer
          record={selected.record}
          to={selected.to}
          link={link}
          onClose={() => setSelected(undefined)}
        />
      )}
    </div>
  )
}
