import test from 'node:test'
import assert from 'node:assert/strict'
import { inputInstant, offsetLabel, zonedInput } from '../src/pages/_shared/telemetry/time-zone.ts'

test('Jakarta edits round trip to UTC without changing the instant', () => {
  const instant = '2026-09-27T18:30:00.000Z'
  assert.equal(zonedInput(instant, 420), '2026-09-28T01:30:00')
  assert.equal(inputInstant('2026-09-28T01:30:00', 420), instant)
  assert.equal(offsetLabel(420), 'UTC+07:00')
})

test('negative and fractional offsets preserve exact time', () => {
  const instant = '2026-09-27T01:00:00.000Z'
  for (const offset of [-480, 0, 330, 345]) {
    assert.equal(inputInstant(zonedInput(instant, offset), offset), instant)
  }
  assert.equal(offsetLabel(-480), 'UTC−08:00')
  assert.equal(offsetLabel(330), 'UTC+05:30')
  assert.throws(() => inputInstant('', 420))
})

test('the editor displays seconds without fractional seconds', () => {
  assert.equal(zonedInput('2026-09-27T12:34:56.789Z', 420), '2026-09-27T19:34:56')
})
