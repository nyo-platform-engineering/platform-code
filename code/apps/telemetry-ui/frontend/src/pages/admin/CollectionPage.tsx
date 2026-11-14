import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { Button } from '../../components/Button'
import { DetailDrawer } from '../../components/DetailDrawer'
import { Table } from '../../components/Table'
import { PageHeader } from '../../layouts/PageHeader'
import { useMetadata } from '../../layouts/MetadataProvider'

const adminPermission = 'observability:admin:read'

type CollectionResponse<T> = {
  databaseConfigured: boolean
  data: T[]
  nextCursor?: string
}

type Column<T> = {
  label: string
  render: (item: T) => ReactNode
}

export function AdminCollectionPage<T>({
  title,
  description,
  endpoint,
  empty,
  rowKey,
  columns,
  detailTitle,
  renderDetails,
}: {
  title: string
  description: string
  endpoint: string
  empty: string
  rowKey: (item: T) => string
  columns: Column<T>[]
  detailTitle: (item: T) => string
  renderDetails: (item: T) => ReactNode
}) {
  const { metadata } = useMetadata()
  const allowed = metadata?.actor.permissions.includes(adminPermission) ?? false
  const [draft, setDraft] = useState('')
  const [request, setRequest] = useState({ search: '', after: '', sequence: 0 })
  const [page, setPage] = useState<CollectionResponse<T> | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [selected, setSelected] = useState<T | null>(null)

  useEffect(() => {
    if (!allowed) return
    const controller = new AbortController()
    const parameters = new URLSearchParams()
    if (request.search) parameters.set('q', request.search)
    if (request.after) parameters.set('after', request.after)
    setLoading(true)
    setPage(null)
    setSelected(null)
    setProblem(null)
    fetch(`${endpoint}${parameters.size ? `?${parameters}` : ''}`, { signal: controller.signal })
      .then(async (response) => {
        if (response.status === 401) window.dispatchEvent(new Event('session-expired'))
        if (!response.ok) throw new Error(`Control-plane data unavailable (${response.status})`)
        return response.json() as Promise<CollectionResponse<T>>
      })
      .then(setPage)
      .catch((error: Error) => {
        if (!controller.signal.aborted) setProblem(error.message)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [allowed, endpoint, request])

  function search(event: FormEvent) {
    event.preventDefault()
    setRequest((current) => ({ search: draft.trim(), after: '', sequence: current.sequence + 1 }))
  }

  if (metadata && !allowed)
    return (
      <>
        <PageHeader eyebrow="Control plane" title={title} />
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
      <PageHeader eyebrow="Control plane" title={title} />
      <p className="mt-2 max-w-2xl text-xs leading-5 text-muted">{description}</p>
      <form className="mt-5 flex max-w-xl gap-2" onSubmit={search}>
        <label className="sr-only" htmlFor="admin-search">
          Search {title.toLowerCase()}
        </label>
        <input
          id="admin-search"
          value={draft}
          maxLength={128}
          onChange={(event) => setDraft(event.target.value)}
          placeholder={`Search ${title.toLowerCase()}…`}
          className="h-8 min-w-0 flex-1 rounded-ui border border-border bg-surface px-3 text-xs text-ink outline-none placeholder:text-dim focus:border-accent"
        />
        <Button type="submit">Search</Button>
      </form>

      {problem && (
        <p role="alert" className="mt-4 text-xs text-danger">
          {problem}
        </p>
      )}
      {!problem && !page && <p className="mt-4 text-xs text-muted">Loading control plane…</p>}
      {page && (
        <div className="mt-5 grid gap-3">
          {!page.databaseConfigured && (
            <p className="rounded-ui border border-border bg-surface p-3 text-[11px] text-muted">
              Local mode is active. Connect PostgreSQL with OAuth mode to display control-plane
              records.
            </p>
          )}
          <div className="overflow-x-auto rounded-ui border border-border bg-surface">
            {page.data.length === 0 ? (
              <p className="px-3 py-8 text-center text-[11px] text-dim">{empty}</p>
            ) : (
              <Table>
                <thead>
                  <tr>
                    {columns.map((column) => (
                      <th key={column.label}>{column.label}</th>
                    ))}
                    <th>
                      <span className="sr-only">Details</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {page.data.map((item) => (
                    <tr
                      key={rowKey(item)}
                      className="cursor-pointer hover:bg-hover"
                      onClick={() => setSelected(item)}
                    >
                      {columns.map((column) => (
                        <td key={column.label}>{column.render(item)}</td>
                      ))}
                      <td className="w-px text-right">
                        <Button
                          className="h-7"
                          aria-label={`Open ${detailTitle(item)}`}
                          onClick={(event) => {
                            event.stopPropagation()
                            setSelected(item)
                          }}
                        >
                          View
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </div>
          <div className="flex items-center justify-between gap-3">
            <span className="font-mono text-[9px] text-dim">
              {loading ? 'Loading…' : `Showing ${page.data.length} records · cursor navigation`}
            </span>
            <div className="flex gap-2">
              <Button
                disabled={!request.after || loading}
                onClick={() =>
                  setRequest((current) => ({
                    ...current,
                    after: '',
                    sequence: current.sequence + 1,
                  }))
                }
              >
                First
              </Button>
              <Button
                disabled={!page.nextCursor || loading}
                onClick={() =>
                  setRequest((current) => ({
                    ...current,
                    after: page.nextCursor ?? '',
                    sequence: current.sequence + 1,
                  }))
                }
              >
                Next
              </Button>
            </div>
          </div>
        </div>
      )}
      {selected && (
        <DetailDrawer
          title={detailTitle(selected)}
          eyebrow={`${title} details`}
          size="compact"
          onClose={() => setSelected(null)}
        >
          <div className="min-h-0 flex-1 overflow-y-auto p-5">{renderDetails(selected)}</div>
        </DetailDrawer>
      )}
    </>
  )
}

export function DetailField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 border-b border-border py-3 last:border-0 sm:grid-cols-[160px_minmax(0,1fr)] sm:gap-5">
      <dt className="font-mono text-[9px] font-semibold tracking-wider text-dim uppercase">
        {label}
      </dt>
      <dd className="min-w-0 text-xs break-words text-ink">{children}</dd>
    </div>
  )
}
