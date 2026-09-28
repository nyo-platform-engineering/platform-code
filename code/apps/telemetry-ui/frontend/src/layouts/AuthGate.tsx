import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { Button } from '../components/Button'

type Providers = { mode: 'local' | 'oauth'; providers: string[] }

const AuthModeContext = createContext<'local' | 'oauth'>('local')

const loginErrors: Record<string, string> = {
  access_denied: 'Your account does not have access. Contact your administrator.',
  cancelled: 'Sign-in was cancelled. You can try again below.',
  invalid_login: 'This sign-in attempt expired or was already used. Please try again.',
  login_failed: 'Sign-in could not be completed. Please try again.',
}

export function AuthGate({ children }: { children: ReactNode }) {
  const [providers, setProviders] = useState<Providers | null>(null)
  const [signedIn, setSignedIn] = useState(false)
  const [loading, setLoading] = useState(true)
  const [problem, setProblem] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setProblem(null)
    async function load() {
      const options = {
        signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)]),
        cache: 'no-store' as const,
      }
      const response = await fetch('/api/v1/auth/providers', options)
      if (!response.ok) throw new Error('Sign-in is unavailable. Please try again.')
      const config = (await response.json()) as Providers
      setProviders(config)
      if (config.mode === 'local') {
        setSignedIn(true)
        return
      }
      const session = await fetch('/api/v1/auth/session', options)
      if (session.status !== 401 && !session.ok) throw new Error('Unable to check your session.')
      setSignedIn(session.ok)
    }
    void load()
      .catch((error: Error) => {
        if (!controller.signal.aborted) setProblem(error.message)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    const expired = () => setSignedIn(false)
    window.addEventListener('session-expired', expired)
    return () => {
      controller.abort()
      window.removeEventListener('session-expired', expired)
    }
  }, [attempt])

  if (loading)
    return <main className="grid min-h-screen place-items-center text-muted">Checking access…</main>
  if (signedIn && !problem && providers)
    return <AuthModeContext.Provider value={providers.mode}>{children}</AuthModeContext.Provider>

  const loginError = new URLSearchParams(window.location.search).get('auth_error')
  const message = problem ?? (loginError ? loginErrors[loginError] : null)
  return (
    <main className="grid min-h-screen place-items-center p-6">
      <section className="w-full max-w-sm rounded-ui border border-border bg-rail p-8">
        <p className="mb-6 text-xs font-semibold tracking-widest text-muted uppercase">
          Signal Deck
        </p>
        <h1 className="text-2xl font-semibold text-ink">Sign in</h1>
        <p className="mt-2 mb-6 text-sm text-muted">
          Use your team account to explore traces and logs.
        </p>
        {message && (
          <p role="alert" className="mb-4 text-sm text-muted">
            {message}
          </p>
        )}
        {problem ? (
          <Button onClick={() => setAttempt((value) => value + 1)}>Try again</Button>
        ) : (
          <div className="grid gap-3">
            {providers?.providers.map((provider) => (
              <a
                key={provider}
                href={`/api/v1/auth/${provider}/login`}
                className="rounded-ui border border-border px-4 py-3 text-center text-sm font-medium text-ink hover:bg-hover"
              >
                Continue with {provider === 'google' ? 'Google' : 'GitHub'}
              </a>
            ))}
          </div>
        )}
      </section>
    </main>
  )
}

export function SignOut() {
  const mode = useContext(AuthModeContext)
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState(false)
  if (mode === 'local') return null
  async function logout() {
    setBusy(true)
    setProblem(false)
    try {
      const response = await fetch('/api/v1/auth/logout', {
        method: 'POST',
        headers: { 'X-Telemetry-CSRF': '1' },
      })
      if (!response.ok) throw new Error('Logout failed')
      window.location.assign('/')
    } catch {
      setProblem(true)
      setBusy(false)
    }
  }
  return (
    <div>
      <Button onClick={() => void logout()} disabled={busy}>
        {busy ? 'Signing out…' : 'Sign out'}
      </Button>
      {problem && (
        <p role="alert" className="mt-1 text-xs text-muted">
          Could not sign out. Try again.
        </p>
      )}
    </div>
  )
}
