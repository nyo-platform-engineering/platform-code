import { useEffect, useState } from 'react'
import { Link, Outlet } from '@tanstack/react-router'
import { LuMoon, LuSun } from 'react-icons/lu'
import { Button } from '../components/Button'
import { MetadataProvider, useMetadata } from './MetadataProvider'

type Theme = 'light' | 'dark'
function initialTheme(): Theme {
  try {
    const saved = localStorage.getItem('signal-deck-theme')
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    /* Storage may be unavailable. */
  }
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}
const navigation = [
  { to: '/', label: 'Overview' },
  { to: '/traces', label: 'Traces' },
  { to: '/logs', label: 'Logs' },
] as const

function Shell() {
  const { metadata } = useMetadata()
  const [theme, setTheme] = useState<Theme>(initialTheme)
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try {
      localStorage.setItem('signal-deck-theme', theme)
    } catch {
      /* Theme still applies this session. */
    }
  }, [theme])
  return (
    <div className="grid min-h-screen grid-cols-[172px_minmax(0,1fr)] max-md:grid-cols-1 max-md:content-start">
      <aside className="sticky top-0 z-10 flex h-screen flex-col border-r border-border bg-rail px-3 py-4 max-md:h-auto max-md:flex-row max-md:items-center max-md:gap-2 max-md:border-r-0 max-md:border-b max-md:p-2">
        <Link
          to="/"
          aria-label="Signal Deck home"
          className="flex min-h-8 items-center gap-2 px-2 text-[13px] font-semibold text-ink"
        >
          <span className="grid size-6 place-items-center rounded bg-accent text-[11px] text-accent-text">
            S
          </span>
          <span className="max-sm:hidden">Signal Deck</span>
        </Link>
        <nav aria-label="Primary navigation" className="mt-7 grid gap-0.5 max-md:mt-0 max-md:flex">
          {navigation.map((item, index) => (
            <Link
              key={item.to}
              to={item.to}
              activeOptions={{ exact: item.to === '/' }}
              className="rounded-ui px-2 py-2 text-xs text-muted hover:bg-hover hover:text-ink data-[status=active]:bg-accent-soft data-[status=active]:text-accent-strong"
            >
              <span className="mr-2 font-mono text-[9px] text-dim max-md:hidden">0{index + 1}</span>
              {item.label}
            </Link>
          ))}
        </nav>
        <div className="mt-auto grid gap-2 max-md:mt-0 max-md:ml-auto max-md:flex">
          <Button
            className="flex h-7 items-center gap-2 text-[10px] max-sm:w-7 max-sm:justify-center max-sm:p-0"
            aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
            aria-pressed={theme === 'dark'}
            onClick={() => setTheme((current) => (current === 'dark' ? 'light' : 'dark'))}
          >
            {theme === 'dark' ? <LuMoon aria-hidden size={13} /> : <LuSun aria-hidden size={13} />}
            <span className="max-sm:hidden">{theme === 'dark' ? 'Dark' : 'Light'}</span>
          </Button>
          <div className="grid gap-0.5 border-t border-border p-2.5 max-md:hidden">
            <span className="font-mono text-[9px] font-semibold tracking-widest text-muted uppercase">
              Active scope
            </span>
            <strong className="text-[11px] font-semibold">
              {metadata?.actor.tenant ?? 'connecting'}
            </strong>
            <small className="truncate text-[10px] text-dim">
              {metadata?.actor.displayName ?? 'Resolving access…'}
            </small>
          </div>
        </div>
      </aside>
      <main className="min-w-0 overflow-hidden px-[clamp(20px,3vw,42px)] pt-6 pb-4 max-md:px-3.5 max-md:pt-4">
        <Outlet />
        <footer className="mt-7 flex justify-between gap-2 border-t border-border pt-2.5 font-mono text-[9px] text-dim max-sm:flex-col">
          <span>API {metadata?.version ?? '…'}</span>
          <span>API instrumentation: server-side only</span>
        </footer>
      </main>
    </div>
  )
}
export default function AppLayout() {
  return (
    <MetadataProvider>
      <Shell />
    </MetadataProvider>
  )
}
