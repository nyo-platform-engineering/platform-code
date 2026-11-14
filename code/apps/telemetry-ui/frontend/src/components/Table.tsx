import type { ComponentProps } from 'react'
import { twMerge } from 'tailwind-merge'

export function Table({ className, ...props }: ComponentProps<'table'>) {
  return (
    <table
      {...props}
      className={twMerge(
        'w-full border-collapse text-left text-[11px] [&_td]:border-b [&_td]:border-border [&_td]:px-3.5 [&_td]:py-2.5 [&_td]:align-top [&_th]:sticky [&_th]:top-0 [&_th]:z-1 [&_th]:border-b [&_th]:border-border [&_th]:bg-surface [&_th]:px-3.5 [&_th]:py-2.5 [&_th]:text-[9px] [&_th]:font-medium [&_th]:tracking-wider [&_th]:whitespace-nowrap [&_th]:text-muted [&_th]:uppercase',
        className,
      )}
    />
  )
}
