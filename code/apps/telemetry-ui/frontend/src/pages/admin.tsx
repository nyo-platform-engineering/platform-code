import { useEffect, useState } from 'react'
import { PageHeader } from '../layouts/PageHeader'
import { useMetadata } from '../layouts/MetadataProvider'

const adminPermission = 'observability:admin:read'

type Organization = {
  id: string
  name: string
  telemetryScope: string
}

type DataSource = {
  id: string
  address: string
  database: string
  username: string
  secure: boolean
  managedBy: string
}

type Assignment = {
  organizationId: string
  signal: string
  dataSourceId: string
  managedBy: string
}

type Summary = {
  databaseConfigured: boolean
  organizations: Organization[]
  dataSources: DataSource[]
  assignments: Assignment[]
  grantCount: number
}

function Empty({ children }: { children: string }) {
  return <p className="px-3 py-5 text-center text-[11px] text-dim">{children}</p>
}

export function AdminPage() {
  const { metadata } = useMetadata()
  const allowed = metadata?.actor.permissions.includes(adminPermission) ?? false
  const [summary, setSummary] = useState<Summary | null>(null)
  const [problem, setProblem] = useState<string | null>(null)

  useEffect(() => {
    if (!allowed) return
    const controller = new AbortController()
    fetch('/api/v1/admin/summary', { signal: controller.signal })
      .then(async (response) => {
        if (response.status === 401) window.dispatchEvent(new Event('session-expired'))
        if (!response.ok) throw new Error(`Admin data unavailable (${response.status})`)
        return response.json() as Promise<Summary>
      })
      .then(setSummary)
      .catch((error: Error) => {
        if (!controller.signal.aborted) setProblem(error.message)
      })
    return () => controller.abort()
  }, [allowed])

  if (metadata && !allowed)
    return (
      <>
        <PageHeader eyebrow="Control plane" title="Admin" />
        <p
          role="alert"
          className="mt-5 rounded-ui border border-border bg-surface p-4 text-xs text-muted"
        >
          You do not have permission to view administration data.
        </p>
      </>
    )

  return (
    <>
      <PageHeader eyebrow="Control plane" title="Admin" />
      {problem && (
        <p role="alert" className="mt-4 text-xs text-danger">
          {problem}
        </p>
      )}
      {!problem && !summary && <p className="mt-4 text-xs text-muted">Loading control plane…</p>}
      {summary && (
        <div className="grid gap-5 pt-5">
          {!summary.databaseConfigured && (
            <p className="rounded-ui border border-border bg-surface p-3 text-[11px] text-muted">
              Local mode is active. Connect PostgreSQL with OAuth mode to display control-plane
              records.
            </p>
          )}
          <section className="grid gap-2 sm:grid-cols-3">
            {[
              ['Organizations', summary.organizations.length],
              ['Access grants', summary.grantCount],
              ['Datasources', summary.dataSources.length],
            ].map(([label, value]) => (
              <div key={label} className="rounded-ui border border-border bg-surface p-4">
                <span className="font-mono text-[9px] tracking-widest text-muted uppercase">
                  {label}
                </span>
                <strong className="mt-2 block text-2xl font-semibold">{value}</strong>
              </div>
            ))}
          </section>

          <section>
            <h2 className="mb-2 text-sm font-semibold">Organization scopes</h2>
            <div className="overflow-x-auto rounded-ui border border-border bg-surface">
              {summary.organizations.length === 0 ? (
                <Empty>No organizations configured.</Empty>
              ) : (
                <table className="w-full text-left text-[11px]">
                  <thead className="border-b border-border font-mono text-[9px] text-muted uppercase">
                    <tr>
                      <th className="p-3">Organization</th>
                      <th className="p-3">ID</th>
                      <th className="p-3">Telemetry scope</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.organizations.map((organization) => (
                      <tr key={organization.id} className="border-b border-border last:border-0">
                        <td className="p-3 font-medium">{organization.name}</td>
                        <td className="p-3 font-mono text-muted">{organization.id}</td>
                        <td className="p-3 font-mono text-muted">{organization.telemetryScope}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </section>

          <section>
            <h2 className="mb-2 text-sm font-semibold">Telemetry routing</h2>
            <div className="overflow-x-auto rounded-ui border border-border bg-surface">
              {summary.assignments.length === 0 ? (
                <Empty>No datasource assignments configured.</Empty>
              ) : (
                <table className="w-full text-left text-[11px]">
                  <thead className="border-b border-border font-mono text-[9px] text-muted uppercase">
                    <tr>
                      <th className="p-3">Organization</th>
                      <th className="p-3">Signal</th>
                      <th className="p-3">Datasource</th>
                      <th className="p-3">Owner</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.assignments.map((item) => (
                      <tr
                        key={`${item.organizationId}:${item.signal}:${item.dataSourceId}`}
                        className="border-b border-border last:border-0"
                      >
                        <td className="p-3 font-mono">{item.organizationId}</td>
                        <td className="p-3 capitalize">{item.signal}</td>
                        <td className="p-3 font-mono text-muted">{item.dataSourceId}</td>
                        <td className="p-3 text-muted">{item.managedBy}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </section>

          <section>
            <h2 className="mb-2 text-sm font-semibold">Datasources</h2>
            <div className="grid gap-2 lg:grid-cols-2">
              {summary.dataSources.length === 0 && (
                <div className="rounded-ui border border-border bg-surface">
                  <Empty>No datasources configured.</Empty>
                </div>
              )}
              {summary.dataSources.map((source) => (
                <article key={source.id} className="rounded-ui border border-border bg-surface p-4">
                  <div className="flex items-center justify-between gap-2">
                    <h3 className="text-sm font-semibold">{source.id}</h3>
                    <span className="font-mono text-[9px] text-muted uppercase">
                      {source.managedBy}
                    </span>
                  </div>
                  <p className="mt-2 font-mono text-[11px] text-muted">{source.address}</p>
                  <p className="mt-1 text-[10px] text-dim">
                    {source.database} · {source.username} · {source.secure ? 'TLS' : 'plaintext'}
                  </p>
                </article>
              ))}
            </div>
          </section>
        </div>
      )}
    </>
  )
}
