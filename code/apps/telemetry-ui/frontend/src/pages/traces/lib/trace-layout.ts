import type { SpanRecord } from '../../_shared/telemetry/types'

export type TraceNode = {
  span: SpanRecord
  start: number
  end: number
  parent?: TraceNode
  children: TraceNode[]
  incomplete: boolean
}
export type TraceLayout = ReturnType<typeof layoutTrace>

// Date.parse discards sub-millisecond precision, which matters for short spans.
function timestampNs(value: string): bigint | undefined {
  const milliseconds = Date.parse(value)
  if (!Number.isFinite(milliseconds)) return undefined
  const fraction = value.match(/\.(\d+)(?:Z|[+-]\d{2}:?\d{2})$/)?.[1] ?? ''
  return BigInt(milliseconds) * 1_000_000n + BigInt(fraction.padEnd(9, '0').slice(3, 9))
}

export function layoutTrace(spans: SpanRecord[], zoomId?: string) {
  const valid = spans.flatMap((span) => {
    const time = timestampNs(span.timestamp)
    return time !== undefined && Number.isFinite(span.durationMs) && span.durationMs >= 0
      ? [{ span, time }]
      : []
  })
  const origin = valid.reduce(
    (min, item) => (item.time < min ? item.time : min),
    valid[0]?.time ?? 0n,
  )
  const nodes = new Map<string, TraceNode>()
  for (const { span, time } of valid) {
    if (nodes.has(span.spanId)) continue
    const start = Number(time - origin) / 1e6
    nodes.set(span.spanId, {
      span,
      start,
      end: start + span.durationMs,
      children: [],
      incomplete: false,
    })
  }
  for (const node of nodes.values()) {
    const parentId = node.span.parentSpanId
    if (!parentId || /^0+$/.test(parentId)) continue
    const parent = nodes.get(parentId)
    // Malformed cycles must not hang the UI or make spans disappear.
    const seen = new Set([node.span.spanId])
    let cursor = parent
    while (cursor && !seen.has(cursor.span.spanId)) {
      seen.add(cursor.span.spanId)
      cursor = nodes.get(cursor.span.parentSpanId ?? '')
    }
    if (!parent || cursor) node.incomplete = true
    else {
      node.parent = parent
      parent.children.push(node)
    }
  }
  const compare = (a: TraceNode, b: TraceNode) =>
    a.start - b.start || b.end - a.end || a.span.spanId.localeCompare(b.span.spanId)
  const roots =
    zoomId && nodes.has(zoomId)
      ? [nodes.get(zoomId)!]
      : [...nodes.values()].filter((node) => !node.parent).sort(compare)
  const ordered: { node: TraceNode; depth: number; row: number }[] = []
  const lanes: TraceNode[][] = []
  function visit(node: TraceNode, depth: number, minimumRow: number) {
    let row = minimumRow
    while (lanes[row]?.some((other) => node.start <= other.end && node.end >= other.start)) row++
    ;(lanes[row] ??= []).push(node)
    ordered.push({ node, depth, row })
    for (const child of node.children.sort(compare)) visit(child, depth + 1, row + 1)
  }
  roots.forEach((root) => visit(root, 0, 0))
  const start = Math.min(...ordered.map((item) => item.node.start), Infinity)
  const end = Math.max(...ordered.map((item) => item.node.end), -Infinity)
  return {
    items: ordered,
    start: Number.isFinite(start) ? start : 0,
    duration: Math.max(0.001, end - start),
    rows: lanes.length,
    omitted: spans.length - nodes.size,
    incomplete: [...nodes.values()].filter((node) => node.incomplete).length,
  }
}
