import test from 'node:test'
import assert from 'node:assert/strict'
import { validateSearch } from '../src/pages/_shared/telemetry/search-validation.ts'

test('search accepts empty or trimmed literal text and rejects short queries', () => {
  for (const value of ['', '  ', 'slow', '  slow  ', "quote_%'"])
    assert.equal(validateSearch(value), true)
  for (const value of ['a', ' ab ']) assert.equal(typeof validateSearch(value), 'string')
})
test('search counts Unicode characters rather than UTF-16 code units', () => {
  assert.equal(validateSearch('😀'.repeat(256)), true)
  assert.equal(typeof validateSearch('😀'.repeat(257)), 'string')
  assert.equal(typeof validateSearch('😀😀'), 'string')
})
test('search rejects control characters inside a query', () => {
  for (const value of ['abc\nxyz', 'abc\tdef', 'abc\u0080def'])
    assert.equal(typeof validateSearch(value), 'string')
})
