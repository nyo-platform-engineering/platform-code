export type TimeWindow = { from: string; to: string }

export function validateWindow(from: string, to: string): TimeWindow {
  const start = Date.parse(from)
  const end = Date.parse(to)
  if (!Number.isFinite(start) || !Number.isFinite(end)) {
    throw new Error('Enter valid From and To times.')
  }
  if (end <= start) throw new Error('To must be after From.')
  if (end - start > 24 * 60 * 60_000) throw new Error('Select a window of at most 24 hours.')
  return { from: new Date(start).toISOString(), to: new Date(end).toISOString() }
}

export function shiftWindow(window: TimeWindow, direction: -1 | 1): TimeWindow {
  const { from, to } = validateWindow(window.from, window.to)
  const start = Date.parse(from)
  const end = Date.parse(to)
  const shift = ((end - start) / 2) * direction
  return {
    from: new Date(start + shift).toISOString(),
    to: new Date(end + shift).toISOString(),
  }
}
