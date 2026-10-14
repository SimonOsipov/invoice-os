import { auditStamp, type StaffAuditRow } from '../auditApi'
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
