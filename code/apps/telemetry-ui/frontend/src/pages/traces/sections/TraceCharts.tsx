import { TelemetryChart } from '../../_shared/telemetry/TelemetryChart'
import type { TelemetryData } from '../../_shared/telemetry/types'

export function TraceCharts({ data }: { data: TelemetryData }) {
  return (
    <div className="grid min-w-0 gap-3 min-[1100px]:grid-cols-3">
      <TelemetryChart mode="traces" data={data} metric="duration" />
      <TelemetryChart mode="traces" data={data} metric="requests" />
      <TelemetryChart mode="traces" data={data} metric="errors" />
    </div>
  )
}
