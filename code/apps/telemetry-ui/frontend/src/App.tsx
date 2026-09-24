import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { Link, Outlet } from '@tanstack/react-router'

export type Capability = {
  id: string
  title: string
  status: 'planned' | 'available'
  bucket: string
  description: string
}

type Metadata = {
  service: string
  version: string
  actor: {
    displayName: string
    tenant: string
    permissions: string[]
  }
  capabilities: Capability[]
}

type MetadataState = {
  metadata: Metadata | null
  problem: string | null
}

const MetadataContext = createContext<MetadataState>({ metadata: null, problem: null })
type Theme = 'light' | 'dark'

function initialTheme(): Theme {
  try {
    const saved = window.localStorage.getItem('signal-deck-theme')
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // Storage can be unavailable in hardened browser contexts.
  }
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function useMetadata() {
  return useContext(MetadataContext)
}

function App() {
  const [metadata, setMetadata] = useState<Metadata | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  const [theme, setTheme] = useState<Theme>(initialTheme)

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try {
      window.localStorage.setItem('signal-deck-theme', theme)
    } catch {
      // The selected theme still applies for the current page session.
    }
  }, [theme])

  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/meta', { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`metadata request failed (${response.status})`)
        return response.json() as Promise<Metadata>
      })
      .then(setMetadata)
      .catch((error: unknown) => {
        if ((error as Error).name !== 'AbortError') setProblem((error as Error).message)
      })
    return () => controller.abort()
  }, [])

  return (
    <MetadataContext.Provider value={{ metadata, problem }}>
      <div className="app-shell antialiased">
        <aside className="rail">
          <Link className="brand" to="/" aria-label="Signal Deck home">
            <span className="brand-mark">S</span>
            <span>Signal Deck</span>
          </Link>
          <nav aria-label="Primary navigation">
            <Link className="nav-item" activeProps={{ className: 'active' }} activeOptions={{ exact: true }} to="/">
              <span>01</span>Overview
            </Link>
            <Link className="nav-item" activeProps={{ className: 'active' }} to="/traces">
              <span>02</span>Traces
            </Link>
            <Link className="nav-item" activeProps={{ className: 'active' }} to="/logs">
              <span>03</span>Logs
            </Link>
          </nav>
          <div className="rail-footer">
            <button
              className="theme-toggle"
              type="button"
              aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
              aria-pressed={theme === 'dark'}
              onClick={() => setTheme((current) => current === 'dark' ? 'light' : 'dark')}
            >
              <ThemeIcon theme={theme} />
              <span>{theme === 'dark' ? 'Dark' : 'Light'}</span>
            </button>
            <div className="identity-card">
              <span className="eyebrow">Active scope</span>
              <strong>{metadata?.actor.tenant ?? 'connecting'}</strong>
              <small>{metadata?.actor.displayName ?? 'Resolving access…'}</small>
            </div>
          </div>
        </aside>

        <main>
          <Outlet />
          <footer>
            <span>API {metadata?.version ?? '…'}</span>
            <span>API instrumentation: server-side only</span>
          </footer>
        </main>
      </div>
    </MetadataContext.Provider>
  )
}

function ThemeIcon({ theme }: { theme: Theme }) {
  if (theme === 'dark') {
    return (
      <svg aria-hidden="true" viewBox="0 0 16 16">
        <path d="M12.7 10.3A5.25 5.25 0 0 1 5.7 3.3 5.25 5.25 0 1 0 12.7 10.3Z" />
      </svg>
    )
  }
  return (
    <svg aria-hidden="true" viewBox="0 0 16 16">
      <circle cx="8" cy="8" r="2.5" />
      <path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.4 1.4M11.6 11.6 13 13M13 3l-1.4 1.4M4.4 11.6 3 13" />
    </svg>
  )
}

export function PageHeader({
  eyebrow,
  title,
  controls = true,
  capabilityId,
}: {
  eyebrow: string
  title: string
  controls?: boolean
  capabilityId?: string
}) {
  const { metadata, problem } = useMetadata()

  return (
    <>
      <header className="topbar">
        <div className="page-title">
          <span className="eyebrow">{eyebrow}</span>
          <div className="title-line">
            <h1>{title}</h1>
            {capabilityId && <FeatureBadge capabilityId={capabilityId} />}
          </div>
        </div>
        {controls && (
          <div className="controls" aria-label="Query controls">
            <label>
              Service
              <select disabled><option>All services</option></select>
            </label>
            <label>
              Window
              <select defaultValue="30m"><option value="30m">Last 30 minutes</option></select>
            </label>
            <button disabled>Run query</button>
          </div>
        )}
      </header>
      {problem && <div className="notice error">{problem}</div>}
      {!problem && (
        <div className="notice">
          <span className="status-dot" />
          <span>{metadata ? `${metadata.actor.tenant} scope` : 'Connecting'}</span>
          <span className="notice-separator" />
          <span>Query adapter pending</span>
        </div>
      )}
    </>
  )
}

export function FeatureBadge({ capabilityId }: { capabilityId: string }) {
  const { metadata } = useMetadata()
  const capability = metadata?.capabilities.find((item) => item.id === capabilityId)
  return <span className="feature-badge">{capability?.status ?? 'loading'} · {capability?.bucket ?? '1m'}</span>
}

export function MetricCard({ label, unit, accent }: { label: string; unit: string; accent: string }) {
  return (
    <article className={`metric-card ${accent}`}>
      <span>{label}</span>
      <strong>—</strong>
      <small>{unit}</small>
    </article>
  )
}

export function NotFoundPage() {
  return (
    <section className="empty-page">
      <span className="eyebrow">404</span>
      <h1>Signal not found.</h1>
      <p>The requested workspace page does not exist.</p>
      <Link className="feature-badge" to="/">Return to overview</Link>
    </section>
  )
}

export function OverviewCard({ to, eyebrow, title, children }: { to: '/traces' | '/logs'; eyebrow: string; title: string; children: ReactNode }) {
  return (
    <Link className="panel overview-card" to={to}>
      <span className="eyebrow">{eyebrow}</span>
      <h2>{title}</h2>
      <p>{children}</p>
      <strong>Open →</strong>
    </Link>
  )
}

export default App
