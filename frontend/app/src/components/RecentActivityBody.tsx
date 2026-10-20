// Body of the Overview "Recent activity" tile: the workspace's last audit events.
import { ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { type AuditResponse, getAuditLog } from '../lib/audit'
import { activityQuery, activityRow } from '../lib/dashboardActivity'
import type { PlatformCtx } from '../types'

const PAD = { padding: '0 20px' } as const

function EmptyActivity() {
  return (
    <div style={{ padding: '40px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
      <div className="card-title" style={{ marginBottom: 3 }}>No activity yet</div>
      <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>Edits, validations, approvals and submissions appear here as they happen.</div>
    </div>
  )
}

export function RecentActivityBody({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const entityId = ctx.mode === 'inhouse' ? null : ctx.active.entityId ?? null
  const query = activityQuery(ctx.mode === 'inhouse', entityId)

  const log = useAsync<AuditResponse>(
    () => (base && query ? getAuditLog(ctx.authedFetch, base, query) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null && query != null, isEmpty: (r) => r.events.length === 0, deps: [ctx.mode, entityId] },
  )

  // A null query never fetches, so useAsync keeps the previous client's state; show empty instead.
  if (query == null) return <EmptyActivity />
  if (log.status === 'loading') return <div style={PAD}><Loading label="Loading activity…" /></div>
  if (log.status === 'error' && log.error) return <div style={PAD}><ErrorState error={log.error} onRetry={log.run} /></div>
  if (log.status !== 'ready' || !log.data) return <EmptyActivity />

  const now = Date.now()
  const rows = log.data.events.map((e) => activityRow(e, now))
  return (
    <div style={{ padding: '18px 20px 6px' }}>
      {rows.map((r, i) => (
        <div key={r.key} style={{ display: 'flex', gap: 12 }}>
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flex: 'none' }}>
            <span style={{ width: 8, height: 8, borderRadius: '50%', background: r.dot, marginTop: 4 }} />
            <span style={{ width: 1, flex: 1, background: 'var(--line-2)', minHeight: i === rows.length - 1 ? '0px' : '18px' }} />
          </div>
          <div style={{ paddingBottom: 16, flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 13, lineHeight: 1.4 }}>
              <span style={{ fontWeight: 600 }}>{r.who}</span> <span style={{ color: 'var(--fg-2)' }}>{r.label}</span>
              {r.company && (
                <>
                  {' '}
                  <span className="mono" style={{ fontSize: 12, color: 'var(--action)' }}>{r.company}</span>
                </>
              )}
            </div>
            <div className="mono" title={r.whenTitle} style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>{r.when}</div>
          </div>
        </div>
      ))}
    </div>
  )
}
