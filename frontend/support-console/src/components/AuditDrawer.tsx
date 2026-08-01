import { COPY_ICON, EXPORT_ICON, LOCK_ICON } from '../data'
import { requestJSON } from '../helpers'
import { Drawer, MetaGrid } from './Drawer'
import type { AuditEntry, Env } from '../types'

type Props = {
  entry: AuditEntry
  env: Env
  onClose: () => void
  onCopy: () => void
  onExport: () => void
}

// proto:1196. Both digests are fabricated and do not chain; audit_log (internal/audit) has
// no digest column either, so the drawer's banner claims only what the grants enforce.
function entryHash(id: string): string {
  return `sha256:9f${id.slice(-4)}a3e1b7c4d09f${id.slice(-2)}8e2c5a1f0b6d3e7c9a4`
}
function prevHash(id: string): string {
  return `sha256:8e${id.slice(-3)}c2`
}

export function AuditDrawer({ entry, env, onClose, onCopy, onExport }: Props) {
  // proto:1198 — the captured request is reconstructed from the entry's own object and
  // tenant so the drawer never shows a payload belonging to a different row.
  const request = requestJSON(
    {
      id: `job_${entry.id.slice(-6)}`,
      tin: `TIN ${entry.tenant.replace(/\D/g, '').slice(0, 8)}-0001`,
      invoice: entry.object,
      app: 'AP-Sterling',
    },
    env,
  )
  const response = `{
  "result": "ok",
  "actor": "${entry.actor}",
  "object": "${entry.object}",
  "action": "${entry.action}"
}`

  return (
    <Drawer
      onClose={onClose}
      header={
        <>
          <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 4 }}>{entry.action}</div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            {entry.id} · {entry.ts}
          </div>
        </>
      }
      banner={
        <div style={{ flex: 'none', background: 'var(--status-muted-bg)', borderBottom: '1px solid var(--line-1)', padding: '9px 22px', display: 'flex', alignItems: 'center', gap: 9 }}>
          {LOCK_ICON}
          <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: 'var(--fg-2)', letterSpacing: '0.03em' }}>
            READ-ONLY EVIDENCE · CANNOT BE EDITED OR DELETED
          </span>
        </div>
      }
      footer={
        <>
          <button type="button" onClick={onCopy} className="ops-btn v2-btn v2-btn-ghost" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            {COPY_ICON} Copy JSON
          </button>
          <button type="button" onClick={onExport} className="ops-btn v2-btn v2-btn-ghost" style={{ flex: 1, justifyContent: 'center', height: 40 }}>
            {EXPORT_ICON} Export evidence
          </button>
        </>
      }
    >
      <MetaGrid
        items={[
          { label: 'Actor', value: entry.actor, mono: false },
          { label: 'Tenant', value: entry.tenant, mono: false },
          { label: 'Object type', value: entry.objectType },
          { label: 'Environment', value: env === 'sandbox' ? 'SANDBOX' : 'LIVE' },
        ]}
      />

      <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '12px 14px', marginBottom: 20 }}>
        <div className="label" style={{ marginBottom: 6 }}>
          Entry hash
        </div>
        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-2)', wordBreak: 'break-all', lineHeight: 1.5 }}>
          {entryHash(entry.id)}
        </div>
        <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', marginTop: 6 }}>
          prev → {prevHash(entry.id)}
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
      <pre className="ops-json">{response}</pre>
    </Drawer>
  )
}
