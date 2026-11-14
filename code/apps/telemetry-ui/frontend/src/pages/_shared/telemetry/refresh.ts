export const refreshIntervals = [5, 10, 15, 30, 60, 120, 300] as const

export function refreshInterval(value: number) {
  return refreshIntervals.some((seconds) => seconds === value) ? value : 5
}

export function refreshLabel(seconds: number) {
  return seconds < 60 ? `${seconds}s` : `${seconds / 60}m`
}

export function refreshDelay(seconds: number, failures: number) {
  return Math.min(300_000, refreshInterval(seconds) * 1_000 * 2 ** Math.min(failures, 4))
}

export function preferredRefreshInterval() {
  try {
    return refreshInterval(Number(localStorage.getItem('signal-deck-refresh-seconds')))
  } catch {
    return 5
  }
}
