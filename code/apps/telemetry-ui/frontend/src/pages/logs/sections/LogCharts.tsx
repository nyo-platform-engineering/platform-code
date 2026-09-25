import { TelemetryChart } from '../../_shared/telemetry/TelemetryChart'
import type { TelemetryData } from '../../_shared/telemetry/types'

export function LogCharts({ data }: { data: TelemetryData }) {
  return (
    <div className="grid min-w-0 gap-3 min-[800px]:grid-cols-2">
      <TelemetryChart mode="logs" data={data} logView="volume" />
      <TelemetryChart mode="logs" data={data} logView="severity" />
    </div>
  )
}
