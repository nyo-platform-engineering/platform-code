// Suggestions are bounded; users can enter paths absent from the sampled logs.
export function discoverBodyFields(bodies: string[]) {
  const fields = new Set<string>()
  let remaining = 10_000
  function visit(value: unknown, parts: string[]) {
    if (--remaining < 0 || parts.length > 8) return
    const path = parts.join('.')
    if (path.length > 128) return
    if (parts.length) fields.add(path)
    if (value === null || typeof value !== 'object') return
    for (const [key, child] of Object.entries(value)) {
      if (remaining <= 0) break
      // The API's dot syntax cannot address literal dots or numeric object keys.
      if (!key || /[.\x00-\x1f\x7f]/.test(key) || (!Array.isArray(value) && /^\d+$/.test(key)))
        continue
      visit(child, [...parts, key])
    }
  }
  for (const body of bodies.slice(0, 100)) {
    if (body.length > 100_000 || remaining <= 0) continue
    try {
      visit(JSON.parse(body), [])
    } catch {
      /* Plain text is not JSON. */
    }
  }
  return [...fields].sort()
}
