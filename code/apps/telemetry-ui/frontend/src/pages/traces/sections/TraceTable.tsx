import { Table } from '../../../components/Table'
import { Button } from '../../../components/Button'
import { LuArrowRight } from 'react-icons/lu'
import { formatNumber, formatTime, type SpanRecord } from '../../_shared/telemetry/types'
import { StatusLabel } from './StatusLabel'
export function TraceTable({
  rows,
  onSelect,
}: {
  rows: SpanRecord[]
  onSelect: (traceId: string) => void
}) {
  if (!rows.length)
    return (
      <p className="px-5 py-6 text-xs text-muted">
        No requests in this window. Generate an action or adjust your filters.
      </p>
    )
  return (
    <div className="overflow-auto">
      <Table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Operation</th>
            <th>Service</th>
            <th>Duration</th>
            <th>Status</th>
            <th>Trace</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={`${row.traceId}-${row.spanId}`}
              className="group cursor-pointer transition-colors duration-150 focus-within:bg-accent-soft hover:bg-accent-soft motion-reduce:transition-none"
              onClick={(event) => {
                // Keep text selectable, and let the native button handle keyboard activation.
                if (
                  (event.target as HTMLElement).closest('button') ||
                  window.getSelection()?.toString()
                )
                  return
                event.currentTarget.querySelector('button')?.focus({ preventScroll: true })
                onSelect(row.traceId)
              }}
            >
              <td className="font-mono text-[10px] whitespace-nowrap text-muted">
                {formatTime(row.timestamp, true)}
              </td>
              <td className="min-w-40">{row.name}</td>
              <td>{row.service}</td>
              <td className="font-mono whitespace-nowrap tabular-nums">
                {formatNumber(row.durationMs)} ms
              </td>
              <td>
                <StatusLabel status={row.status} />
              </td>
              <td>
                <Button
                  className="inline-flex h-auto w-full items-center justify-between gap-4 border-0 bg-transparent p-0 font-mono text-[10px] whitespace-nowrap text-accent-strong no-underline hover:bg-transparent hover:underline [&_svg]:shrink-0 [&_svg]:text-muted group-focus-within:[&_svg]:text-accent-strong group-hover:[&_svg]:text-accent-strong"
                  aria-label={`Open trace ${row.traceId}: ${row.name}`}
                  aria-haspopup="dialog"
                  title="Open trace details"
                  onClick={() => onSelect(row.traceId)}
                >
                  <span>{row.traceId.slice(0, 12)}…</span>
                  <LuArrowRight aria-hidden="true" focusable="false" size={16} />
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
    </div>
  )
}
