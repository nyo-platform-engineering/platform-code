import { Button } from './Button'
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export type SelectOption = { value: string; label: string }
type Props = {
  label: string
  options: SelectOption[]
  values: string[]
  onChange: (values: string[]) => void
  multiple?: boolean
  placeholder?: string
  allowCustom?: boolean
  onOpenChange?: (open: boolean) => void
  onSearchChange?: (query: string) => void
  status?: string
  maxSelected?: number
}

export function SearchableSelect({
  label,
  options,
  values,
  onChange,
  multiple = false,
  placeholder = 'Select an option',
  maxSelected = 20,
  allowCustom = false,
  onOpenChange,
  onSearchChange,
  status,
}: Props) {
  const id = useId()
  const trigger = useRef<HTMLButtonElement>(null)
  const popup = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const [position, setPosition] = useState({ left: 0, top: 0, width: 280, maxHeight: 380 })
  const filtered = options.filter((option) =>
    option.label.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()),
  )
  if (allowCustom && query.trim() && !filtered.some((option) => option.value === query.trim())) {
    filtered.push({ value: query.trim(), label: `Use “${query.trim()}”` })
  }
  const activeIndex = Math.min(active, filtered.length - 1)
  const selected = values.map(
    (value) => options.find((option) => option.value === value) ?? { value, label: value },
  )
  const text = selected.length ? selected.map((option) => option.label).join(', ') : placeholder

  useEffect(() => {
    onOpenChange?.(open)
  }, [open, onOpenChange])
  useEffect(() => {
    onSearchChange?.(query)
  }, [query, onSearchChange])

  function close(restoreFocus = false) {
    setOpen(false)
    if (restoreFocus) trigger.current?.focus()
  }

  function choose(value: string) {
    if (multiple) {
      if (values.includes(value)) onChange(values.filter((item) => item !== value))
      else if (values.length < maxSelected) onChange([...values, value])
    } else {
      onChange([value])
      close(true)
    }
  }

  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      const rect = trigger.current!.getBoundingClientRect()
      const width = Math.min(Math.max(rect.width, 280), window.innerWidth - 24)
      const below = window.innerHeight - rect.bottom - 20
      const above = rect.top - 20
      const upward = below < 260 && above > below
      const height = Math.min(380, Math.max(120, upward ? above : below))
      setPosition({
        left: Math.max(12, Math.min(rect.left, window.innerWidth - width - 12)),
        top: upward ? Math.max(12, rect.top - height - 6) : rect.bottom + 6,
        width,
        maxHeight: height,
      })
    }
    place()
    input.current?.focus()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    const outside = (event: PointerEvent) => {
      if (
        !popup.current?.contains(event.target as Node) &&
        !trigger.current?.contains(event.target as Node)
      )
        setOpen(false)
    }
    const focus = (event: FocusEvent) => {
      if (
        !popup.current?.contains(event.target as Node) &&
        !trigger.current?.contains(event.target as Node)
      )
        setOpen(false)
    }
    document.addEventListener('pointerdown', outside)
    document.addEventListener('focusin', focus)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
      document.removeEventListener('pointerdown', outside)
      document.removeEventListener('focusin', focus)
    }
  }, [open])

  useEffect(() => {
    if (open)
      document.getElementById(`${id}-option-${activeIndex}`)?.scrollIntoView({ block: 'nearest' })
  }, [activeIndex, id, open])

  return (
    <div className="grid w-full min-w-0 gap-1.5">
      <span id={`${id}-label`} className="text-[11px] font-semibold text-muted">
        {label}
      </span>
      <Button
        ref={trigger}
        type="button"
        className="flex min-h-8.5 w-full items-center gap-2 text-left"
        aria-labelledby={`${id}-label ${id}-value`}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? `${id}-popup` : undefined}
        onClick={() => {
          setQuery('')
          setActive(0)
          setOpen(!open)
        }}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault()
            setQuery('')
            setActive(0)
            setOpen(true)
          }
        }}
      >
        <span id={`${id}-value`} className="min-w-0 flex-1 truncate" title={text}>
          {text}
        </span>
        {multiple && values.length > 1 && (
          <span className="rounded bg-accent-soft px-1 text-[11px] text-ink">{values.length}</span>
        )}
        <span aria-hidden="true">⌄</span>
      </Button>
      {open &&
        createPortal(
          <div
            ref={popup}
            id={`${id}-popup`}
            role="dialog"
            aria-label={`${label} options`}
            className="fixed z-50 box-border flex flex-col gap-1.5 rounded-lg border border-border-strong bg-surface p-2 font-sans text-xs leading-relaxed text-ink shadow-xl"
            style={position}
            onKeyDown={(event) => {
              if (event.key === 'Escape') {
                event.preventDefault()
                event.stopPropagation()
                close(true)
              }
            }}
          >
            <input
              ref={input}
              className="min-h-9 w-full rounded border border-border bg-canvas px-2.5 py-2 text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
              type="search"
              role="combobox"
              aria-label={`Search ${label.toLowerCase()}`}
              aria-expanded="true"
              aria-autocomplete="list"
              aria-controls={`${id}-list`}
              aria-activedescendant={activeIndex >= 0 ? `${id}-option-${activeIndex}` : undefined}
              placeholder={`Search ${label.toLowerCase()}…`}
              value={query}
              onChange={(event) => {
                setQuery(event.target.value)
                setActive(0)
              }}
              onKeyDown={(event) => {
                if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
                  event.preventDefault()
                  setActive(
                    Math.max(
                      0,
                      Math.min(
                        filtered.length - 1,
                        activeIndex + (event.key === 'ArrowDown' ? 1 : -1),
                      ),
                    ),
                  )
                }
                if ((event.key === 'Home' || event.key === 'End') && event.ctrlKey) {
                  event.preventDefault()
                  setActive(event.key === 'Home' ? 0 : filtered.length - 1)
                }
                if (event.key === 'Enter' && filtered[activeIndex]) {
                  event.preventDefault()
                  choose(filtered[activeIndex].value)
                }
                // Portal content is outside the toolbar's tab order; return to its trigger.
                if (event.key === 'Tab' && event.shiftKey) {
                  event.preventDefault()
                  close(true)
                }
              }}
            />
            <div
              id={`${id}-list`}
              role="listbox"
              aria-label={label}
              aria-multiselectable={multiple || undefined}
              className="min-h-0 overflow-y-auto overscroll-contain"
            >
              {filtered.map((option, index) => {
                const checked = values.includes(option.value)
                const disabled = multiple && !checked && values.length >= maxSelected
                return (
                  <div
                    key={option.value}
                    id={`${id}-option-${index}`}
                    role="option"
                    aria-selected={checked}
                    aria-disabled={disabled || undefined}
                    data-active={index === activeIndex}
                    className="flex cursor-pointer items-center gap-2 rounded p-2 wrap-anywhere aria-disabled:cursor-not-allowed aria-disabled:opacity-50 aria-selected:bg-accent-soft aria-selected:text-ink data-[active=true]:bg-hover"
                    onPointerMove={() => setActive(index)}
                    onPointerDown={(event) => event.preventDefault()}
                    onClick={() => {
                      if (!disabled) choose(option.value)
                    }}
                  >
                    <span className="w-4 shrink-0 text-center" aria-hidden="true">
                      {checked ? '✓' : ''}
                    </span>
                    <span>{option.label}</span>
                  </div>
                )
              })}
            </div>
            <div className="px-2 py-1 text-[11px] text-muted" role="status">
              {status ??
                (!filtered.length
                  ? 'No matching options'
                  : multiple
                    ? `${values.length} selected · ${values.length >= maxSelected ? 'Selection limit reached' : 'Enter to toggle'}`
                    : `${filtered.length} options · Enter to select`)}
            </div>
            <div className="flex justify-end gap-2 border-t border-border pt-1.5">
              {multiple && (
                <Button type="button" disabled={!values.length} onClick={() => onChange([])}>
                  Clear selection
                </Button>
              )}
              <Button
                type="button"
                onClick={() => close(true)}
                onKeyDown={(event) => {
                  if (event.key === 'Tab' && !event.shiftKey) {
                    close(true)
                  }
                }}
              >
                Done
              </Button>
            </div>
          </div>,
          document.body,
        )}
    </div>
  )
}
