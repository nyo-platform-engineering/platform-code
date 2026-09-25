import { useEffect, useState } from 'react'
import { SearchableSelect, type SelectOption } from '../../../components/SearchableSelect'
import { fetchAttributeKeys } from './api'
import type { TelemetryMode } from './types'

export function AttributeKeySelect({
  label,
  value,
  onChange,
  mode,
  scope,
  search,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  mode: TelemetryMode
  scope: string
  search: URLSearchParams
}) {
  const [open, setOpen] = useState(false)
  const [term, setTerm] = useState('')
  const [options, setOptions] = useState<SelectOption[]>([])
  const [status, setStatus] = useState('')
  const searchKey = search.toString()
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    setOptions([])
    setStatus('Loading attribute keys…')
    const timer = setTimeout(() => {
      fetchAttributeKeys(mode, new URLSearchParams(searchKey), scope, term, controller.signal)
        .then((result) => {
          if (controller.signal.aborted) return
          setOptions(result.data.map(({ key }) => ({ value: key, label: key })))
          setStatus(
            result.truncated
              ? 'First 50 keys · type to narrow results'
              : `${result.data.length} keys in this time range · or enter your own`,
          )
        })
        .catch(() => {
          if (!controller.signal.aborted)
            setStatus('Suggestions unavailable · you can still enter a key')
        })
    }, 300)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [open, term, mode, scope, searchKey])
  return (
    <SearchableSelect
      label={label}
      options={options}
      values={value ? [value] : []}
      onChange={(values) => onChange(values[0] ?? '')}
      placeholder="Find an attribute…"
      allowCustom
      onOpenChange={setOpen}
      onSearchChange={setTerm}
      status={status}
    />
  )
}
