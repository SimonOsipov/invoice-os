import { REDRIVE_ICON } from '../icons'
import { jobStateStyle, type JobDetail } from '../jobsApi'
import { syncStamp } from '../rulesApi'
import { Drawer, MetaGrid } from './Drawer'
import { Badge } from './StatusBadge'
import { Note } from './Tenants'

type Props = {
  detail: JobDetail | null
  /** Why there is no detail: still reading, or the read failed. */
  note: { text: string; onRetry?: () => void }
  busy: boolean
  onRedrive: () => void
  onCancel: () => void
  onClose: () => void
}

export const NRS_LINE = 'The Access Point acknowledged this submission. Confirm with NRS before re-sending.'

const RED_BTN = { border: '1px solid var(--status-red-border)', background: 'var(--status-red-bg)', height: 38, padding: '0 14px', borderRadius: 'var(--radius-btn)', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)', display: 'inline-flex', alignItems: 'center', gap: 7 } as const
const LOCKED = { opacity: 0.45, cursor: 'not-allowed' } as const

export function JobDrawer({ detail, note, busy, onRedrive, onCancel, onClose }: Props) {
  const job = detail?.job
  const redrivable = job?.state === 'dead_lettered' && !job.acknowledged
  const cancellable = job?.state === 'queued'
  return (
    <Drawer
      width={560}
      onClose={onClose}
      header={
        job ? (
          <>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4 }}>
              <span className="mono" style={{ fontSize: 14, fontWeight: 700, overflowWrap: 'anywhere' }}>
                {job.invoice_number}
              </span>
              <Badge style={jobStateStyle(job.state)} fontSize={9.5} />
            </div>
            <div style={{ fontSize: 12.5, color: 'var(--fg-2)', overflowWrap: 'anywhere' }}>{job.tenant_name}</div>
          </>
        ) : (
          <span style={{ fontSize: 14, fontWeight: 700 }}>Job</span>
        )
      }
      footer={
        redrivable ? (
          <>
            <div style={{ flex: 1 }} />
            <button type="button" onClick={onRedrive} disabled={busy} className="ops-btn v2-btn v2-btn-primary" style={{ height: 38, ...(busy ? LOCKED : {}) }}>
              {REDRIVE_ICON} Re-drive
            </button>
          </>
        ) : cancellable ? (
          <>
            <div style={{ flex: 1 }} />
            <button type="button" onClick={onCancel} disabled={busy} className="ops-btn" style={{ ...RED_BTN, ...(busy ? LOCKED : { cursor: 'pointer' }) }}>
              Cancel job
            </button>
          </>
        ) : undefined
      }
    >
      {detail && job ? (
        <>
          <MetaGrid
            items={[
              { label: 'Tenant', value: job.tenant_name, mono: false },
              { label: 'Invoice', value: job.invoice_number },
              { label: 'Invoice status', value: job.invoice_status },
              { label: 'Attempts', value: `${job.attempts} / ${job.max_attempts}` },
              { label: 'Created', value: syncStamp(job.created_at) },
              { label: 'Updated', value: syncStamp(job.updated_at) },
            ]}
          />
          {job.state === 'dead_lettered' && job.acknowledged && (
            <div style={{ border: '1px solid var(--status-amber-border)', background: 'var(--status-amber-bg)', color: 'var(--status-amber-text)', borderRadius: 'var(--radius-md)', padding: '11px 14px', fontSize: 12.5, marginBottom: 22 }}>{NRS_LINE}</div>
          )}
          <div className="label" style={{ marginBottom: 8 }}>
            Last error
          </div>
          <pre className="ops-json" style={{ marginBottom: 22, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
            {job.last_error ?? '—'}
          </pre>
          <div className="label" style={{ marginBottom: 12 }}>
            Invoice timeline
          </div>
          <ol aria-label="Invoice timeline" style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column' }}>
            {detail.history.map((h, i) => (
              <li key={i} style={{ display: 'grid', gridTemplateColumns: '18px 1fr', gap: 12 }}>
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
                  <span style={{ width: 11, height: 11, borderRadius: '50%', background: 'var(--action)' }} />
                  {i < detail.history.length - 1 && <span style={{ flex: 1, width: 2, background: 'var(--action)' }} />}
                </div>
                <div style={{ paddingBottom: 16 }}>
                  <div style={{ fontSize: 13, fontWeight: 600 }}>
                    {h.from_status ? `${h.from_status} → ${h.to_status}` : h.to_status}
                  </div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                    {h.actor} · {syncStamp(h.changed_at)}
                  </div>
                </div>
              </li>
            ))}
          </ol>
        </>
      ) : (
        <Note text={note.text} onRetry={note.onRetry} />
      )}
    </Drawer>
  )
}
