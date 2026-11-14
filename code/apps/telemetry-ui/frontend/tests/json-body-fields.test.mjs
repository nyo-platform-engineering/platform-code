import test from 'node:test'
import assert from 'node:assert/strict'
import { discoverBodyFields } from '../src/pages/_shared/telemetry/json-body-fields.ts'

test('discovers nested body fields and zero-based array paths, ignoring plain text', () => {
  assert.deepEqual(
    discoverBodyFields([
      'Request completed',
      '{"order":{"total":41},"items":[{"price":5}],"ok":true}',
    ]),
    ['items', 'items.0', 'items.0.price', 'ok', 'order', 'order.total'],
  )
})

test('does not suggest ambiguous literal dotted or numeric object keys', () => {
  assert.deepEqual(discoverBodyFields(['{"order.total":41,"0":"zero","order":{"total":41}}']), [
    'order',
    'order.total',
  ])
})

test('bounds body size, sample count, and traversal depth', () => {
  assert.deepEqual(discoverBodyFields([...Array(100).fill('{}'), '{"outside":1}']), [])
  assert.deepEqual(discoverBodyFields([JSON.stringify({ huge: 'x'.repeat(100_000) })]), [])
  let nested = 1
  for (let i = 0; i < 10; i++) nested = { child: nested }
  assert.equal(discoverBodyFields([JSON.stringify(nested)]).length, 8)
})
