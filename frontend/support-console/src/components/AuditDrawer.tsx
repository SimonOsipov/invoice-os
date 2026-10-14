import { auditStamp, type StaffAuditRow, type StaffChange } from '../auditApi'
import { Drawer, MetaGrid } from './Drawer'

export function AuditEntryDrawer({ entry, onClose }: { entry: StaffAuditRow; onClose: () => void }) {
  return (
    <Drawer
      width={560}
      onClose={onClose}
      header={
        <>
          <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 4, overflowWrap: 'anywhere' }}>{entry.event}</div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', overflowWrap: 'anywhere' }}>
            #{entry.id} · {auditStamp(entry.created_at)}
          </div>
        </>
      }
    >
      <MetaGrid
        items={[
          { label: 'Tenant', value: entry.tenant_name ?? entry.tenant_id, mono: entry.tenant_name === null },
          { label: 'Actor', value: entry.actor_name, mono: false },
          { label: 'Actor id', value: entry.actor },
          { label: 'Time', value: auditStamp(entry.created_at) },
        ]}
      />
      <div className="label" style={{ marginBottom: 8 }}>
        Payload
      </div>
      <pre className="ops-json" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
        {JSON.stringify(entry.payload, null, 2)}
      </pre>
    </Drawer>
  )
}

export function StaffChangeDrawer({ change, onClose, onOpenTenantLog }: { change: StaffChange; onClose: () => void; onOpenTenantLog: () => void }) {
  return (
    <Drawer
      width={560}
      onClose={onClose}
      header={
        <>
          <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 4, overflowWrap: 'anywhere' }}>{change.event}</div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', overflowWrap: 'anywhere' }}>
            #{change.id} · {auditStamp(change.created_at)}
          </div>
        </>
      }
      footer={
        change.tenant_id ? (
          <>
            <div style={{ flex: 1 }} />
            <button type="button" onClick={onOpenTenantLog} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 32, padding: '0 12px', fontSize: 12 }}>
              Open in tenant log
            </button>
          </>
        ) : undefined
      }
    >
      <MetaGrid
        items={[
          { label: 'Staff member', value: change.actor },
          { label: 'Tenant', value: change.tenant_id ? (change.tenant_name ?? change.tenant_id) : 'All tenants', mono: !!change.tenant_id && change.tenant_name === null },
          { label: 'Subject', value: change.subject_id ?? '—' },
          { label: 'Rule set version', value: change.rule_set_version_id ?? '—' },
          { label: 'Time', value: auditStamp(change.created_at) },
        ]}
      />
      <div className="label" style={{ marginBottom: 8 }}>
        Payload
      </div>
      <pre className="ops-json" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
        {JSON.stringify(change.payload, null, 2)}
      </pre>
    </Drawer>
  )
}
