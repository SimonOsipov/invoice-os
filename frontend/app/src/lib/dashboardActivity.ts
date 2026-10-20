import { actorLabel } from './actor'
import type { AuditEvent, AuditLogQuery } from './audit'
import { auditEventView, type AuditTone } from './auditVocabulary'
import { fmtDate, fmtDateTime } from './format'

export const ACTIVITY_LIMIT = 5

export interface ActivityRowView {
  key: string
  who: string
  label: string
  company: string | null
  when: string
  whenTitle: string
  dot: string
}

const DOT: Record<NonNullable<AuditTone>, string> = {
  green: 'var(--status-green-text)',
  red: 'var(--status-red-text)',
  amber: 'var(--status-amber-text)',
}

// null: a firm with no resolved client. Sending no company would list every client.
export function activityQuery(isInhouse: boolean, entityId: string | null): AuditLogQuery | null {
  if (isInhouse) return { limit: ACTIVITY_LIMIT }
  if (entityId == null) return null
  return { limit: ACTIVITY_LIMIT, company: entityId }
}

export function activityRow(e: AuditEvent, now: number): ActivityRowView {
  const view = auditEventView(e.event)
  return {
    key: e.id,
    who: actorLabel(e.actor, { name: e.actor_name, kind: e.actor_kind }).text,
    label: view.label,
    company: e.company_scope === 'company' && e.company_name ? e.company_name : null,
    when: relativeAgo(e.created_at, now),
    whenTitle: fmtDateTime(e.created_at),
    dot: view.tone ? DOT[view.tone] : 'var(--line-3)',
  }
}

// ceiling: computed at render, does not tick; re-renders only on mount or client switch.
export function relativeAgo(iso: string, now: number): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const s = Math.floor((now - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  if (s < 7 * 86400) return `${Math.floor(s / 86400)}d ago`
  return fmtDate(iso)
}
