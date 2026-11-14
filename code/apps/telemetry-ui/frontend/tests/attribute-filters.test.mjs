import test from 'node:test'
import assert from 'node:assert/strict'
import {
  encodeAttributes,
  validateJSONPath,
  readAttributes,
  validateAttributeKey,
  validateAttributeValue,
} from '../src/pages/_shared/telemetry/attribute-filters.ts'

test('attribute URLs round trip literal keys and values and omit unused values', () => {
  const conditions = [
    { scope: 'resource', key: "key.%'", op: 'eq', value: '' },
    { scope: 'span', key: 'status', op: 'exists', value: 'stale' },
  ]
  const encoded = encodeAttributes(conditions)
  assert.equal(JSON.parse(encoded[1]).value, undefined)
  assert.deepEqual(readAttributes(encoded), {
    conditions: [conditions[0], { ...conditions[1], value: '' }],
  })
})
test('malformed attribute URLs fail visibly instead of silently broadening queries', () => {
  const valid = { scope: 'log', key: 'event.name', op: 'eq', value: 'checkout' }
  for (const item of [
    null,
    [],
    { ...valid, scope: 'sql' },
    { ...valid, op: 'execute' },
    { ...valid, extra: true },
    { ...valid, value: 42 },
  ]) {
    assert.ok(readAttributes([JSON.stringify(item)]).error)
  }
  assert.ok(readAttributes(['{']).error)
  assert.ok(readAttributes(Array(9).fill(JSON.stringify(valid))).error)
  assert.equal(readAttributes(Array(8).fill(JSON.stringify(valid))).error, undefined)
})
test('attribute input validation distinguishes empty, numeric, and existence operators', () => {
  assert.equal(validateAttributeValue('', 'eq'), true)
  assert.equal(typeof validateAttributeValue('', 'contains'), 'string')
  assert.equal(validateAttributeValue('', 'exists'), true)
  for (const value of ['500', '-.5', '1e3'])
    assert.equal(validateAttributeValue(value, 'gte'), true)
  for (const value of ['', 'NaN', 'Infinity', '1e999', '0x10', '500ms'])
    assert.equal(typeof validateAttributeValue(value, 'gte'), 'string')
  assert.equal(validateAttributeKey('😀'.repeat(128)), true)
  for (const value of ['', '😀'.repeat(129), 'bad\nkey'])
    assert.equal(typeof validateAttributeKey(value), 'string')
  assert.equal(validateAttributeValue('😀'.repeat(256), 'eq'), true)
  assert.equal(typeof validateAttributeValue('😀'.repeat(257), 'eq'), 'string')
})

test('JSON paths survive URLs and validate bounded traversal', () => {
  const fields = [
    { scope: 'body', key: 'items.0.price', op: 'gt', value: '10' },
    { scope: 'log', key: 'payload', path: 'user.id', op: 'eq', value: '42' },
  ]
  assert.deepEqual(readAttributes(encodeAttributes(fields)).conditions, fields)
  for (const path of ['', 'user.id', 'items.0.price']) assert.equal(validateJSONPath(path), true)
  for (const path of ['a..b', 'items.1000001', 'a.b.c.d.e.f.g.h.i'])
    assert.equal(typeof validateJSONPath(path), 'string')
})
