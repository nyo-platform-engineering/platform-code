export function SeverityBadge({ severity }: { severity: string }) {
  return (
    <span
      className="inline-flex items-center gap-1.5 font-mono text-[10px] uppercase before:size-1.5 before:rounded-full before:bg-current"
      data-severity={severity}
      style={{ color: `var(--chart-${severity}, var(--chart-unspecified))` }}
    >
      {severity}
    </span>
  )
}
