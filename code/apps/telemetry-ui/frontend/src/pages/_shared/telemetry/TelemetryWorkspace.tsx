import { Button } from '../../../components/Button'
import { SQLPreview } from './SQLPreview'
import { TelemetrySearch } from './TelemetrySearch'
import { filterValues } from './filters'
import { useEffect, useState, type ReactNode } from 'react'
import { PageHeader } from '../../../layouts/PageHeader'
import { useMetadata } from '../../../layouts/MetadataProvider'
import { useTelemetryData, useTelemetryFilters } from './hooks'
import { preferredRefreshInterval, refreshInterval } from './refresh'
import { TimeControls } from './TimeControls'
import { QueryControls } from './QueryControls'
import { TelemetryChart } from './TelemetryChart'
import { windowMinutes, type TelemetryMode, type TraceLink, type TelemetryData } from './types'

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
  const { search, setFilter, setSearchQuery, setTimeWindow } = useTelemetryFilters()
  const { dataSources } = useMetadata()
  const sourceKey = mode === 'traces' ? 'traceSource' : 'logSource'
  const sourceOptions = dataSources[mode]
  const selectedSource = search.get(sourceKey) ?? ''
  const sourceReady = sourceOptions.some((source) => source.id === selectedSource)

  useEffect(() => {
    for (const [key, options] of [
      ['traceSource', dataSources.traces],
      ['logSource', dataSources.logs],
    ] as const) {
      const selected = new URLSearchParams(window.location.search).get(key)
      if (options.length && !options.some((source) => source.id === selected))
        setFilter(key, options[0].id)
    }
  }, [dataSources, setFilter])
  const [refreshSeconds, setRefreshSeconds] = useState(preferredRefreshInterval)
  const [paused, setPaused] = useState(
    () =>
      search.has('to') ||
      Number(search.get('offset')) > 0 ||
      Boolean(search.get('q')) ||
      search.has('attr'),
  )
  const { data, updated, problem, loading, services, refresh } = useTelemetryData(
    mode,
    search,
    paused,
    setFilter,
    refreshSeconds,
    sourceReady,
  )
  const [showCharts, setShowCharts] = useState(false)
  const traceId = search.get('traceId') ?? ''
  const link: TraceLink = (id, target) => {
    const params = new URLSearchParams({ traceId: id, minutes: String(windowMinutes(search)) })
    for (const key of ['traceSource', 'logSource']) {
      const value = search.get(key)
      if (value) params.set(key, value)
    }
    const services = filterValues(search, 'service')
    for (const service of services) params.append('service', service)
    for (const key of ['from', 'to']) {
      const value = search.get(key)
      if (value) params.set(key, value)
    }
    return `/${target}?${params}`
  }

  function toggleLive() {
    setTimeWindow(
      String(windowMinutes(search)),
      paused ? undefined : (data?.to ?? new Date().toISOString()),
    )
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
          <TimeControls
            minutes={windowMinutes(search)}
            paused={paused}
            refreshSeconds={refreshSeconds}
            onIntervalChange={(seconds) => {
              const interval = refreshInterval(seconds)
              setRefreshSeconds(interval)
              try {
                localStorage.setItem('signal-deck-refresh-seconds', String(interval))
              } catch {
                /* Optional preference. */
              }
            }}
            loading={loading}
            problem={problem}
            updated={updated}
            from={data?.from}
            to={data?.to}
            onRangeChange={(minutes) => {
              setPaused(false)
              setTimeWindow(minutes)
            }}
            onWindowChange={({ from, to }) => {
              setPaused(true)
              setTimeWindow(String((Date.parse(to) - Date.parse(from)) / 60_000), to, from)
            }}
            onToggleLive={toggleLive}
            onRefresh={refresh}
          />
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
                  dataSources={sourceOptions}
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
