import test from 'node:test'
import assert from 'node:assert/strict'
import {
  refreshInterval,
  refreshDelay,
  refreshIntervals,
} from '../src/pages/_shared/telemetry/refresh.ts'

test('live refresh supports only intervals from five seconds to five minutes', () => {
  assert.deepEqual(refreshIntervals, [5, 10, 15, 30, 60, 120, 300])
  for (const seconds of refreshIntervals) assert.equal(refreshDelay(seconds, 0), seconds * 1000)
  for (const invalid of [0, 1, 2, 4, 301, NaN]) assert.equal(refreshInterval(invalid), 5)
})

test('error backoff never speeds up a selected interval or exceeds five minutes', () => {
  assert.equal(refreshDelay(5, 1), 10_000)
  assert.equal(refreshDelay(120, 1), 240_000)
  assert.equal(refreshDelay(300, 0), 300_000)
  assert.equal(refreshDelay(300, 4), 300_000)
})
