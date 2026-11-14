import test from 'node:test'
import assert from 'node:assert/strict'
import { layoutTrace } from '../src/pages/traces/lib/trace-layout.ts'

const span = (spanId, parentSpanId, start, durationMs) => ({
  spanId,
  parentSpanId,
  timestamp: '2026-09-25T01:00:00.' + start + 'Z',
  durationMs,
  traceId: 'trace',
  service: 'api',
  name: spanId,
  status: 'Ok',
})

test('preserves sub-millisecond starts and nests out-of-order spans', () => {
  const result = layoutTrace([
    span('child', 'root', '000250123', 0.2),
    span('root', '', '000000123', 1),
  ])
  assert.equal(result.items[0].node.span.spanId, 'root')
  assert.equal(result.items[1].node.start, 0.25)
  assert.equal(result.items[1].depth, 1)
  assert.equal(result.duration, 1)
})

test('parallel siblings occupy separate rows and sequential work can share a row', () => {
  const result = layoutTrace([
    span('root', '', '000', 10),
    span('a', 'root', '001', 4),
    span('b', 'root', '002', 2),
    span('c', 'root', '006', 2),
  ])
  const rows = Object.fromEntries(result.items.map((item) => [item.node.span.spanId, item.row]))
  assert.notEqual(rows.a, rows.b)
  assert.equal(rows.a, rows.c)
  assert.ok(rows.a > rows.root)
})

test('cycles, missing parents and zero-length spans remain bounded and visible', () => {
  const result = layoutTrace([
    span('a', 'b', '000', 0),
    span('b', 'a', '000', 0),
    span('orphan', 'missing', '000', 0),
  ])
  assert.equal(result.items.length, 3)
  assert.equal(result.incomplete, 3)
  assert.ok(result.duration > 0)
  assert.equal(layoutTrace([]).items.length, 0)
})

test('zoom keeps descendants and excludes siblings; malformed data is omitted', () => {
  const spans = [
    span('root', '', '000', 10),
    span('a', 'root', '001', 4),
    span('b', 'root', '006', 2),
    span('child', 'a', '002', 1),
  ]
  const result = layoutTrace(spans, 'a')
  assert.deepEqual(
    result.items.map((item) => item.node.span.spanId),
    ['a', 'child'],
  )
  assert.equal(result.start, 1)
  assert.equal(result.duration, 4)
  assert.equal(
    layoutTrace([...spans, { ...spans[0], spanId: 'invalid', timestamp: 'bad' }, spans[0]]).omitted,
    2,
  )
})
