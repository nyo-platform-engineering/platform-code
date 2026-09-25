import { Button } from '../../../components/Button'
import { useMemo, useState } from 'react'
import { layoutTrace } from '../lib/trace-layout'
import { formatNumber, type SpanRecord } from '../../_shared/telemetry/types'

export function TraceFlamegraph({ spans }: { spans: SpanRecord[] }) {
  const [zoomId, setZoomId] = useState<string>()
  const [zoomHistory, setZoomHistory] = useState<(string | undefined)[]>([])
  const [selectedId, setSelectedId] = useState<string>()
  const [hoverId, setHoverId] = useState<string>()
  const layout = useMemo(() => layoutTrace(spans, zoomId), [spans, zoomId])
  const active = layout.items.find(({ node }) => node.span.spanId === (selectedId ?? hoverId))?.node
  function clearSelection() {
    setSelectedId(undefined)
    setHoverId(undefined)
  }
  function back() {
    if (!zoomHistory.length) return
    setZoomId(zoomHistory[zoomHistory.length - 1])
    setZoomHistory((history) => history.slice(0, -1))
    clearSelection()
  }
  return (
    <div className="p-4 max-sm:p-2.5">
      <div className="flex flex-wrap items-center justify-between gap-2.5 [&_h3]:text-[13px] [&_h3]:font-semibold [&_p]:mt-1 [&_p]:text-xs [&_p]:text-muted">
        <div>
          <h3>Trace flamegraph</h3>
          <p>Time runs left to right · child spans sit below their parent</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={!zoomHistory.length} onClick={back}>
            ← Back
          </Button>
          <Button
            disabled={!zoomId}
            onClick={() => {
              setZoomId(undefined)
              setZoomHistory([])
              clearSelection()
            }}
          >
            Reset zoom
          </Button>
          <Button disabled={!selectedId} onClick={clearSelection}>
            Clear selection
          </Button>
        </div>
      </div>
      <div
        className="mt-5 flex justify-between pb-2 text-[10px] text-muted tabular-nums"
        aria-hidden="true"
      >
        {[0, 0.25, 0.5, 0.75, 1].map((fraction) => (
          <span key={fraction}>
            {formatNumber(layout.start + layout.duration * fraction, 3)} ms
          </span>
        ))}
      </div>
      <div
        className="max-h-[420px] overflow-auto rounded border border-border"
        role="group"
        aria-label="Trace span timeline"
        onClick={(event) => {
          if (!(event.target as HTMLElement).closest('button')) clearSelection()
        }}
        onKeyDown={(event) => {
          if (event.key === 'Escape' && (selectedId || zoomId)) {
            event.preventDefault()
            event.stopPropagation()
            if (selectedId) clearSelection()
            else back()
          }
        }}
      >
        <div
          className="relative min-h-11 bg-[repeating-linear-gradient(to_right,transparent_0,transparent_calc(25%-1px),var(--ui-border)_calc(25%-1px),var(--ui-border)_25%)]"
          style={{ height: Math.max(1, layout.rows) * 36 + 8 }}
        >
          {layout.items.map(({ node, depth, row }) => {
            const span = node.span
            return (
              <Button
                key={span.spanId}
                className="absolute flex h-[30px] max-w-full min-w-0 items-center justify-between gap-2 overflow-hidden rounded-xs border-accent bg-accent-soft px-1.5 text-left text-ink data-[error=true]:border-l-[3px] data-[error=true]:border-danger data-[selected=true]:outline-2 data-[selected=true]:-outline-offset-2 data-[selected=true]:outline-ink [&_small]:whitespace-nowrap [&_small]:tabular-nums max-sm:[&_small]:hidden [&>span]:truncate"
                data-error={span.status === 'Error'}
                data-selected={selectedId === span.spanId}
                style={{
                  left: `${(100 * (node.start - layout.start)) / layout.duration}%`,
                  width: `max(3px, ${(100 * span.durationMs) / layout.duration}%)`,
                  top: row * 36 + 4,
                }}
                aria-label={`${span.name}, ${span.service}, ${formatNumber(span.durationMs, 3)} milliseconds, depth ${depth}, ${span.status || 'Unset'}`}
                aria-pressed={selectedId === span.spanId}
                onMouseEnter={() => setHoverId(span.spanId)}
                onMouseLeave={() => setHoverId(undefined)}
                onFocus={() => setHoverId(span.spanId)}
                onBlur={() => setHoverId(undefined)}
                onClick={() => {
                  setSelectedId((current) => (current === span.spanId ? undefined : span.spanId))
                  setHoverId(undefined)
                }}
              >
                <span>
                  {span.status === 'Error' ? '⚠ ' : ''}
                  {span.name}
                </span>
                <small>{formatNumber(span.durationMs, 3)} ms</small>
              </Button>
            )
          })}
        </div>
      </div>
      <div className="mt-3 min-h-[70px] rounded border border-border bg-surface p-3 wrap-anywhere [&_dl]:my-3 [&_dl]:flex [&_dl]:flex-wrap [&_dl]:gap-x-7 [&_dl]:gap-y-3 [&_dl]:text-xs [&_dt]:mb-1 [&_dt]:text-[10px] [&_dt]:text-muted [&_p]:text-xs [&_p]:text-muted [&_pre]:max-h-[200px] [&_pre]:overflow-auto [&_pre]:text-[11px] [&_summary]:text-[11px]">
        {active ? (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2.5 [&_h3]:text-[13px] [&_h3]:font-semibold [&_p]:mt-1 [&_p]:text-xs [&_p]:text-muted">
              <strong>{active.span.name}</strong>
              <Button
                disabled={zoomId === active.span.spanId}
                onClick={() => {
                  setZoomHistory((history) => [...history, zoomId])
                  setZoomId(active.span.spanId)
                  setSelectedId(active.span.spanId)
                  setHoverId(undefined)
                }}
              >
                Zoom to span
              </Button>
            </div>
            <dl>
              <div>
                <dt>Service</dt>
                <dd>{active.span.service}</dd>
              </div>
              <div>
                <dt>Duration</dt>
                <dd>{formatNumber(active.span.durationMs, 3)} ms</dd>
              </div>
              <div>
                <dt>Start offset</dt>
                <dd>+{formatNumber(active.start, 3)} ms</dd>
              </div>
              <div>
                <dt>Status</dt>
                <dd>{active.span.status || 'Unset'}</dd>
              </div>
              <div>
                <dt>Span ID</dt>
                <dd>{active.span.spanId}</dd>
              </div>
            </dl>
            {active.incomplete && (
              <p>Parent unavailable or invalid; this span is shown as a root.</p>
            )}
            <details>
              <summary>Span attributes</summary>
              <pre>{JSON.stringify(active.span.attributes ?? {}, null, 2)}</pre>
            </details>
          </>
        ) : (
          <p>
            Hover or focus to inspect. Click to select; click again or blank space to deselect. Zoom
            to explore a subtree, then use Back or Reset zoom.
          </p>
        )}
      </div>
      {(layout.incomplete > 0 || layout.omitted > 0) && (
        <p className="mt-3 text-xs text-muted">
          {layout.incomplete > 0 && `${layout.incomplete} spans have missing or invalid parents. `}
          {layout.omitted > 0 && `${layout.omitted} duplicate or invalid spans omitted. `}
          This view represents the spans available in the selected time window.
        </p>
      )}
    </div>
  )
}
