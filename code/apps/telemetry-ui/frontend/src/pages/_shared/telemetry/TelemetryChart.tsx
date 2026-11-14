import { Button } from '../../../components/Button'
import { useMemo, useState } from 'react'
import {
  logPoints,
  logVolumePoints,
  LOG_VOLUME_SERIES,
  SEVERITY_SERIES,
  TRACE_METRICS,
  tracePoints,
  type TraceMetric,
} from './chart-model'
import { formatNumber, type TelemetryData, type TelemetryMode } from './types'
import { TimeSeriesPlot } from './TimeSeriesPlot'

type Props = {
  mode: TelemetryMode
  data: TelemetryData
  logView?: 'volume' | 'severity'
  metric?: TraceMetric
}

export function TelemetryChart({ mode, data, logView = 'severity', metric = 'requests' }: Props) {
  const [hidden, setHidden] = useState<Set<string>>(() => new Set())
  const points = useMemo(
    () =>
      mode === 'traces'
        ? tracePoints(data.traceBuckets)
        : logView === 'volume'
          ? logVolumePoints(data.logBuckets)
          : logPoints(data.logBuckets),
    [mode, logView, data.traceBuckets, data.logBuckets],
  )
  const series =
    mode === 'traces'
      ? TRACE_METRICS[metric].series
      : logView === 'volume'
        ? LOG_VOLUME_SERIES
        : SEVERITY_SERIES
  const visible = series.filter((item) => !hidden.has(item.key))
  const unit = mode === 'traces' ? TRACE_METRICS[metric].unit : 'records / min'
  const integer = mode === 'logs' || metric === 'requests'

  function toggleSeries(key: string) {
    setHidden((previous) => {
      const next = new Set(previous)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <section
      className="min-w-0 rounded-ui border border-border bg-surface-raised"
      aria-label={
        mode === 'traces'
          ? `${TRACE_METRICS[metric].label} chart`
          : logView === 'volume'
            ? 'Log volume chart'
            : 'Log severity chart'
      }
    >
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3.5 py-2.5 [&_h2]:text-xs [&_h2]:font-semibold [&>span]:font-mono [&>span]:text-[10px] [&>span]:text-muted">
        <div>
          <h2>
            {mode === 'traces'
              ? TRACE_METRICS[metric].label
              : logView === 'volume'
                ? 'Log volume'
                : 'Severity breakdown'}
          </h2>
        </div>
        <span className="font-mono text-[10px] text-muted">1 minute buckets</span>
      </div>

      <div
        className="flex flex-wrap items-center gap-0.5 px-2.5 pt-1 [&_button]:flex [&_button]:h-6 [&_button]:items-center [&_button]:gap-1 [&_button]:border-transparent [&_button]:bg-transparent [&_button]:px-1 [&_button]:text-[10px] [&_button[aria-pressed=false]]:line-through [&_button[aria-pressed=false]]:opacity-45"
        aria-label="Visible chart series"
      >
        {series.map((item) => (
          <Button
            key={item.key}
            aria-pressed={!hidden.has(item.key)}
            onClick={() => toggleSeries(item.key)}
          >
            <span
              className="inline-block size-[7px] shrink-0 rounded-xs"
              style={{ background: item.color }}
            />
            {item.label}
            {mode === 'logs' && (
              <span className="font-mono text-dim">
                {formatNumber(
                  data.logBuckets
                    .filter((bucket) => logView === 'volume' || bucket.severity === item.key)
                    .reduce((sum, bucket) => sum + bucket.records, 0),
                  0,
                )}
              </span>
            )}
          </Button>
        ))}
      </div>

      <TimeSeriesPlot
        points={points}
        series={visible}
        unit={unit}
        stacked={mode === 'logs'}
        integer={integer}
        emptyLabel={
          mode === 'logs'
            ? 'No log records in this window'
            : metric === 'errors'
              ? 'No errors in this window'
              : metric === 'duration'
                ? 'No duration samples in this window'
                : 'No requests in this window'
        }
      />

      <div className="flex flex-wrap justify-between gap-1 px-3.5 pt-1 pb-2 text-[9px] text-muted">
        <span>Hover to inspect · ← → to navigate</span>
        <span>Local time · shaded = partial minute</span>
      </div>
    </section>
  )
}
