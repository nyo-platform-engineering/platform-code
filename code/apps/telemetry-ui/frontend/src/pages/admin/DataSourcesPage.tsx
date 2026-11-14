import { AdminCollectionPage, DetailField } from './CollectionPage'

type DataSource = {
  id: string
  signal: string
  organizationId: string
  organizationName: string
  address: string
  database: string
  username: string
  secure: boolean
  managedBy: string
  assignmentManagedBy: string
}

export function DataSourcesPage() {
  return (
    <AdminCollectionPage<DataSource>
      title="Data sources"
      description="Non-secret ClickHouse connection metadata and signal routing for the active organization."
      endpoint="/api/v1/admin/data-sources"
      empty="No matching datasource assignments."
      rowKey={(item) => `${item.signal}:${item.id}`}
      detailTitle={(item) => item.id}
      renderDetails={(item) => (
        <div className="grid gap-4">
          <dl className="rounded-ui border border-border bg-surface px-4">
            <DetailField label="Datasource ID">
              <span className="font-mono">{item.id}</span>
            </DetailField>
            <DetailField label="Signal">
              <span className="capitalize">{item.signal}</span>
            </DetailField>
            <DetailField label="Organization">
              <strong>{item.organizationName}</strong>
              <span className="mt-0.5 block font-mono text-[10px] text-muted">
                {item.organizationId}
              </span>
            </DetailField>
            <DetailField label="Address">
              <span className="font-mono">{item.address}</span>
            </DetailField>
            <DetailField label="Database">
              <span className="font-mono">{item.database}</span>
            </DetailField>
            <DetailField label="Username">
              <span className="font-mono">{item.username}</span>
            </DetailField>
            <DetailField label="Transport">{item.secure ? 'TLS' : 'Plaintext'}</DetailField>
            <DetailField label="Datasource owner">{item.managedBy}</DetailField>
            <DetailField label="Routing owner">{item.assignmentManagedBy}</DetailField>
          </dl>
          <p className="text-[11px] leading-5 text-muted">
            Credentials are referenced by the backend and are never returned to the browser.
          </p>
        </div>
      )}
      columns={[
        { label: 'Datasource', render: (item) => <strong className="font-mono">{item.id}</strong> },
        { label: 'Signal', render: (item) => <span className="capitalize">{item.signal}</span> },
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
        { label: 'Address', render: (item) => <span className="font-mono">{item.address}</span> },
        {
          label: 'Database user',
          render: (item) => (
            <span className="text-muted">
              {item.database} · {item.username}
            </span>
          ),
        },
        { label: 'Transport', render: (item) => (item.secure ? 'TLS' : 'Plaintext') },
        {
          label: 'Owner',
          render: (item) => (
            <span className="text-muted">
              {item.managedBy} / {item.assignmentManagedBy}
            </span>
          ),
        },
      ]}
    />
  )
}
