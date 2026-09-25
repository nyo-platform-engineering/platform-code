import { Button } from '../../../components/Button'
import { SQLPreview } from './SQLPreview'
import { TelemetrySearch } from './TelemetrySearch'
import { filterValues } from './filters'
import { useState, type ReactNode } from 'react'
import { PageHeader } from '../../../layouts/PageHeader'
import { useTelemetryData, useTelemetryFilters } from './hooks'
import { QueryControls } from './QueryControls'
import { TelemetryChart } from './TelemetryChart'
import {
  formatTime,
  windowMinutes,
  type TelemetryMode,
  type TraceLink,
  type TelemetryData,
} from './types'

type SelectionProps = {
  traceId: string
  search: URLSearchParams
  link: TraceLink
  onClose: () => void
}
type Props = {
  mode: TelemetryMode
  renderChart?: (data: TelemetryData) => ReactNode
  renderSummary: (data?: TelemetryData) => ReactNode
  renderRecords: (data: TelemetryData, onSelect: (id: string) => void, link: TraceLink) => ReactNode
  renderSelection: (props: SelectionProps) => ReactNode
}
export function TelemetryWorkspace({
  mode,
  renderChart,
  renderSummary,
  renderRecords,
  renderSelection,
}: Props) {
  const { search, setFilter, setSearchQuery } = useTelemetryFilters()
  const [paused, setPaused] = useState(
    () => Number(search.get('offset')) > 0 || Boolean(search.get('q')) || search.has('attr'),
  )
  const { data, updated, problem, loading, services, refresh } = useTelemetryData(
    mode,
    search,
    paused,
  )
  const [showCharts, setShowCharts] = useState(false)
  const traceId = search.get('traceId') ?? ''
  const link: TraceLink = (id, target) => {
    const params = new URLSearchParams({ traceId: id, minutes: String(windowMinutes(search)) })
    const services = filterValues(search, 'service')
    for (const service of services.length ? services : ['']) params.append('service', service)
    return `/${target}?${params}`
  }

  function toggleLive() {
    if (paused) setFilter('offset', '0')
    setPaused((current) => !current)
  }

  function showOlder() {
    if (data?.page.nextOffset == null) return
    setPaused(true)
    setFilter('offset', String(data.page.nextOffset))
  }

  return (
    <>
      <PageHeader
        compact
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button aria-pressed={!paused} onClick={toggleLive} className="flex items-center gap-2">
              <span className={`size-1.5 rounded-full ${paused ? 'bg-dim' : 'bg-accent'}`} />
              {paused ? 'Resume live' : 'Live · pause'}
            </Button>
            <Button onClick={refresh} disabled={loading}>
              Refresh
            </Button>
            <span className="text-[11px] text-muted">
              {paused ? 'Updates paused' : problem ? 'Retrying…' : 'Every 2s'}
              {updated ? ` · Updated ${formatTime(updated, true)}` : ''}
            </span>
          </div>
        }
        eyebrow="Observability"
        title={mode === 'traces' ? 'Traces' : 'Logs'}
        capabilityId={mode === 'traces' ? 'trace-red' : 'log-volume'}
      />
      <section className="grid min-w-0 gap-3 pt-3">
        <section
          className="min-w-0 rounded-ui border border-border bg-surface-raised"
          aria-label="Search and filters"
        >
          <div className="grid min-w-0 gap-3 p-3">
            <TelemetrySearch
              key={`${mode}:${search.get('q') ?? ''}:${search.getAll('attr').join('|')}`}
              value={search.get('q') ?? ''}
              search={search}
              attributes={search.getAll('attr')}
              mode={mode}
              onSubmit={(value, attributes) => {
                setPaused(true)
                setSearchQuery(value, attributes)
                if (value === (search.get('q') ?? '')) refresh()
              }}
              controls={
                <QueryControls
                  mode={mode}
                  search={search}
                  services={services}
                  setFilter={setFilter}
                />
              }
            />
          </div>
        </section>
        <section aria-label="Metrics for applied search" className="grid min-w-0 gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <div className="min-w-0 flex-1">{renderSummary(data)}</div>
            <Button
              className="border-accent/40 bg-accent-soft font-medium text-accent-strong hover:border-accent hover:bg-accent-soft"
              aria-expanded={showCharts}
              aria-controls="telemetry-charts"
              onClick={() => setShowCharts((value) => !value)}
            >
              {showCharts ? 'Hide charts' : 'Show charts'}
            </Button>
          </div>
          {showCharts && (
            <div id="telemetry-charts">
              {data ? (
                renderChart ? (
                  renderChart(data)
                ) : (
                  <TelemetryChart mode={mode} data={data} />
                )
              ) : (
                <p role="status" className="p-3 text-xs text-muted">
                  {problem ? 'Charts unavailable. Refresh to retry.' : 'Loading charts…'}
                </p>
              )}
            </div>
          )}
        </section>
        {problem && (
          <div
            className="rounded-ui border border-danger px-4 py-3 text-xs text-danger"
            role="alert"
          >
            {problem}
            {data && ' Showing the last successful result.'}
          </div>
        )}

        {traceId &&
          renderSelection({
            traceId,
            search,
            link,
            onClose: () => setFilter('traceId', '', mode === 'logs'),
          })}

        <section
          className="min-w-0 overflow-hidden rounded-ui border border-border bg-surface-raised"
          aria-label={mode === 'traces' ? 'Recent requests' : 'Log records'}
        >
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3.5 py-2.5 [&_h2]:text-xs [&_h2]:font-semibold [&>span]:font-mono [&>span]:text-[10px] [&>span]:text-muted">
            <h2>{mode === 'traces' ? 'Recent requests' : 'Log records'}</h2>
            <span>
              {data
                ? `${mode === 'traces' ? data.traces.length : data.logs.length} shown${search.get('q') || search.has('attr') ? ' · search applied' : ''}`
                : 'Loading…'}
            </span>
          </div>
          {data && renderRecords(data, (id) => setFilter('traceId', id, false), link)}

          {data && (Number(search.get('offset')) > 0 || data.page.truncated) && (
            <div className="flex flex-wrap items-center gap-2.5 border-t border-border px-4 py-3 [&>span]:text-[10px] [&>span]:text-muted">
              {Number(search.get('offset')) > 0 && (
                <Button onClick={() => setFilter('offset', '0')}>Newest</Button>
              )}
              {data.page.nextOffset != null && <Button onClick={showOlder}>Older records</Button>}
              {data.page.truncated && (
                <span>More records available · paging pauses live updates</span>
              )}
            </div>
          )}
        </section>
        <SQLPreview mode={mode} search={search} />
      </section>
    </>
  )
}
