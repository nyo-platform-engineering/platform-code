import type { ChartPoint, ChartSeries } from './chart-model'
import { formatNumber, formatTime } from './types'

export function ChartTooltip({
  id,
  point,
  series,
  stacked,
  unit,
  left,
}: {
  id: string
  point: ChartPoint
  series: ChartSeries[]
  stacked: boolean
  unit: string
  left: number
}) {
  const total = series.reduce((sum, item) => sum + (point.values[item.key] ?? 0), 0)
  return (
    <div
      id={id}
      className="pointer-events-none absolute top-1 z-20 w-56 max-w-[calc(100%-16px)] rounded-ui border border-border-strong bg-surface p-3 text-[11px] shadow-lg"
      role="tooltip"
      style={{ left }}
    >
      <div className="flex items-center justify-between gap-2 [&_strong]:font-semibold [&>span]:text-[10px] [&>span]:text-muted">
        <strong>
          {formatTime(point.timestamp)} –{' '}
          {formatTime(new Date(Date.parse(point.timestamp) + 60_000).toISOString())}
        </strong>
        <span>
          {new Date(point.timestamp).toLocaleDateString([], { month: 'short', day: 'numeric' })}
        </span>
      </div>
      {point.partial && (
        <div className="mt-1.5 text-[10px] text-accent-strong">
          Partial minute · observed values
        </div>
      )}
      <dl className="mt-3 grid gap-1.5 [&>div]:flex [&>div]:items-center [&>div]:justify-between [&>div]:gap-3">
        {series.map((item) => (
          <div key={item.key}>
            <dt className="flex items-center gap-2 text-muted">
              <span
                className="inline-block size-[7px] shrink-0 rounded-xs"
                style={{ background: item.color }}
              />
              {item.label}
            </dt>
            <dd className="font-mono text-[11px]">
              {formatNumber(point.values[item.key], 2)}
              {unit === 'percent' ? '%' : unit === 'milliseconds' ? ' ms' : ''}
            </dd>
          </div>
        ))}
        {stacked && (
          <div className="border-t border-border pt-2">
            <dt className="flex items-center gap-2 text-muted">Visible total</dt>
            <dd className="font-mono text-[11px]">{formatNumber(total, 0)}</dd>
          </div>
        )}
      </dl>
      {point.trace && (
        <p className="mt-2.5 border-t border-border pt-2 text-[10px] text-muted">
          {formatNumber(point.trace.requests, 0)} requests · {formatNumber(point.trace.errors, 0)}{' '}
          errors
        </p>
      )}
      {!stacked && !series.some((item) => point.values[item.key] != null) && (
        <p className="mt-2.5 border-t border-border pt-2 text-[10px] text-muted">
          No samples in this minute.
        </p>
      )}
    </div>
  )
}
