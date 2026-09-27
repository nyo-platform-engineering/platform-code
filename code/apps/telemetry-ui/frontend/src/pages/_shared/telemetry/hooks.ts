import { useCallback, useEffect, useState } from 'react'
import { fetchServices, fetchTelemetry } from './api'
import { filterValues, initialService, type SetFilter } from './filters'
import type { TelemetryData, TelemetryMode } from './types'

import { refreshDelay } from './refresh'

export function useTelemetryFilters() {
  const [search, setSearch] = useState(() => new URLSearchParams(window.location.search))

  useEffect(() => {
    const update = () => setSearch(new URLSearchParams(window.location.search))
    window.addEventListener('popstate', update)
    return () => window.removeEventListener('popstate', update)
  }, [])

  const setFilter = useCallback((key: string, value: string | string[], resetPagination = true) => {
    const values = Array.isArray(value) ? value : [value]
    if (key === 'service' && !values.some(Boolean)) return
    const next = new URLSearchParams(window.location.search)
    next.delete(key)
    for (const item of [...new Set(values)].filter(Boolean)) next.append(key, item)

    if (resetPagination && (key !== 'offset' || value === '0')) {
      next.delete('offset')
      if (!next.has('to') && key !== 'offset' && (next.get('q') || next.has('attr')))
        next.set('to', new Date().toISOString())
    } else if (key === 'offset' && !next.has('to')) {
      next.set('to', new Date().toISOString())
    }

    const query = next.toString()
    window.history.pushState(
      window.history.state,
      '',
      `${window.location.pathname}${query ? `?${query}` : ''}`,
    )
    setSearch(next)
  }, [])

  const setSearchQuery = useCallback((query: string, attributes: string[]) => {
    const next = new URLSearchParams(window.location.search)
    for (const key of ['q', 'attr', 'offset']) next.delete(key)
    if (query) next.set('q', query)
    for (const attribute of attributes) next.append('attr', attribute)
    if (!next.has('to') && (query || attributes.length)) next.set('to', new Date().toISOString())
    window.history.pushState(
      window.history.state,
      '',
      `${window.location.pathname}${next.size ? `?${next}` : ''}`,
    )
    setSearch(next)
  }, [])
  const setTimeWindow = useCallback((minutes: string, to?: string, from?: string) => {
    const next = new URLSearchParams(window.location.search)
    next.set('minutes', minutes)
    next.delete('offset')
    if (from && to) next.set('from', from)
    else next.delete('from')
    if (to) next.set('to', to)
    else next.delete('to')
    window.history.pushState(window.history.state, '', `${window.location.pathname}?${next}`)
    setSearch(next)
  }, [])
  return { search, setFilter, setSearchQuery, setTimeWindow }
}

export function useTelemetryData(
  mode: TelemetryMode,
  search: URLSearchParams,
  paused: boolean,
  setFilter: SetFilter,
  refreshSeconds: number,
) {
  const overviewSearch = new URLSearchParams(search)
  if (mode === 'traces') overviewSearch.delete('traceId')
  const queryKey = `${mode}?${overviewSearch}`
  const [result, setResult] = useState<{ key: string; data: TelemetryData; updated: string }>()
  const [failure, setFailure] = useState<{ key: string; message: string }>()
  const [loading, setLoading] = useState(false)
  const [services, setServices] = useState<string[]>([])
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision((current) => current + 1)

  useEffect(() => {
    const controller = new AbortController()
    const params = new URLSearchParams(queryKey.slice(queryKey.indexOf('?') + 1))
    let timer: ReturnType<typeof setTimeout> | undefined
    let busy = false
    let failures = 0

    async function load() {
      if (busy || controller.signal.aborted || document.hidden) return
      busy = true
      setLoading(true)
      try {
        if (!filterValues(params, 'service').length) {
          const available = await fetchServices(mode, params, controller.signal)
          if (controller.signal.aborted) return
          setServices(available)
          const service = initialService(params, available)
          if (!service)
            throw new Error('No services found in this time range. Choose a wider time range.')
          setFilter('service', service)
          return
        }
        const data = await fetchTelemetry(mode, params, controller.signal)
        if (!controller.signal.aborted) {
          setResult({ key: queryKey, data, updated: new Date().toISOString() })
          setFailure(undefined)
          setServices(data.services)
          failures = 0
        }
      } catch (error) {
        failures += 1
        if (!controller.signal.aborted)
          setFailure({ key: queryKey, message: (error as Error).message })
      } finally {
        busy = false
        if (!controller.signal.aborted) {
          setLoading(false)
          if (!paused) timer = setTimeout(() => void load(), refreshDelay(refreshSeconds, failures))
        }
      }
    }

    const onVisibilityChange = () => {
      clearTimeout(timer)
      if (!document.hidden && !paused) void load()
    }
    document.addEventListener('visibilitychange', onVisibilityChange)
    void load()

    return () => {
      controller.abort()
      clearTimeout(timer)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [mode, queryKey, paused, revision, setFilter, refreshSeconds])

  // Keep charts mounted during refresh/pause, but never show data for old filters.
  return {
    data: result?.key === queryKey ? result.data : undefined,
    updated: result?.key === queryKey ? result.updated : undefined,
    problem: failure?.key === queryKey ? failure.message : undefined,
    loading,
    services,
    refresh,
  }
}
