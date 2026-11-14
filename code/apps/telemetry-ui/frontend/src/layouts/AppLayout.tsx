import { useEffect, useState } from 'react'
import { Link, Outlet } from '@tanstack/react-router'
import { LuMoon, LuSun } from 'react-icons/lu'
import { Button } from '../components/Button'
import { AuthGate, SignOut } from './AuthGate'
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
const telemetryNavigation = [
  { to: '/', label: 'Overview' },
  { to: '/traces', label: 'Traces' },
  { to: '/logs', label: 'Logs' },
] as const
const adminNavigation = [
  { to: '/admin/organizations', label: 'Organizations' },
  { to: '/admin/data-sources', label: 'Data sources' },
  { to: '/admin/access-grants', label: 'Access grants' },
] as const
const adminPermission = 'observability:admin:read'

function Shell() {
  const { metadata } = useMetadata()
  const canViewAdmin = metadata?.actor.permissions.includes(adminPermission) ?? false
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
          className="flex min-h-8 shrink-0 items-center gap-2 px-2 text-[13px] font-semibold text-ink"
        >
          <span className="grid size-6 place-items-center rounded bg-accent text-[11px] text-accent-text">
            S
          </span>
          <span className="max-sm:hidden">Signal Deck</span>
        </Link>
        <nav
          aria-label="Primary navigation"
          className="mt-7 grid gap-5 max-md:mt-0 max-md:flex max-md:min-w-0 max-md:flex-1 max-md:items-center max-md:gap-2 max-md:overflow-x-auto"
        >
          <NavigationGroup label="Telemetry" items={telemetryNavigation} />
          {canViewAdmin && <NavigationGroup label="Admin" items={adminNavigation} divided />}
        </nav>
        <div className="mt-auto grid gap-2 max-md:mt-0 max-md:ml-auto max-md:flex max-md:shrink-0">
          <Button
            className="flex h-7 items-center gap-2 text-[10px] max-sm:w-7 max-sm:justify-center max-sm:p-0"
            aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
            aria-pressed={theme === 'dark'}
            onClick={() => setTheme((current) => (current === 'dark' ? 'light' : 'dark'))}
          >
            {theme === 'dark' ? <LuMoon aria-hidden size={13} /> : <LuSun aria-hidden size={13} />}
            <span className="max-sm:hidden">{theme === 'dark' ? 'Dark' : 'Light'}</span>
          </Button>
          <SignOut />
          <div className="grid gap-0.5 border-t border-border p-2.5 max-md:hidden">
            <span className="font-mono text-[9px] font-semibold tracking-widest text-muted uppercase">
              Active scope
            </span>
            <strong className="text-[11px] font-semibold">
              {metadata?.actor.organizationName ??
                metadata?.actor.organizationScope ??
                'connecting'}
            </strong>
            <small className="truncate text-[10px] text-dim">
              {metadata
                ? `${metadata.actor.displayName} · ${metadata.actor.organizationScope}`
                : 'Resolving access…'}
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

function NavigationGroup({
  label,
  items,
  divided = false,
}: {
  label: string
  items: ReadonlyArray<{
    to:
      | '/'
      | '/traces'
      | '/logs'
      | '/admin/organizations'
      | '/admin/data-sources'
      | '/admin/access-grants'
    label: string
  }>
  divided?: boolean
}) {
  return (
    <section
      className={`grid gap-1 max-md:flex max-md:items-center ${divided ? 'max-md:border-l max-md:border-border max-md:pl-2' : ''}`}
    >
      <h2 className="px-2 font-mono text-[9px] font-semibold tracking-[0.16em] text-dim uppercase max-md:hidden">
        {label}
      </h2>
      <div className="grid gap-0.5 max-md:flex">
        {items.map((item, index) => (
          <Link
            key={item.to}
            to={item.to}
            activeOptions={{ exact: item.to === '/' }}
            className="rounded-ui px-2 py-2 text-xs whitespace-nowrap text-muted hover:bg-hover hover:text-ink data-[status=active]:bg-accent-soft data-[status=active]:text-accent-strong"
          >
            <span className="mr-2 font-mono text-[9px] text-dim max-md:hidden">
              {String(index + 1).padStart(2, '0')}
            </span>
            {item.label}
          </Link>
        ))}
      </div>
    </section>
  )
}

export default function AppLayout() {
  return (
    <AuthGate>
      <MetadataProvider>
        <Shell />
      </MetadataProvider>
    </AuthGate>
  )
}
