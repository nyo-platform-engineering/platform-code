export function offsetLabel(minutes: number) {
  const hours = Math.floor(Math.abs(minutes) / 60)
  const remainder = Math.abs(minutes) % 60
  return `UTC${minutes >= 0 ? '+' : '−'}${String(hours).padStart(2, '0')}:${String(remainder).padStart(2, '0')}`
}

export function zonedInput(instant: string, offset: number) {
  return new Date(Date.parse(instant) + offset * 60_000).toISOString().slice(0, 19)
}

export function inputInstant(value: string, offset: number) {
  const time = Date.parse(`${value}Z`)
  if (!Number.isFinite(time)) throw new Error('Enter valid Start and End times.')
  return new Date(time - offset * 60_000).toISOString()
}

// These choices are fixed UTC offsets; Jakarta, Singapore and Tokyo do not use DST.
export const timeZones = Array.from({ length: 27 }, (_, index) => (index - 12) * 60)
  .concat(330, 345)
  .sort((a, b) => a - b)
  .map((offset) => {
    const city = new Map([
      [420, 'Jakarta / WIB'],
      [480, 'Singapore'],
      [540, 'Tokyo'],
      [330, 'India'],
      [345, 'Nepal'],
    ]).get(offset)
    return { offset, label: city ? `${city} · ${offsetLabel(offset)}` : offsetLabel(offset) }
  })

export function preferredOffset() {
  try {
    const saved = localStorage.getItem('signal-deck-time-offset')
    if (saved !== null && timeZones.some((zone) => zone.offset === Number(saved)))
      return Number(saved)
  } catch {
    /* Storage may be unavailable. */
  }
  return Intl.DateTimeFormat().resolvedOptions().timeZone === 'Asia/Jakarta' ? 420 : 0
}
