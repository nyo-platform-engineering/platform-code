import { AdminCollectionPage, DetailField } from './CollectionPage'

type AccessGrant = {
  id: string
  provider: string
  selectorType: string
  selectorValue: string
  organizationId: string
  organizationName: string
  managedBy: string
  permissions: string[]
}

export function AccessGrantsPage() {
  return (
    <AdminCollectionPage<AccessGrant>
      title="Access grants"
      description="Provider identities mapped to the active organization and their observability permissions."
      endpoint="/api/v1/admin/access-grants"
      empty="No matching access grants."
      rowKey={(item) => item.id}
      detailTitle={(item) => item.selectorValue}
      renderDetails={(item) => (
        <dl className="rounded-ui border border-border bg-surface px-4">
          <DetailField label="Grant ID">
            <span className="font-mono">{item.id}</span>
          </DetailField>
          <DetailField label="Provider">
            <span className="capitalize">{item.provider}</span>
          </DetailField>
          <DetailField label="Selector type">{item.selectorType}</DetailField>
          <DetailField label="Selector value">
            <span className="font-mono">{item.selectorValue}</span>
          </DetailField>
          <DetailField label="Organization">
            <strong>{item.organizationName}</strong>
            <span className="mt-0.5 block font-mono text-[10px] text-muted">
              {item.organizationId}
            </span>
          </DetailField>
          <DetailField label="Owner">{item.managedBy}</DetailField>
          <DetailField label="Permissions">
            {item.permissions.length === 0 ? (
              <span className="text-muted">No explicit permissions</span>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {item.permissions.map((permission) => (
                  <span
                    key={permission}
                    className="rounded bg-accent-soft px-1.5 py-0.5 font-mono text-[9px] text-accent-strong"
                  >
                    {permission}
                  </span>
                ))}
              </div>
            )}
          </DetailField>
        </dl>
      )}
      columns={[
        {
          label: 'Provider',
          render: (item) => <span className="capitalize">{item.provider}</span>,
        },
        {
          label: 'Selector',
          render: (item) => (
            <div>
              <span className="font-mono text-[9px] text-dim uppercase">{item.selectorType}</span>
              <strong className="mt-0.5 block font-mono font-medium">{item.selectorValue}</strong>
            </div>
          ),
        },
        {
          label: 'Permissions',
          render: (item) => (
            <div className="flex max-w-md flex-wrap gap-1">
              {item.permissions.map((permission) => (
                <span
                  key={permission}
                  className="rounded bg-accent-soft px-1.5 py-0.5 font-mono text-[9px] text-accent-strong"
                >
                  {permission}
                </span>
              ))}
            </div>
          ),
        },
        {
          label: 'Organization',
          render: (item) => (
            <div>
              <strong className="font-medium">{item.organizationName}</strong>
              <span className="mt-0.5 block font-mono text-[9px] text-dim">
                {item.organizationId}
              </span>
            </div>
          ),
        },
        { label: 'Owner', render: (item) => <span className="text-muted">{item.managedBy}</span> },
      ]}
    />
  )
}
