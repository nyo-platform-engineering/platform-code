import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { LuX } from 'react-icons/lu'
import { Button } from './Button'

// Nested drawers share the body lock; restore it only after the last one closes.
let openDrawers = 0
let originalOverflow = ''

export function DetailDrawer({
  title,
  eyebrow,
  onClose,
  children,
  size = 'wide',
}: {
  title: string
  eyebrow: string
  onClose: () => void
  children: ReactNode
  size?: 'compact' | 'wide'
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const closeButton = useRef<HTMLButtonElement>(null)
  const closeTimer = useRef<number | undefined>(undefined)
  const titleId = useId()
  const [closing, setClosing] = useState(false)
  function requestClose() {
    if (closing) return
    setClosing(true)
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    closeTimer.current = window.setTimeout(onClose, reducedMotion ? 0 : 110)
  }
  useEffect(() => {
    const element = dialog.current!
    const previousFocus = document.activeElement as HTMLElement | null
    element.showModal()
    if (openDrawers === 0) originalOverflow = document.body.style.overflow
    openDrawers += 1
    document.body.style.overflow = 'hidden'
    closeButton.current?.focus()
    return () => {
      window.clearTimeout(closeTimer.current)
      element.close()
      openDrawers -= 1
      if (openDrawers === 0) document.body.style.overflow = originalOverflow
      if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true })
    }
  }, [])
  return createPortal(
    <dialog
      ref={dialog}
      data-drawer-state={closing ? 'closing' : 'open'}
      className={`fixed inset-y-0 right-0 left-auto m-0 h-dvh max-h-dvh max-w-full border-0 border-l border-border-strong bg-canvas p-0 text-ink shadow-2xl backdrop:bg-black/45 backdrop:backdrop-blur-xs max-sm:w-full ${size === 'compact' ? 'w-[min(560px,88vw)]' : 'w-[min(1040px,88vw)]'}`}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault()
        requestClose()
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return
        const rect = event.currentTarget.getBoundingClientRect()
        if (
          event.clientX < rect.left ||
          event.clientX > rect.right ||
          event.clientY < rect.top ||
          event.clientY > rect.bottom
        )
          requestClose()
      }}
    >
      <div className="flex h-full min-w-0 flex-col">
        <header className="flex items-center justify-between border-b border-border bg-surface px-5 py-4">
          <div>
            <p className="text-[10px] tracking-widest text-muted uppercase">{eyebrow}</p>
            <h2 id={titleId} className="mt-1 text-xl font-semibold">
              {title}
            </h2>
          </div>
          <Button
            ref={closeButton}
            aria-label={`Close ${eyebrow.toLowerCase()}`}
            onClick={requestClose}
          >
            <LuX aria-hidden />
          </Button>
        </header>
        {children}
      </div>
    </dialog>,
    document.body,
  )
}
