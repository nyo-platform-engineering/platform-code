import { Link } from '@tanstack/react-router'
export function NotFoundPage() {
  return (
    <section className="grid min-h-[70vh] content-center justify-items-start gap-3">
      <span className="font-mono text-xs text-muted">404</span>
      <h1 className="text-2xl font-semibold">Signal not found.</h1>
      <p className="text-xs text-muted">The requested workspace page does not exist.</p>
      <Link to="/" className="text-xs text-accent-strong underline">
        Return to overview
      </Link>
    </section>
  )
}
