export type AttributeCondition = {
  scope: 'resource' | 'span' | 'log' | 'body'
  path?: string
  key: string
  op: 'eq' | 'neq' | 'contains' | 'exists' | 'missing' | 'gt' | 'gte' | 'lt' | 'lte'
  value: string
}
export const ATTRIBUTE_OPERATORS = [
  { value: 'eq', label: 'Equals' },
  { value: 'neq', label: 'Not equal' },
  { value: 'contains', label: 'Contains' },
  { value: 'exists', label: 'Exists' },
  { value: 'missing', label: 'Missing' },
  { value: 'gt', label: '> Number' },
  { value: 'gte', label: '≥ Number' },
  { value: 'lt', label: '< Number' },
  { value: 'lte', label: '≤ Number' },
]
export const needsValue = (op: string) => op !== 'exists' && op !== 'missing'
export function validateAttributeKey(value: string) {
  return (
    (value.length > 0 &&
      [...value].length <= 128 &&
      !/[\u0000-\u001f\u007f-\u009f]/u.test(value)) ||
    'Enter an attribute key (1–128 characters, no controls).'
  )
}
export function validateAttributeValue(value: string, op: string) {
  if (!needsValue(op)) return true
  if ([...value].length > 256 || /[\u0000-\u001f\u007f-\u009f]/u.test(value))
    return 'Use at most 256 characters without controls.'
  if (op === 'contains' && !value) return 'Contains requires a value.'
  if (
    ['gt', 'gte', 'lt', 'lte'].includes(op) &&
    (!/^[+-]?(?:\d+\.?\d*|\.\d+)(?:e[+-]?\d+)?$/i.test(value) || !Number.isFinite(Number(value)))
  )
    return 'Enter a finite number.'
  return true
}
export function readAttributes(values: string[]): {
  conditions: AttributeCondition[]
  error?: string
} {
  try {
    if (values.length > 8) throw new Error()
    const conditions = values.map((value) => {
      const item = JSON.parse(value)
      if (
        !item ||
        !['resource', 'span', 'log', 'body'].includes(item.scope) ||
        !ATTRIBUTE_OPERATORS.some((op) => op.value === item.op) ||
        typeof item.key !== 'string' ||
        (item.path !== undefined && typeof item.path !== 'string') ||
        (item.value !== undefined && typeof item.value !== 'string') ||
        Object.keys(item).some((key) => !['scope', 'key', 'op', 'value', 'path'].includes(key))
      )
        throw new Error()
      return { ...item, value: item.value ?? '' } as AttributeCondition
    })
    return { conditions }
  } catch {
    return {
      conditions: [],
      error: 'Invalid attribute filters in this URL. Clear them before searching.',
    }
  }
}
export function encodeAttributes(conditions: AttributeCondition[]) {
  return conditions.map(({ scope, key, op, value, path }) =>
    JSON.stringify({
      scope,
      key,
      op,
      ...(path && scope !== 'body' ? { path } : {}),
      ...(needsValue(op) ? { value } : {}),
    }),
  )
}

export function validateJSONPath(value: string) {
  if (!value) return true
  const parts = value.split('.')
  return (
    ([...value].length <= 128 &&
      parts.length <= 8 &&
      parts.every(
        (part) =>
          part.length > 0 &&
          !/[\u0000-\u001f\u007f-\u009f]/u.test(part) &&
          (!/^\d+$/.test(part) || Number(part) <= 1000000),
      )) ||
    'Use up to 8 dot-separated segments; array indexes start at 0.'
  )
}
