import test from 'node:test'
import assert from 'node:assert/strict'
import { shiftWindow, validateWindow } from '../src/pages/_shared/telemetry/time-window.ts'
import { windowMinutes } from '../src/pages/_shared/telemetry/types.ts'

test('a one-hour window shifts exactly thirty minutes in either direction', () => {
  const window = { from: '2026-09-27T12:00:00.000Z', to: '2026-09-27T13:00:00.000Z' }
  const previous = shiftWindow(window, -1)
  assert.deepEqual(previous, { from: '2026-09-27T11:30:00.000Z', to: '2026-09-27T12:30:00.000Z' })
  assert.deepEqual(shiftWindow(previous, 1), window)
})

test('custom windows retain their exact duration across midnight', () => {
  const window = { from: '2026-09-27T23:50:00.000Z', to: '2026-09-28T00:05:00.000Z' }
  assert.deepEqual(shiftWindow(window, 1), {
    from: '2026-09-27T23:57:30.000Z',
    to: '2026-09-28T00:12:30.000Z',
  })
  assert.equal(windowMinutes(new URLSearchParams({ ...window, minutes: '60' })), 15)
})

test('invalid, reversed, empty and over-24-hour windows are rejected', () => {
  const from = '2026-09-27T00:00:00Z'
  for (const to of ['invalid', from, '2026-09-26T23:59:00Z', '2026-09-28T00:00:01Z']) {
    assert.throws(() => validateWindow(from, to))
  }
  assert.equal(validateWindow(from, '2026-09-28T00:00:00Z').to, '2026-09-28T00:00:00.000Z')
})
