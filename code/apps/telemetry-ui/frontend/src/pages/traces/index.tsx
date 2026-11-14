import { TelemetryWorkspace } from '../_shared/telemetry/TelemetryWorkspace'
import { TraceCharts } from './sections/TraceCharts'
import { TraceTable } from './sections/TraceTable'
import { TraceSummary } from './sections/TraceSummary'
import { TraceDrawer } from './sections/TraceDrawer'

export function TracesPage() {
  return (
    <TelemetryWorkspace
      mode="traces"
      renderChart={(data) => <TraceCharts data={data} />}
      renderSummary={(data) => <TraceSummary data={data} />}
      renderRecords={(data, onSelect) => <TraceTable rows={data.traces} onSelect={onSelect} />}
      renderSelection={(props) => <TraceDrawer key={props.traceId} {...props} />}
    />
  )
}
