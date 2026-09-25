// An explicit empty service means all services; an absent service defaults to the demo.
export function filterValues(search: URLSearchParams, key: string): string[] {
  const values = search.getAll(key)
  return [...new Set(values.length ? values.filter(Boolean) : key === 'service' ? ['go-demo'] : [])]
}

export type SetFilter = (key: string, value: string | string[]) => void
