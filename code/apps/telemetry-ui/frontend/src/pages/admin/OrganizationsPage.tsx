import { useEffect, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { AdminCollectionPage, DetailField } from './CollectionPage'

type Organization = {
  id: string
  name: string
  telemetryScope: string
}

export function OrganizationsPage() {
  return (
    <AdminCollectionPage<Organization>
      title="Organizations"
      description="Authorization and telemetry-isolation boundaries visible to the active administrator."
      endpoint="/api/v1/admin/organizations"
      empty="No matching organizations."
      rowKey={(item) => item.id}
      detailTitle={(item) => item.name}
      renderDetails={(item) => (
        <div className="grid gap-5">
          <dl className="rounded-ui border border-border bg-surface px-4">
            <DetailField label="Organization">{item.name}</DetailField>
            <DetailField label="ID">
              <span className="font-mono">{item.id}</span>
            </DetailField>
            <DetailField label="Telemetry scope">
              <span className="font-mono">{item.telemetryScope}</span>
            </DetailField>
          </dl>
          <OrganizationIdentities organization={item} />
        </div>
      )}
      columns={[
        { label: 'Organization', render: (item) => <strong>{item.name}</strong> },
        { label: 'ID', render: (item) => <span className="font-mono text-muted">{item.id}</span> },
        {
          label: 'Telemetry scope',
          render: (item) => <span className="font-mono text-muted">{item.telemetryScope}</span>,
        },
      ]}
    />
  )
}

type Identity = {
  id: string
  provider: string
  selectorType: string
  selectorValue: string
  organizationId: string
  permissions: string[]
}

type IdentityPage = {
  data: Identity[]
  nextCursor?: string
}

function OrganizationIdentities({ organization }: { organization: Organization }) {
  const [page, setPage] = useState<IdentityPage | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/admin/access-grants', { signal: controller.signal })
      .then(async (response) => {
        if (response.status === 401) window.dispatchEvent(new Event('session-expired'))
        if (!response.ok) throw new Error(`Identity mappings unavailable (${response.status})`)
        return response.json() as Promise<IdentityPage>
      })
      .then(setPage)
      .catch((error: Error) => {
        if (!controller.signal.aborted) setProblem(error.message)
      })
    return () => controller.abort()
  }, [organization.id])

  return (
    <section>
      <div className="mb-2 flex items-end justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">Users and identities</h3>
          <p className="mt-1 text-[10px] text-muted">
            OAuth subjects, emails, and domains mapped to this organization.
          </p>
        </div>
        <Link
          to="/admin/access-grants"
          className="text-[10px] font-medium text-accent-strong hover:underline"
        >
          View access grants
        </Link>
      </div>
      <div className="overflow-hidden rounded-ui border border-border bg-surface">
        {!page && !problem && <p className="p-4 text-[11px] text-muted">Loading identities…</p>}
        {problem && <p className="p-4 text-[11px] text-danger">{problem}</p>}
        {page?.data.length === 0 && (
          <p className="p-4 text-[11px] text-muted">No users or identity mappings configured.</p>
        )}
        {page?.data.map((identity) => (
          <div
            key={identity.id}
            className="flex items-start justify-between gap-4 border-b border-border p-3 last:border-0"
          >
            <div className="min-w-0">
              <strong className="block truncate font-mono text-[11px] font-medium">
                {identity.selectorValue}
              </strong>
              <span className="mt-1 block text-[9px] text-dim uppercase">
                {identity.provider} · {identity.selectorType}
              </span>
            </div>
            <span className="shrink-0 font-mono text-[9px] text-muted">
              {identity.permissions.length} permission
              {identity.permissions.length === 1 ? '' : 's'}
            </span>
          </div>
        ))}
      </div>
      {page?.nextCursor && (
        <p className="mt-2 text-[10px] text-muted">
          More identities are available in Access grants.
        </p>
      )}
    </section>
  )
}
