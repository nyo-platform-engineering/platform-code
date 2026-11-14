import { Link } from '@tanstack/react-router'
import { LuArrowRight } from 'react-icons/lu'
import { PageHeader } from '../layouts/PageHeader'

const views = [
  {
    to: '/traces',
    eyebrow: 'Trace health',
    title: 'RED by service',
    description: 'Request rate, errors, and duration with one-minute buckets.',
  },
  {
    to: '/logs',
    eyebrow: 'Log pressure',
    title: 'Volume by severity',
    description: 'Compare OpenTelemetry severity bands over time.',
  },
] as const

export function OverviewPage() {
  return (
    <>
      <PageHeader eyebrow="Workspace" title="Overview" />
      <section className="pt-5">
        <h2 className="mb-3 text-base font-semibold">Explore telemetry</h2>
        <div className="grid gap-2 sm:grid-cols-2">
          {views.map((view) => (
            <Link
              to={view.to}
              key={view.to}
              className="rounded-ui border border-border bg-surface p-4 text-ink transition-colors hover:border-accent hover:bg-surface-hover"
            >
              <span className="font-mono text-[9px] tracking-widest text-muted uppercase">
                {view.eyebrow}
              </span>
              <h3 className="mt-1 text-base font-semibold">{view.title}</h3>
              <p className="mt-2 mb-3 max-w-[48ch] text-[11px] leading-relaxed text-muted">
                {view.description}
              </p>
              <span className="flex items-center gap-1 text-[11px] text-accent-strong">
                Open <LuArrowRight aria-hidden />
              </span>
            </Link>
          ))}
        </div>
      </section>
    </>
  )
}
