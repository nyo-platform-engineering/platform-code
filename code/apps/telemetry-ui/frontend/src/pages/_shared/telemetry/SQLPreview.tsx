import { useEffect, useState } from 'react'
import { LuCode, LuCopy } from 'react-icons/lu'
import { Button } from '../../../components/Button'
import { fetchSQLPreview } from './api'
import type { TelemetryMode } from './types'

export function SQLPreview({ mode, search }: { mode: TelemetryMode; search: URLSearchParams }) {
  const [open, setOpen] = useState(false)
  const [result, setResult] = useState<Awaited<ReturnType<typeof fetchSQLPreview>>>()
  const [problem, setProblem] = useState('')
  const [copied, setCopied] = useState('')
  const [revision, setRevision] = useState(0)
  const searchKey = search.toString()
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    setResult(undefined)
    setProblem('')
    setCopied('')
    fetchSQLPreview(mode, new URLSearchParams(searchKey), controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setResult(value)
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setProblem(error instanceof Error ? error.message : 'SQL preview unavailable')
      })
    return () => controller.abort()
  }, [open, mode, searchKey, revision])
  async function copy(name: string, sql: string) {
    try {
      await navigator.clipboard.writeText(sql)
      setCopied(`Copied ${name}`)
    } catch {
      setCopied('Copy unavailable. Select and copy the SQL below.')
    }
  }
  return (
    <div className="min-w-0 border-t border-border pt-3">
      <Button
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="inline-flex items-center gap-1.5"
      >
        <LuCode aria-hidden />
        {open ? 'Hide SQL preview' : 'Preview SQL'}
      </Button>
      {open && (
        <section aria-label="SQL preview" className="mt-3 grid min-w-0 gap-3">
          <p className="text-[11px] leading-relaxed text-muted">
            Applied filters only. Preview does not execute SQL. Parameters correspond to each ? in
            order; timestamps retain exact nanoseconds. Unsaved search edits are not included.
          </p>
          <Button onClick={() => setRevision((value) => value + 1)} className="justify-self-start">
            Refresh preview
          </Button>
          {problem ? (
            <p role="alert" className="text-xs text-danger">
              {problem}
            </p>
          ) : !result ? (
            <p role="status" className="text-xs text-muted">
              Loading SQL…
            </p>
          ) : (
            <>
              <p className="font-mono text-[10px] break-all text-muted">
                {result.from} → {result.to}
              </p>
              {result.queries.map((query) => (
                <details key={query.name} open className="min-w-0 rounded border border-border">
                  <summary className="cursor-pointer p-2 text-xs font-semibold">
                    {query.name}
                  </summary>
                  <div className="grid min-w-0 gap-2 border-t border-border p-2">
                    <Button
                      className="inline-flex items-center gap-1.5 justify-self-start"
                      onClick={() => copy(query.name, query.sql)}
                    >
                      <LuCopy aria-hidden />
                      Copy {query.name} SQL
                    </Button>
                    <pre
                      tabIndex={0}
                      aria-label={`${query.name} SQL`}
                      className="max-h-72 overflow-auto rounded bg-canvas p-3 font-mono text-[11px] leading-relaxed"
                    >
                      {query.sql.replace(
                        / (FROM|PREWHERE|WHERE|GROUP BY|ORDER BY|LIMIT|SETTINGS) /g,
                        '\n$1 ',
                      )}
                    </pre>
                    <details>
                      <summary className="cursor-pointer text-[11px] text-muted">
                        Bound parameters ({query.parameters.length})
                      </summary>
                      <ol className="mt-2 max-h-48 overflow-auto font-mono text-[10px]">
                        {query.parameters.map((param) => (
                          <li
                            key={param.position}
                            className="border-t border-border py-1 break-all"
                          >
                            {param.position}. [{param.type}] {JSON.stringify(param.value)}
                          </li>
                        ))}
                      </ol>
                      <Button
                        className="mt-2"
                        onClick={() =>
                          copy(
                            `${query.name} parameters`,
                            JSON.stringify(query.parameters, null, 2),
                          )
                        }
                      >
                        Copy parameters
                      </Button>
                    </details>
                  </div>
                </details>
              ))}
            </>
          )}
          <p role="status" className="text-[11px] text-muted">
            {copied}
          </p>
        </section>
      )}
    </div>
  )
}
