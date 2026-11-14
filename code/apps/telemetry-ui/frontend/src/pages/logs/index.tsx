import { Button } from '../../components/Button'
import { TelemetryWorkspace } from '../_shared/telemetry/TelemetryWorkspace'
import { LogExplorer } from './sections/LogExplorer'
import { LogCharts } from './sections/LogCharts'
import { LogSummary } from './sections/LogSummary'

export function LogsPage() {
  return (
    <TelemetryWorkspace
      mode="logs"
      renderChart={(data) => <LogCharts data={data} />}
      renderSummary={(data) => <LogSummary data={data} />}
      renderRecords={(data, _onSelect, link) => (
        <LogExplorer rows={data.logs} link={link} to={data.to} />
      )}
      renderSelection={({ traceId, onClose }) => (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-ui border border-border-strong bg-accent-soft px-4 py-3 text-[11px] [&_code]:font-mono [&_code]:text-[10px] [&_code]:wrap-anywhere [&>span]:flex [&>span]:min-w-0 [&>span]:flex-wrap [&>span]:gap-2">
          <span>
            Filtered trace <code>{traceId}</code>
          </span>
          <Button onClick={onClose}>Clear</Button>
        </div>
      )}
    />
  )
}
