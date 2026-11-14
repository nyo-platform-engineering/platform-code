export function validateSearch(value: string): true | string {
  const trimmed = value.trim()
  const size = [...trimmed].length
  return size === 0 || (size >= 3 && size <= 256 && !/[\u0000-\u001f\u007f-\u009f]/u.test(trimmed))
    ? true
    : 'Use 3–256 characters without control characters.'
}
