import type { ComponentProps } from 'react'
import { twMerge } from 'tailwind-merge'

export function Button({ className, type = 'button', ...props }: ComponentProps<'button'>) {
  return (
    <button
      type={type}
      {...props}
      className={twMerge(
        'h-8 cursor-pointer rounded-ui border border-border bg-surface px-2.5 text-[11px] font-medium text-muted hover:border-border-strong hover:bg-surface-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
    />
  )
}
