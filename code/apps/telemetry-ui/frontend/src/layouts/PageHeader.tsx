import type { ReactNode } from 'react'
import { useMetadata } from './MetadataProvider'

export function PageHeader({
  eyebrow,
  title,
  capabilityId,
  compact = false,
  actions,
}: {
  compact?: boolean
  actions?: ReactNode
  eyebrow: string
  title: string
  capabilityId?: string
}) {
  const { metadata, problem } = useMetadata()
  const capability = metadata?.capabilities.find((item) => item.id === capabilityId)
  if (compact)
    return (
      <>
        <header className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-baseline gap-3">
            <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
            <span className="text-xs text-muted">{metadata?.actor.tenant ?? 'Connecting…'}</span>
          </div>
          {actions}
        </header>
        {problem && (
          <p role="alert" className="mt-2 text-xs text-danger">
            {problem}
          </p>
        )}
      </>
    )
  return (
    <>
      <header className="flex min-h-14 items-end justify-between gap-6 border-b border-border pb-3.5">
        <div className="min-w-0">
          <span className="font-mono text-[9px] font-semibold tracking-widest text-muted uppercase">
            {eyebrow}
          </span>
          <div className="mt-1 flex flex-wrap items-center gap-2.5">
            <h1 className="text-[clamp(21px,2.2vw,28px)] leading-tight font-semibold tracking-tight">
              {title}
            </h1>
            {capabilityId && (
              <span className="rounded-full border border-border-strong px-1.5 py-1 font-mono text-[9px] font-semibold text-muted uppercase">
                {capability?.status ?? 'loading'} · {capability?.bucket ?? '1m'}
              </span>
            )}
          </div>
        </div>
      </header>
      {problem ? (
        <div role="alert" className="mt-2 text-xs text-danger">
          {problem}
        </div>
      ) : (
        <div className="mt-2 flex min-h-6 items-center gap-2 text-[10px] text-dim">
          <span className="size-1.5 rounded-full bg-accent ring-3 ring-accent-soft" />
          <span>{metadata ? `${metadata.actor.tenant} scope` : 'Connecting'}</span>
          <span className="h-2.5 w-px bg-border-strong" />
          <span>ClickHouse · live telemetry</span>
        </div>
      )}
    </>
  )
}
