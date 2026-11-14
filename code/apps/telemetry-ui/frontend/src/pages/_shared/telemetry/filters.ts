// Empty service filters are unresolved: discover and select a service before querying.
export function filterValues(search: URLSearchParams, key: string): string[] {
  const values = search.getAll(key)
  return [...new Set(values.filter(Boolean))]
}

export type SetFilter = (key: string, value: string | string[]) => void

export function initialService(search: URLSearchParams, available: string[]): string | undefined {
  return filterValues(search, 'service')[0] ?? available.filter(Boolean).sort()[0]
}
