import { createFileRoute } from '@tanstack/react-router'
import { MetricCard, PageHeader } from '../App'

export const Route = createFileRoute('/traces')({
  component: TracesPage,
})

function TracesPage() {
  return (
    <>
      <PageHeader eyebrow="Traces" title="RED by service" capabilityId="trace-red" />
      <section className="section-block">
        <div className="metric-grid">
          <MetricCard label="Request rate" unit="req/min" accent="mint" />
          <MetricCard label="Error rate" unit="percent" accent="coral" />
          <MetricCard label="P95 duration" unit="milliseconds" accent="violet" />
        </div>
        <div className="panel chart-panel">
          <div className="panel-title"><strong>Service profile</strong><span>1 minute buckets</span></div>
          <div className="chart-placeholder" role="img" aria-label="Empty trace RED chart scaffold">
            <div className="chart-grid" />
            <p>ClickHouse query adapter is the next implementation task.</p>
          </div>
        </div>
      </section>
    </>
  )
}
