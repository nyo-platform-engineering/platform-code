export function Summary({
  label,
  value,
  unit,
  detail,
  tone,
}: {
  label: string
  value: string
  unit?: string
  detail: string
  tone: string
}) {
  return (
    <article
      className="flex min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-1 px-3 py-2 max-[420px]:px-2"
      title={detail}
    >
      <div className="flex items-center gap-1.5 text-[10px] text-muted">
        <span
          className="inline-block size-[7px] shrink-0 rounded-xs"
          style={{ background: `var(--chart-${tone})` }}
        />
        {label}
      </div>
      <div className="font-mono text-base leading-none tracking-tight tabular-nums max-[420px]:text-sm [&_small]:ml-1 [&_small]:text-[10px] [&_small]:font-normal [&_small]:text-dim">
        {value}
        <small>{unit}</small>
      </div>
      <p className="sr-only">{detail}</p>
    </article>
  )
}
