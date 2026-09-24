import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '../App'

const severityRows = [
  { label: 'Fatal', tone: 'fatal', width: '18%' },
  { label: 'Error', tone: 'error', width: '34%' },
  { label: 'Warn', tone: 'warn', width: '47%' },
  { label: 'Info', tone: 'info', width: '76%' },
  { label: 'Debug', tone: 'debug', width: '42%' },
]

export const Route = createFileRoute('/logs')({
  component: LogsPage,
})

function LogsPage() {
  return (
    <>
      <PageHeader eyebrow="Logs" title="Volume by severity" capabilityId="log-volume" />
      <section className="section-block">
        <div className="panel severity-panel">
          <div className="panel-title"><strong>Severity bands</strong><span>1 minute buckets</span></div>
          <div className="severity-list" aria-label="Log severity visualization scaffold">
            {severityRows.map((row) => (
              <div className="severity-row" key={row.label}>
                <span>{row.label}</span>
                <div className="severity-track"><i className={row.tone} style={{ width: row.width }} /></div>
                <b>—</b>
              </div>
            ))}
          </div>
        </div>
      </section>
    </>
  )
}
