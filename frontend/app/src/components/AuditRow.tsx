// One audit row (Who · What · Company · When + chevron) and its expansion body.
//
// Stateless w.r.t. open/closed -- the parent owns `expandedId` (ReviewInvoicesTab.tsx's
// idiom), which is what makes single-open free and lets AUDIT-09 mount this row scoped to
// one invoice. AuditRow.test.tsx: auditRow_isExtractable pins that independence.
//
// The grid constants live here, not in AuditTable.tsx, following the shipped
// REVIEW_GRID_COLUMNS precedent: the row owns the geometry and the header imports it, so
// the two can never drift apart.

import { chevDownGlyph } from '../glyphs'
import type { AuditEvent } from '../lib/audit'
import { auditEventView } from '../lib/auditVocabulary'
import { fmtDateTime } from '../lib/format'

import { ActorCell } from './ActorCell'

// MembersTable.tsx's shape: two flexible columns, two fixed, a 44px chevron rail.
//
//   190 who floor + 220 what floor + 374 fixed (160+170+44) + 4 gaps x 12 + 36 padding = 868
//
// The min-width is restated on every row -- that restatement is what stops a row collapsing
// inside the scroll container (e2e/topology/audit.spec.ts asserts it on the rendered page).
export const AUDIT_COLS = 'minmax(190px,1fr) minmax(220px,1.4fr) 160px 170px 44px'
export const AUDIT_TABLE_MIN_WIDTH = 868
export const AUDIT_GRID_GAP = 12

const EVIDENCE_REASON_ID = 'audit-evidence-blocked-reason'
const EVIDENCE_REASON = 'The evidence bundle is not reachable from this screen yet.'

const TONE_TEXT: Record<string, string> = {
  green: 'var(--status-green-text)',
  red: 'var(--status-red-text)',
  amber: 'var(--status-amber-text)',
}

// Both keys are real: `id` from internal/invoice/* and approval/engine.go, `invoice_id`
// from approval/decision.go and submission/verdict_audit.go.
function invoiceRef(payload: unknown): { id: string; number: string | null } | null {
  const p = asRecord(payload)
  if (!p) return null
  const id = typeof p.invoice_id === 'string' ? p.invoice_id : typeof p.id === 'string' ? p.id : null
  if (!id) return null
  return { id, number: typeof p.invoice_number === 'string' ? p.invoice_number : null }
}

function asRecord(payload: unknown): Record<string, unknown> | null {
  if (payload == null || typeof payload !== 'object' || Array.isArray(payload)) return null
  return payload as Record<string, unknown>
}

function fieldLabel(key: string): string {
  const words = key.replace(/_/g, ' ').trim()
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : key
}

function fieldValue(value: unknown): string {
  if (value == null) return '—'
  if (typeof value === 'string') return value === '' ? '—' : value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return JSON.stringify(value)
}

export interface AuditRowProps {
  event: AuditEvent
  expanded: boolean
  onToggle: () => void
  // Absent on an invoice-scoped mount (AUDIT-09): there is nothing to narrow to when the
  // caller already filtered to one invoice, so the affordance simply does not render.
  onFilterToInvoice?: (invoiceId: string, invoiceNumber: string | null) => void
}

export function AuditRow({ event, expanded, onToggle, onFilterToInvoice }: AuditRowProps) {
  const view = auditEventView(event.event)
  const inv = invoiceRef(event.payload)
  const payload = asRecord(event.payload)
  const keys = payload ? Object.keys(payload) : []

  return (
    <>
      <div
        onClick={onToggle}
        data-testid="audit-row"
        aria-expanded={expanded}
        className="pf-row pf-list-row"
        style={{ display: 'grid', gridTemplateColumns: AUDIT_COLS, gap: AUDIT_GRID_GAP, minWidth: AUDIT_TABLE_MIN_WIDTH, padding: '12px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
      >
        <ActorCell actor={event.actor} actor_name={event.actor_name} actor_kind={event.actor_kind} />
        {/* Colour is reserved for outcome (auditVocabulary.ts) -- an event with no outcome
            takes the ordinary text colour, so a domain can never tint the row. */}
        <span data-testid="audit-what" style={{ fontSize: 13, fontWeight: 500, color: view.tone ? TONE_TEXT[view.tone] : 'var(--fg-1)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {view.label}
        </span>
        {/* company_scope, not a null check: 'workspace' means the event belongs to the firm
            itself, which is not the same absence as an unattributed row. */}
        <span data-testid="audit-company" style={{ fontSize: 12.5, color: event.company_scope === 'company' ? 'var(--fg-1)' : 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {event.company_scope === 'company' ? (event.company_name ?? '—') : event.company_scope === 'workspace' ? 'Workspace' : '—'}
        </span>
        <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-3)', whiteSpace: 'nowrap' }}>{fmtDateTime(event.created_at)}</span>
        <span aria-hidden style={{ display: 'inline-flex', justifyContent: 'flex-end', color: 'var(--fg-4)', pointerEvents: 'none', transform: expanded ? 'rotate(0deg)' : 'rotate(-90deg)', transition: 'transform 160ms' }}>
          {chevDownGlyph}
        </span>
      </div>
      {expanded && (
        <div data-testid="audit-expansion" style={{ minWidth: AUDIT_TABLE_MIN_WIDTH, padding: '14px 18px 16px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
          {keys.length === 0 ? (
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>This event carries no detail.</span>
          ) : (
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(210px,1fr))', gap: '10px 24px' }}>
              {keys.map((k) => (
                <span key={k} data-testid="audit-payload-field" style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
                  <span style={{ fontSize: 10.5, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--fg-3)' }}>{fieldLabel(k)}</span>
                  <span className="mono" style={{ fontSize: 12, color: 'var(--fg-1)', overflowWrap: 'anywhere' }}>{fieldValue(payload?.[k])}</span>
                </span>
              ))}
            </div>
          )}
          {inv != null && onFilterToInvoice != null && (
            <button
              type="button"
              data-testid="audit-invoice-affordance"
              onClick={() => onFilterToInvoice(inv.id, inv.number)}
              className="pf-btn"
              style={{ marginTop: 14, border: 0, padding: 0, background: 'transparent', color: 'var(--accent-text, var(--fg-1))', fontSize: 12.5, fontWeight: 500, cursor: 'pointer' }}
            >
              {inv.number != null ? `All events for ${inv.number} →` : 'All events for this invoice →'}
            </button>
          )}
          {/* AUDIT-08 owns the evidence drawer. Disabled with a VISIBLE reason rather than
              hidden (InvoiceDetail.tsx's idiom) -- a title= on a disabled button never
              fires in Chromium, so the reason has to be text. */}
          {view.domain === 'submissions' && inv != null && (
            <div style={{ marginTop: 10 }}>
              <button
                type="button"
                data-testid="audit-evidence-affordance"
                disabled
                aria-describedby={EVIDENCE_REASON_ID}
                className="pf-btn"
                style={{ border: 0, padding: 0, background: 'transparent', color: 'var(--fg-4)', fontSize: 12.5, fontWeight: 500, cursor: 'not-allowed' }}
              >
                View transmission evidence →
              </button>
              <div id={EVIDENCE_REASON_ID} data-testid="audit-evidence-blocked-reason" style={{ marginTop: 2, fontSize: 11.5, color: 'var(--fg-3)' }}>
                {EVIDENCE_REASON}
              </div>
            </div>
          )}
          {/* The row shows the human label; the footer keeps the identifier that label was
              derived from, so a support conversation can name the exact event. */}
          <div className="mono" style={{ marginTop: 14, display: 'flex', gap: 16, flexWrap: 'wrap', fontSize: 10.5, color: 'var(--fg-4)' }}>
            <span data-testid="audit-event-identifier">{event.event}</span>
            <span data-testid="audit-event-id">{event.id}</span>
          </div>
        </div>
      )}
    </>
  )
}
