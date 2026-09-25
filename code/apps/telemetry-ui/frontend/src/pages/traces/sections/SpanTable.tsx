import { Table } from '../../../components/Table'
import { formatNumber, type SpanRecord } from '../../_shared/telemetry/types'
import { StatusLabel } from './StatusLabel'
export function SpanTable({ rows }: { rows: SpanRecord[] }) {
  const start = Math.min(...rows.map((row) => Date.parse(row.timestamp)))
  const duration = Math.max(
    1,
    ...rows.map((row) => Date.parse(row.timestamp) - start + row.durationMs),
  )

  return (
    <div className="overflow-auto">
      <Table>
        <thead>
          <tr>
            <th>Span</th>
            <th>Duration</th>
            <th>Timeline</th>
            <th>Status / attributes</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const offset = (100 * (Date.parse(row.timestamp) - start)) / duration
            const width = Math.min(100 - offset, Math.max(0.5, (100 * row.durationMs) / duration))
            return (
              <tr key={row.spanId}>
                <td className="min-w-40">
                  {row.parentSpanId ? '↳ ' : ''}
                  {row.name}
                  <small className="mt-1 block font-mono text-[9px] text-dim">{row.spanId}</small>
                </td>
                <td className="font-mono whitespace-nowrap tabular-nums">
                  {formatNumber(row.durationMs, 3)} ms
                </td>
                <td>
                  <div className="mt-1 h-2 w-44 overflow-hidden bg-track [&_i]:block [&_i]:h-full [&_i]:bg-accent">
                    <i style={{ marginLeft: `${offset}%`, width: `${width}%` }} />
                  </div>
                </td>
                <td>
                  <StatusLabel status={row.status} />
                  <details className="[&_pre]:max-w-[360px] [&_pre]:text-[10px] [&_pre]:wrap-anywhere [&_pre]:whitespace-pre-wrap [&_summary]:pt-1.5 [&_summary]:text-[10px] [&_summary]:text-muted">
                    <summary>Attributes</summary>
                    <pre>{JSON.stringify(row.attributes, null, 2)}</pre>
                  </details>
                </td>
              </tr>
            )
          })}
        </tbody>
      </Table>
    </div>
  )
}
