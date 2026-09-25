import { useCallback, useEffect, useState } from 'react'
import { fetchTelemetry } from './api'
import type { TelemetryData, TelemetryMode } from './types'

const REFRESH_INTERVAL_MS = 2_000

export function useTelemetryFilters() {
  const [search, setSearch] = useState(() => new URLSearchParams(window.location.search))

  useEffect(() => {
    const update = () => setSearch(new URLSearchParams(window.location.search))
    window.addEventListener('popstate', update)
    return () => window.removeEventListener('popstate', update)
  }, [])

  const setFilter = useCallback((key: string, value: string | string[], resetPagination = true) => {
    const next = new URLSearchParams(window.location.search)
    next.delete(key)
    const values = Array.isArray(value) ? value : [value]
    for (const item of [...new Set(values)].filter(Boolean)) next.append(key, item)
    if (key === 'service' && !next.has(key)) next.set(key, '')

    if (resetPagination && (key !== 'offset' || value === '0')) {
      next.delete('offset')
      next.delete('to')
      if (key !== 'offset' && (next.get('q') || next.has('attr')))
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
    for (const key of ['q', 'attr', 'offset', 'to']) next.delete(key)
    if (query) next.set('q', query)
    for (const attribute of attributes) next.append('attr', attribute)
    if (query || attributes.length) next.set('to', new Date().toISOString())
    window.history.pushState(
      window.history.state,
      '',
      `${window.location.pathname}${next.size ? `?${next}` : ''}`,
    )
    setSearch(next)
  }, [])
  return { search, setFilter, setSearchQuery }
}

export function useTelemetryData(mode: TelemetryMode, search: URLSearchParams, paused: boolean) {
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
          if (!paused)
            timer = setTimeout(
              () => void load(),
              Math.min(30_000, REFRESH_INTERVAL_MS * 2 ** Math.min(failures, 4)),
            )
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
  }, [mode, queryKey, paused, revision])

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
