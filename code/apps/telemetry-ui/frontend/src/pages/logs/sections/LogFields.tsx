import { useState } from 'react'

export type LogField = {
  id: string
  key: string
  label: string
  scope: 'record' | 'log' | 'resource'
}
export const DEFAULT_FIELDS = ['body', 'timestamp', 'severity', 'service']

export function LogFields({
  fields,
  visible,
  onChange,
}: {
  fields: LogField[]
  visible: string[]
  onChange: (fields: string[]) => void
}) {
  const [query, setQuery] = useState('')
  const matches = fields.filter((field) =>
    `${field.scope} ${field.label}`.toLowerCase().includes(query.toLowerCase()),
  )
  return (
    <aside
      aria-label="Displayed log fields"
      className="min-w-0 space-y-3 bg-surface p-3 md:sticky md:top-3"
    >
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-xs font-semibold">Display fields</h3>
        <button
          type="button"
          onClick={() => onChange(DEFAULT_FIELDS)}
          className="text-[11px] text-accent-strong hover:underline"
        >
          Reset
        </button>
      </div>
      <input
        type="search"
        aria-label="Find display field"
        placeholder="Find a field…"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        className="w-full min-w-0 rounded border border-border bg-surface-raised px-2 py-1.5 text-xs outline-accent"
      />
      <p className="text-[10px] text-muted">
        Choose what each record shows. Attributes from these results.
      </p>
      <div className="max-h-48 space-y-3 overflow-y-auto md:max-h-[65vh]">
        {(['record', 'log', 'resource'] as const).map((scope) => {
          const group = matches.filter((field) => field.scope === scope)
          if (!group.length) return null
          return (
            <fieldset key={scope} className="space-y-1">
              <legend className="mb-1 text-[10px] font-semibold text-muted uppercase">
                {scope === 'record' ? 'Record fields' : `${scope} attributes`}
              </legend>
              {group.map((field) => (
                <label
                  key={field.id}
                  className="flex cursor-pointer items-start gap-2 rounded px-1 py-1 text-xs transition-colors focus-within:bg-accent-soft hover:bg-accent-soft hover:text-accent-strong motion-reduce:transition-none"
                >
                  <input
                    type="checkbox"
                    checked={visible.includes(field.id)}
                    onChange={(event) =>
                      onChange(
                        event.target.checked
                          ? [...visible, field.id]
                          : visible.filter((id) => id !== field.id),
                      )
                    }
                    className="mt-0.5 shrink-0 accent-accent"
                  />
                  <span className="min-w-0 wrap-anywhere">{field.label}</span>
                </label>
              ))}
            </fieldset>
          )
        })}
        {!matches.length && <p className="text-xs text-muted">No matching fields.</p>}
      </div>
    </aside>
  )
}
