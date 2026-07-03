import { CLOSE_ICON, COPY_ICON, EXPORT_ICON, LOCK_ICON } from '../data'
import type { AuditEntry } from '../data'
import { reqJSON } from '../helpers'
import type { Env } from '../types'

type Props = {
  entry: AuditEntry
  env: Env
  onClose: () => void
  onCopy: () => void
  onExport: () => void
}

export function AuditDrawer({ entry, env, onClose, onCopy, onExport }: Props) {
  const envLabel = env === 'sandbox' ? 'SANDBOX' : 'LIVE'
  const request = reqJSON({ id: 'job_' + entry.id.slice(-6), tin: entry.reqTin, invoice: entry.reqInvoice, app: 'AP-Sterling' }, env)
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'rgba(20,23,26,0.32)', animation: 'opsFade 160ms ease-out' }} />
      <div
        className="ops-drawer"
        style={{
          position: 'fixed',
          top: 0,
          right: 0,
          bottom: 0,
          zIndex: 81,
          width: 560,
          maxWidth: '94vw',
          background: 'var(--bg-1)',
          borderLeft: '1px solid var(--line-2)',
          boxShadow: '-24px 0 48px -24px rgba(20,23,26,0.3)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'opsDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: 1 }}>
            <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 4 }}>{entry.action}</div>
            <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {entry.id} · {entry.ts}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="ops-btn"
            style={{ border: 0, background: 'var(--bg-3)', cursor: 'pointer', width: 30, height: 30, borderRadius: 6, color: 'var(--fg-2)', display: 'grid', placeItems: 'center' }}
          >
            {CLOSE_ICON}
          </button>
        </div>
        {/* immutability banner */}
        <div style={{ flex: 'none', background: 'var(--status-muted-bg)', borderBottom: '1px solid var(--line-1)', padding: '9px 22px', display: 'flex', alignItems: 'center', gap: 9 }}>
          {LOCK_ICON}
          <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: 'var(--fg-2)', letterSpacing: '0.03em' }}>
            READ-ONLY EVIDENCE · CANNOT BE EDITED OR DELETED · HASH-CHAINED
          </span>
        </div>
        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 1, background: 'var(--line-1)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden', marginBottom: 20 }}>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Actor</div>
              <div style={{ fontSize: 12.5, fontWeight: 600, marginTop: 4 }}>{entry.actor}</div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Tenant</div>
              <div style={{ fontSize: 12.5, fontWeight: 600, marginTop: 4 }}>{entry.tenant}</div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Object type</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {entry.objectType}
              </div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Environment</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {envLabel}
              </div>
            </div>
          </div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 8, background: 'var(--bg-2)', padding: '12px 14px', marginBottom: 20 }}>
            <div className="label" style={{ marginBottom: 6 }}>
              Entry hash
            </div>
            <div className="mono" style={{ fontSize: 11, color: 'var(--fg-2)', wordBreak: 'break-all', lineHeight: 1.5 }}>
              {entry.hash}
            </div>
            <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', marginTop: 6 }}>
              prev → {entry.prevHash}
            </div>
          </div>
          <div className="label" style={{ marginBottom: 10 }}>
            Captured request
          </div>
          <pre className="ops-json" style={{ marginBottom: 18 }}>
            {request}
          </pre>
          <div className="label" style={{ marginBottom: 10 }}>
            Captured response
          </div>
          <pre className="ops-json">{entry.response}</pre>
        </div>
        <div style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', gap: 10 }}>
          <button type="button" onClick={onCopy} className="ops-btn v2-btn v2-btn-ghost" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            {COPY_ICON} Copy JSON
          </button>
          <button type="button" onClick={onExport} className="ops-btn v2-btn v2-btn-ghost" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            {EXPORT_ICON} Export evidence
          </button>
        </div>
      </div>
    </>
  )
}
