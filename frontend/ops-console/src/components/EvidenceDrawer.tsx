// Signed evidence bundle drawer (prototype lines 657-704). Overlay geometry is the
// same shell as JobDrawer — same 580px width, same scrim/panel z-order and animations.

import { CLOSE_ICON, COPY_ICON, EVIDENCE_QR, EXPORT_ICON, SHIELD_ICON } from '../data'
import { reqJSON } from '../helpers'
import type { EvidenceBundle } from '../charts'
import type { Env } from '../types'

type Props = {
  evidence: EvidenceBundle
  env: Env
  onClose: () => void
  onCopy: () => void
  onDownload: () => void
}

export function EvidenceDrawer({ evidence, env, onClose, onCopy, onDownload }: Props) {
  // proto:1232 — the submitted-invoice JSON embeds the live sandbox/live toggle, so it
  // is built per render rather than frozen onto the bundle. Note the id handed to
  // reqJSON is the `sub_` form, not the row's own `ev_` id: it exists only to derive
  // the `idem_` idempotency key, so the two ids stay distinct.
  const request = reqJSON(
    { id: 'sub_' + evidence.invoice.slice(-6), buyer: evidence.buyer, btin: evidence.btin, invoice: evidence.invoice, raw: evidence.raw, desc: evidence.desc },
    env,
  )

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
          width: 580,
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
            <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 4 }}>Evidence bundle · {evidence.invoice}</div>
            <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {evidence.irn} · {evidence.cleared}
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

        <div
          style={{
            flex: 'none',
            background: 'var(--status-green-bg)',
            borderBottom: '1px solid var(--status-green-border)',
            padding: '9px 22px',
            display: 'flex',
            alignItems: 'center',
            gap: 9,
          }}
        >
          {SHIELD_ICON}
          <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: 'var(--status-green-text)', letterSpacing: '0.03em' }}>
            ACCEPTED BY FIRS/MBS · SIGNED & HASH-CHAINED · IMMUTABLE
          </span>
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: '1fr 1fr',
              gap: 1,
              background: 'var(--line-1)',
              border: '1px solid var(--line-1)',
              borderRadius: 8,
              overflow: 'hidden',
              marginBottom: 20,
            }}
          >
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">IRN</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4, color: 'var(--accent)' }}>
                {evidence.irn}
              </div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Buyer</div>
              <div style={{ fontSize: 12.5, fontWeight: 600, marginTop: 4 }}>{evidence.buyer}</div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Invoice value</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {evidence.value}
              </div>
            </div>
            <div style={{ background: 'var(--bg-2)', padding: '12px 14px' }}>
              <div className="label">Cleared at</div>
              <div className="mono" style={{ fontSize: 12, fontWeight: 600, marginTop: 4 }}>
                {evidence.cleared}
              </div>
            </div>
          </div>

          <div
            style={{
              display: 'grid',
              gridTemplateColumns: '92px 1fr',
              gap: 14,
              alignItems: 'center',
              border: '1px solid var(--line-1)',
              borderRadius: 8,
              background: 'var(--bg-2)',
              padding: 14,
              marginBottom: 20,
            }}
          >
            <div style={{ width: 92, height: 92, background: '#0c0e10', borderRadius: 6, display: 'grid', placeItems: 'center' }}>{EVIDENCE_QR}</div>
            <div>
              <div className="label" style={{ marginBottom: 6 }}>
                CSID signature
              </div>
              <div className="mono" style={{ fontSize: 11, color: 'var(--fg-2)', wordBreak: 'break-all', lineHeight: 1.5 }}>
                {evidence.csid}
              </div>
            </div>
          </div>

          <div style={{ border: '1px solid var(--line-1)', borderRadius: 8, background: 'var(--bg-2)', padding: '12px 14px', marginBottom: 20 }}>
            <div className="label" style={{ marginBottom: 6 }}>
              Entry hash
            </div>
            <div className="mono" style={{ fontSize: 11, color: 'var(--fg-2)', wordBreak: 'break-all', lineHeight: 1.5 }}>
              {evidence.hash}
            </div>
            <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', marginTop: 6 }}>
              prev → {evidence.prevHash}
            </div>
          </div>

          <div className="label" style={{ marginBottom: 10 }}>
            Submitted invoice
          </div>
          <pre className="ops-json" style={{ marginBottom: 18 }}>{request}</pre>
          <div className="label" style={{ marginBottom: 10 }}>
            Clearance response
          </div>
          <pre className="ops-json">{evidence.response}</pre>
        </div>

        <div style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', gap: 10 }}>
          <button type="button" onClick={onCopy} className="ops-btn v2-btn v2-btn-ghost" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            {COPY_ICON} Copy JSON
          </button>
          <button type="button" onClick={onDownload} className="ops-btn v2-btn v2-btn-primary" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            {EXPORT_ICON} Download bundle
          </button>
        </div>
      </div>
    </>
  )
}
