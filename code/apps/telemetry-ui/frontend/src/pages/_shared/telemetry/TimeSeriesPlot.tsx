import { useEffect, useId, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { axisTicks, linePath, type ChartPoint, type ChartSeries } from './chart-model'
import { formatNumber, formatTime } from './types'
import { ChartTooltip } from './ChartTooltip'

const HEIGHT = 130
const MARGIN = { top: 24, right: 16, bottom: 36, left: 48 }

export function TimeSeriesPlot({
  points,
  series,
  unit,
  stacked,
  integer,
  emptyLabel,
}: {
  points: ChartPoint[]
  series: ChartSeries[]
  unit: string
  stacked: boolean
  integer: boolean
  emptyLabel: string
}) {
  const container = useRef<HTMLDivElement>(null)
  const tooltipId = useId()
  const [width, setWidth] = useState(800)
  const [activeTime, setActiveTime] = useState<string>()

  useEffect(() => {
    if (!container.current) return
    const observer = new ResizeObserver(([entry]) =>
      setWidth(Math.max(240, entry.contentRect.width)),
    )
    observer.observe(container.current)
    return () => observer.disconnect()
  }, [])

  const plotWidth = width - MARGIN.left - MARGIN.right
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom
  const cellWidth = plotWidth / Math.max(1, points.length)
  const x = (index: number) => MARGIN.left + (index + 0.5) * cellWidth
  const maximum = points.reduce((max, point) => {
    const values = series.map((item) => point.values[item.key] ?? 0)
    return Math.max(
      max,
      stacked ? values.reduce((sum, value) => sum + value, 0) : Math.max(0, ...values),
    )
  }, 0)
  const ticks = axisTicks(maximum, integer)
  const ceiling = ticks[ticks.length - 1]
  const y = (value: number) => HEIGHT - MARGIN.bottom - (value / ceiling) * plotHeight
  const activeIndex = points.findIndex((point) => point.timestamp === activeTime)
  const activePoint = points[activeIndex]
  const tickCount = width < 500 ? 3 : 6
  const timeIndices = [
    ...new Set(
      Array.from({ length: tickCount }, (_, i) =>
        Math.round((i * (points.length - 1)) / (tickCount - 1)),
      ),
    ),
  ].filter((index) => index >= 0)

  function inspectPointer(event: PointerEvent<HTMLDivElement>) {
    if (!points.length) return
    const bounds = event.currentTarget.getBoundingClientRect()
    const index = Math.floor((event.clientX - bounds.left - MARGIN.left) / cellWidth)
    setActiveTime(points[Math.max(0, Math.min(points.length - 1, index))].timestamp)
  }

  function inspectKeyboard(event: KeyboardEvent<HTMLDivElement>) {
    if (!points.length) return
    let next = activeIndex < 0 ? points.length - 1 : activeIndex
    if (event.key === 'ArrowLeft') next--
    else if (event.key === 'ArrowRight') next++
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = points.length - 1
    else if (event.key === 'Escape') {
      setActiveTime(undefined)
      return
    } else return
    event.preventDefault()
    setActiveTime(points[Math.max(0, Math.min(points.length - 1, next))].timestamp)
  }

  return (
    <div
      ref={container}
      className="relative mx-4 mt-1.5 h-[130px] touch-pan-y rounded-ui [&_svg]:block [&_svg]:overflow-visible"
      tabIndex={0}
      role="group"
      aria-label={`${unit} chart. Use left and right arrow keys to inspect each minute.`}
      aria-describedby={activePoint ? tooltipId : undefined}
      onPointerMove={inspectPointer}
      onPointerDown={inspectPointer}
      onPointerLeave={() => setActiveTime(undefined)}
      onFocus={() => setActiveTime(points[points.length - 1]?.timestamp)}
      onBlur={() => setActiveTime(undefined)}
      onKeyDown={inspectKeyboard}
    >
      <svg width="100%" height={HEIGHT} viewBox={`0 0 ${width} ${HEIGHT}`} aria-hidden="true">
        <text x={MARGIN.left} y={13} className="fill-muted font-mono text-[9px]">
          {unit}
        </text>
        {ticks.map((tick) => (
          <g key={tick}>
            <line
              x1={MARGIN.left}
              x2={width - MARGIN.right}
              y1={y(tick)}
              y2={y(tick)}
              className="stroke-border [stroke-dasharray:2_4]"
            />
            <text
              x={MARGIN.left - 10}
              y={y(tick) + 4}
              textAnchor="end"
              className="fill-muted font-mono text-[10px]"
            >
              {formatNumber(tick, tick < 1 ? 3 : 1)}
            </text>
          </g>
        ))}
        {points.map(
          (point, index) =>
            point.partial && (
              <rect
                key={point.timestamp}
                x={x(index) - cellWidth / 2}
                y={MARGIN.top}
                width={cellWidth}
                height={plotHeight}
                className="fill-track"
              />
            ),
        )}
        {stacked
          ? points.map((point, index) => {
              let base = 0
              return series.map((item) => {
                const value = point.values[item.key] ?? 0
                const top = base + value
                const rect = value > 0 && (
                  <rect
                    key={`${point.timestamp}-${item.key}`}
                    x={x(index) - cellWidth * 0.4}
                    y={y(top)}
                    width={Math.max(0.2, cellWidth * 0.8)}
                    height={y(base) - y(top)}
                    fill={item.color}
                    opacity={activePoint && activeIndex !== index ? 0.4 : 0.9}
                  />
                )
                base = top
                return rect
              })
            })
          : series.map((item) => (
              <g key={item.key}>
                <path
                  d={linePath(points, item.key, x, y)}
                  fill="none"
                  stroke={item.color}
                  strokeWidth={2}
                  strokeDasharray={item.dash}
                  strokeLinejoin="round"
                />
                {points.map((point, index) => {
                  const value = point.values[item.key]
                  return (
                    value != null &&
                    value > 0 && (
                      <circle
                        key={point.timestamp}
                        cx={x(index)}
                        cy={y(value)}
                        r={points.length > 100 ? 1.5 : 3}
                        fill={item.color}
                      />
                    )
                  )
                })}
              </g>
            ))}
        {timeIndices.map(
          (index) =>
            points[index] && (
              <text
                key={index}
                x={x(index)}
                y={HEIGHT - 10}
                textAnchor={index === 0 ? 'start' : index === points.length - 1 ? 'end' : 'middle'}
                className="fill-muted font-mono text-[10px]"
              >
                {formatTime(points[index].timestamp)}
              </text>
            ),
        )}
        {activePoint && (
          <line
            x1={x(activeIndex)}
            x2={x(activeIndex)}
            y1={MARGIN.top}
            y2={HEIGHT - MARGIN.bottom}
            className="stroke-muted stroke-1 [stroke-dasharray:4_3]"
          />
        )}
        {!stacked &&
          activePoint &&
          series.map((item) => {
            const value = activePoint.values[item.key]
            return (
              value != null && (
                <circle
                  key={item.key}
                  cx={x(activeIndex)}
                  cy={y(value)}
                  r={4}
                  fill={item.color}
                  className="stroke-surface stroke-2"
                />
              )
            )
          })}
      </svg>

      {(!series.length || maximum === 0) && !activePoint && (
        <div className="pointer-events-none absolute inset-x-[10%] inset-y-[20%] flex flex-col items-center justify-center gap-2 text-center text-[11px] text-muted [&_span]:bg-surface [&_span]:px-2 [&_span]:py-1 [&_strong]:bg-surface [&_strong]:px-2 [&_strong]:py-1 [&_strong]:font-medium [&_strong]:text-ink">
          <strong>{series.length ? emptyLabel : 'All series hidden'}</strong>
          <span>
            {series.length
              ? 'Generate an action or choose a wider time range.'
              : 'Select a series above to show it.'}
          </span>
        </div>
      )}
      {activePoint && (
        <ChartTooltip
          id={tooltipId}
          point={activePoint}
          series={series}
          stacked={stacked}
          unit={unit}
          left={Math.max(
            8,
            Math.min(
              width - 240,
              x(activeIndex) > width / 2 ? x(activeIndex) - 240 : x(activeIndex) + 16,
            ),
          )}
        />
      )}
    </div>
  )
}
