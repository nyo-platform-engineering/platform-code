import { useState } from 'react'
import { offsetLabel, preferredOffset, zonedInput } from './time-zone'
import { shiftWindow, type TimeWindow } from './time-window'
import { refreshIntervals, refreshLabel } from './refresh'
import { TimeRangePicker } from './TimeRangePicker'
import { LuPause, LuPlay, LuRefreshCw } from 'react-icons/lu'
import { SearchableSelect } from '../../../components/SearchableSelect'
import { Button } from '../../../components/Button'

type Props = {
  minutes: number
  paused: boolean
  refreshSeconds: number
  onIntervalChange: (seconds: number) => void
  loading: boolean
  problem?: string
  updated?: string
  from?: string
  to?: string
  onRangeChange: (minutes: string) => void
  onWindowChange: (window: TimeWindow) => void
  onToggleLive: () => void
  onRefresh: () => void
}

export function TimeControls({
  minutes,
  paused,
  refreshSeconds,
  onIntervalChange,
  loading,
  problem,
  updated,
  from,
  to,
  onRangeChange,
  onWindowChange,
  onToggleLive,
  onRefresh,
}: Props) {
  const [offset, setOffset] = useState(preferredOffset)
  const zone = offsetLabel(offset)

  function dateTime(value: string) {
    return `${zonedInput(value, offset).replace('T', ' ')} ${zone}`
  }

  function shift(direction: -1 | 1) {
    if (from && to) onWindowChange(shiftWindow({ from, to }, direction))
  }

  return (
    <section
      aria-label="Time range and refresh"
      className="relative grid w-full min-w-0 items-center gap-x-4 gap-y-1.5 sm:w-auto sm:grid-cols-[auto_minmax(0,1fr)]"
    >
      <div className="flex flex-col gap-1.5 px-1 text-[11px] text-muted">
        {from && to ? (
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
            <dt>Start</dt>
            <dd className="font-mono tabular-nums">
              <time dateTime={from}>{dateTime(from)}</time>
            </dd>
            <dt>End</dt>
            <dd className="font-mono tabular-nums">
              <time dateTime={to}>{dateTime(to)}</time>
            </dd>
          </dl>
        ) : (
          <span>Loading time window…</span>
        )}
      </div>
      <div
        role="group"
        aria-label="Query time controls"
        className="flex flex-wrap items-center gap-1 rounded-lg border border-border bg-surface-raised p-1"
      >
        <Button
          aria-label="Shift window backward by half"
          title={`Back ${minutes / 2} minutes`}
          disabled={!from || !to || loading}
          onClick={() => shift(-1)}
          className="border-transparent bg-transparent px-2 text-base"
        >
          «
        </Button>
        <TimeRangePicker
          offset={offset}
          onOffsetChange={setOffset}
          minutes={minutes}
          paused={paused}
          from={from}
          to={to}
          onRangeChange={onRangeChange}
          onWindowChange={onWindowChange}
        />
        <Button
          aria-label="Shift window forward by half"
          title={`Forward ${minutes / 2} minutes`}
          disabled={!from || !to || loading}
          onClick={() => shift(1)}
          className="border-transparent bg-transparent px-2 text-base"
        >
          »
        </Button>
        <span aria-hidden className="mx-1 h-4 w-px bg-border" />
        <Button
          onClick={onRefresh}
          disabled={loading}
          aria-label="Refresh current time range"
          title="Refresh the selected time window"
          className="flex items-center gap-1.5 border-transparent bg-transparent"
        >
          <LuRefreshCw
            aria-hidden
            className={`size-3.5 ${loading ? 'motion-safe:animate-spin' : ''}`}
          />
          Refresh
        </Button>
        <Button
          onClick={onToggleLive}
          aria-pressed={!paused}
          aria-label={paused ? 'Resume live updates' : 'Pause live updates'}
          title={
            paused
              ? `Return to the current time and refresh every ${refreshSeconds} seconds`
              : 'Freeze this time window for investigation'
          }
          className={`flex items-center gap-1.5 ${paused ? 'bg-surface text-muted' : 'border-accent/30 bg-accent-soft text-accent-strong'}`}
        >
          {paused ? (
            <LuPlay aria-hidden className="size-3.5" />
          ) : (
            <LuPause aria-hidden className="size-3.5" />
          )}
          {paused ? 'Paused' : 'Live'}
        </Button>
        <SearchableSelect
          compact
          label="Live refresh interval"
          values={[String(refreshSeconds)]}
          options={refreshIntervals.map((seconds) => ({
            value: String(seconds),
            label: refreshLabel(seconds),
          }))}
          onChange={([value]) => onIntervalChange(Number(value))}
        />
      </div>
      <span
        className={`px-1 text-[11px] sm:col-start-2 sm:text-right ${problem ? 'text-danger' : 'text-muted'}`}
      >
        {problem
          ? 'Query failed'
          : updated
            ? `Updated ${zonedInput(updated, offset).slice(11)} ${zone}`
            : 'Connecting…'}
      </span>
    </section>
  )
}
