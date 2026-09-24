import { createFileRoute } from '@tanstack/react-router'
import { OverviewCard, PageHeader } from '../App'

export const Route = createFileRoute('/')({
  component: OverviewPage,
})

function OverviewPage() {
  return (
    <>
      <PageHeader eyebrow="Workspace" title="Overview" controls={false} />
      <section className="section-block">
        <div className="section-heading">
          <div><span className="eyebrow">Signal views</span><h2>Explore telemetry</h2></div>
        </div>
        <div className="overview-grid">
          <OverviewCard to="/traces" eyebrow="Trace health" title="RED by service">
            Request rate, errors, and duration with one-minute buckets.
          </OverviewCard>
          <OverviewCard to="/logs" eyebrow="Log pressure" title="Volume by severity">
            Compare OpenTelemetry severity bands over time.
          </OverviewCard>
        </div>
      </section>
    </>
  )
}
