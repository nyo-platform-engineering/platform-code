import test from 'node:test'
import assert from 'node:assert/strict'
import { filterValues, initialService } from '../src/pages/_shared/telemetry/filters.ts'

test('missing or empty service selection resolves to one available service', () => {
  const available = ['payments', 'api-gateway', 'checkout']
  assert.equal(initialService(new URLSearchParams(), available), 'api-gateway')
  assert.equal(initialService(new URLSearchParams('service='), available), 'api-gateway')
  assert.deepEqual(available, ['payments', 'api-gateway', 'checkout'])
})

test('no available services leaves selection unresolved instead of selecting all', () => {
  assert.equal(initialService(new URLSearchParams(), []), undefined)
})

test('explicit service filters are preserved and deduplicated', () => {
  const search = new URLSearchParams('service=checkout&service=checkout&service=payments')
  assert.deepEqual(filterValues(search, 'service'), ['checkout', 'payments'])
  assert.equal(initialService(search, ['api-gateway']), 'checkout')
})
