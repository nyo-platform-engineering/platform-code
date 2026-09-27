import { useEffect, useId, useRef, useState } from 'react'
import { LuChevronDown, LuClock3, LuX } from 'react-icons/lu'
import { SearchableSelect } from '../../../components/SearchableSelect'
import { Button } from '../../../components/Button'
import { validateWindow, type TimeWindow } from './time-window'
import { inputInstant, offsetLabel, timeZones, zonedInput } from './time-zone'

type Props = {
  offset: number
  onOffsetChange: (offset: number) => void
  minutes: number
  paused: boolean
  from?: string
  to?: string
  onRangeChange: (minutes: string) => void
  onWindowChange: (window: TimeWindow) => void
}

const ranges = [
  { value: 5, label: 'Last 5 minutes' },
  { value: 15, label: 'Last 15 minutes' },
  { value: 30, label: 'Last 30 minutes' },
  { value: 60, label: 'Last hour' },
  { value: 180, label: 'Last 3 hours' },
  { value: 360, label: 'Last 6 hours' },
  { value: 720, label: 'Last 12 hours' },
  { value: 1440, label: 'Last 24 hours' },
]

export function TimeRangePicker({
  offset,
  onOffsetChange,
  minutes,
  paused,
  from,
  to,
  onRangeChange,
  onWindowChange,
}: Props) {
  const id = useId()
  const trigger = useRef<HTMLButtonElement>(null)
  const panel = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [start, setStart] = useState('')
  const [end, setEnd] = useState('')
  const [error, setError] = useState('')
  const range = ranges.find((item) => item.value === minutes)
  const label = paused
    ? `Custom · ${minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60}h` : `${Number(minutes.toFixed(2))}m`}`
    : (range?.label ?? 'Custom window')

  function close() {
    setOpen(false)
    trigger.current?.focus()
  }

  function toggle() {
    if (open) {
      close()
      return
    }
    const currentEnd = to ?? new Date().toISOString()
    const currentStart = from ?? new Date(Date.parse(currentEnd) - minutes * 60_000).toISOString()
    setStart(zonedInput(currentStart, offset))
    setEnd(zonedInput(currentEnd, offset))
    setError('')
    setOpen(true)
  }

  function changeZone(next: number) {
    try {
      setStart(zonedInput(inputInstant(start, offset), next))
      setEnd(zonedInput(inputInstant(end, offset), next))
      onOffsetChange(next)
      setError('')
      try {
        localStorage.setItem('signal-deck-time-offset', String(next))
      } catch {
        /* Optional preference. */
      }
    } catch {
      setError('Enter valid Start and End times before changing timezone.')
    }
  }

  useEffect(() => {
    if (!open) return
    const dismiss = (event: PointerEvent) => {
      if (
        !panel.current?.contains(event.target as Node) &&
        !trigger.current?.contains(event.target as Node)
      )
        setOpen(false)
    }
    document.addEventListener('pointerdown', dismiss)
    return () => document.removeEventListener('pointerdown', dismiss)
  }, [open])

  return (
    <>
      <Button
        ref={trigger}
        onClick={toggle}
        aria-label={`Time range: ${label}`}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-controls={open ? id : undefined}
        className="flex min-w-0 flex-1 items-center gap-2 border-transparent bg-transparent text-ink"
      >
        <LuClock3 aria-hidden className="size-3.5 shrink-0" />
        <span className="truncate">{label}</span>
        <LuChevronDown aria-hidden className="ml-auto size-3 shrink-0" />
      </Button>
      {open && (
        <div
          ref={panel}
          id={id}
          role="dialog"
          aria-label="Choose time range"
          className="absolute top-full right-0 z-40 mt-2 max-h-[min(36rem,80vh)] w-[min(38rem,calc(100vw-2rem))] overflow-auto rounded-lg border border-border-strong bg-surface text-xs shadow-xl"
          onKeyDown={(event) => {
            if (event.key === 'Escape') {
              event.stopPropagation()
              close()
            }
          }}
        >
          <div className="flex items-center justify-between border-b border-border px-4 py-3">
            <div>
              <h2 className="font-semibold text-ink">Time range</h2>
              <p className="mt-1 text-[11px] text-muted">
                Set exact bounds or jump to a recent window.
              </p>
            </div>
            <Button
              aria-label="Close time range"
              onClick={close}
              className="border-transparent bg-transparent px-2"
            >
              <LuX aria-hidden />
            </Button>
          </div>
          <div className="grid sm:grid-cols-[minmax(0,1fr)_12rem]">
            <form
              aria-label="Exact time window"
              className="grid gap-3 p-4"
              onSubmit={(event) => {
                event.preventDefault()
                try {
                  onWindowChange(
                    validateWindow(inputInstant(start, offset), inputInstant(end, offset)),
                  )
                  close()
                } catch (problem) {
                  setError((problem as Error).message)
                }
              }}
            >
              <h3 className="text-[10px] font-semibold tracking-wider text-muted uppercase">
                Absolute range
              </h3>
              <label className="grid gap-1.5 font-medium text-muted">
                Start
                <input
                  autoFocus
                  required
                  type="datetime-local"
                  step="1"
                  value={start}
                  onChange={(event) => setStart(event.target.value)}
                  className="min-w-0 rounded border border-border bg-canvas p-2 font-mono text-[11px] text-ink focus-visible:outline-2 focus-visible:outline-accent"
                />
              </label>
              <label className="grid gap-1.5 font-medium text-muted">
                End
                <input
                  required
                  type="datetime-local"
                  step="1"
                  value={end}
                  onChange={(event) => setEnd(event.target.value)}
                  className="min-w-0 rounded border border-border bg-canvas p-2 font-mono text-[11px] text-ink focus-visible:outline-2 focus-visible:outline-accent"
                />
              </label>
              <SearchableSelect
                label="Timezone"
                values={[String(offset)]}
                options={timeZones.map((zone) => ({
                  value: String(zone.offset),
                  label: zone.label,
                }))}
                onChange={([value]) => changeZone(Number(value))}
                portalContainer={() => panel.current}
              />
              <p className="text-[10px] leading-relaxed text-muted">
                Times shown in {offsetLabel(offset)}. Maximum 24 hours. Applying an exact range
                pauses live updates.
              </p>
              {error && (
                <p role="alert" className="text-danger">
                  {error}
                </p>
              )}
              <Button type="submit" className="border-accent/30 bg-accent-soft text-accent-strong">
                Apply range
              </Button>
            </form>
            <aside
              aria-label="Quick time ranges"
              className="border-t border-border bg-surface-raised p-3 sm:border-t-0 sm:border-l"
            >
              <h3 className="px-2 pb-2 text-[10px] font-semibold tracking-wider text-muted uppercase">
                Quick ranges
              </h3>
              <div className="grid grid-cols-2 gap-1 sm:grid-cols-1">
                {ranges.map((item) => (
                  <Button
                    key={item.value}
                    onClick={() => {
                      onRangeChange(String(item.value))
                      close()
                    }}
                    className={`border-transparent text-left ${!paused && item.value === minutes ? 'bg-accent-soft text-accent-strong' : 'bg-transparent'}`}
                  >
                    {item.label}
                  </Button>
                ))}
              </div>
              <p className="mt-3 px-2 text-[10px] leading-relaxed text-muted">
                Relative to now. Use « / » to move half a window.
              </p>
            </aside>
          </div>
        </div>
      )}
    </>
  )
}
