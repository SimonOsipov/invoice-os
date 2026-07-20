// Invoice detail — fiscal record (IRN/CSID/QR after transmit), status pill, doc-type
// badge, line items + totals, validation panel, audit trail, and the View-XML / PDF /
// Transmit actions. Ported from Platform.dc.html ~L598-694 + the detail slice of
// renderVals() (~L1366-1395).

import { useState, type FormEvent, type ReactNode } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { amount, fmt, fmtDate, fmtPlain } from '../lib/format'
import { statusStyle, defaultDraft } from '../lib/clients'
import { validate } from '../lib/validation'
import { fiscalRecord, Qr } from '../lib/qr'
import { detailTarget } from '../lib/importReport'
import {
  editInvoice,
  getInvoice,
  getInvoiceHistory,
  invoiceStatusStyle,
  isFixable,
  revalidateInvoice,
  shouldFetchInvoices,
  verdictStatus,
  type InvoiceEditInput,
  type InvoiceRecord,
  type StatusChange,
} from '../lib/invoices'
import { ViolationsTable } from './ViolationsTable'
import { crossGlyph, docGlyph2, downloadGlyph, sendGlyph, shieldGlyph, tickGlyph13, warnGlyph } from '../glyphs'
import type { Invoice, PlatformCtx } from '../types'

const DOC_FULL: Record<string, string> = {
  B2G: 'Business → Government',
  B2C: 'Business → Consumer',
  B2B: 'Business → Business',
}

export function InvoiceDetail({ ctx }: { ctx: PlatformCtx }) {
  const { active, selectedId } = ctx

  // Click-through from the import report (M4-08-05, AC7). This MUST return before the
  // `invList.find(...) || invList[0] || fallback` chain below: an imported invoice is a
  // real server UUID that matches no mock invoice number, so falling through would render
  // an UNRELATED mock invoice — someone else's buyer, items and totals — under a real
  // invoice's id. That is worse than showing nothing ([click-through-honest-placeholder]).
  // M4-09-05: this branch now mounts the live detail surface (LiveInvoiceDetail, below)
  // instead of the inert placeholder; the mock (selectMock) fallback chain underneath is
  // untouched — [mock-detail-branch-left], removal is M4-10.
  const target = detailTarget({ selectedId, importedInvoiceId: ctx.importedInvoiceId })
  if (target.kind === 'imported') {
    // key={invoiceId}: forces a full remount on invoice SWITCH so the previous invoice's
    // local state (edit-form field values, staleSinceEdit, revalidateError) doesn't leak
    // into the next one. The key stays stable while invoiceId is unchanged, so the
    // in-place history/detail refresh after edit/re-validate within one invoice is
    // unaffected — only switching invoices remounts.
    return <LiveInvoiceDetail key={target.invoiceId} ctx={ctx} invoiceId={target.invoiceId} />
  }

  const invList = active.invoices
  // Fall back to a synthesized draft-shaped invoice, mirroring the prototype's guard.
  const fallback = defaultDraft(active)
  const detailInv: Invoice =
    invList.find((i) => i.number === selectedId) ||
    invList[0] || { ...fallback, status: 'Draft', items: fallback.items }

  const dsub = amount(detailInv.items)
  const dvat = dsub * 0.075
  const dval = validate(detailInv)
  const dErr = dval.errors.length
  const dWarn = dval.warnings.length
  const detailValOk = dErr === 0 && dWarn === 0

  const detailChecks: { label: string; bg: string; fg: string; glyph: ReactNode }[] = []
  dval.errors.forEach((e) => detailChecks.push({ label: e.label, bg: 'var(--status-red-bg)', fg: 'var(--status-red-text)', glyph: crossGlyph }))
  dval.warnings.forEach((e) => detailChecks.push({ label: e.label, bg: 'var(--status-amber-bg)', fg: 'var(--status-amber-text)', glyph: warnGlyph }))
  dval.passed.slice(0, 6).forEach((p) => detailChecks.push({ label: p, bg: 'var(--status-green-bg)', fg: 'var(--status-green-text)', glyph: tickGlyph13 }))

  const audit: { event: string; meta: string; dot: string; line: string }[] = [
    { event: 'Invoice created', meta: 'AMARA OKAFOR · ' + detailInv.date + ' 09:14', dot: 'var(--fg-3)', line: '20px' },
    { event: 'Validated · ' + dval.passed.length + '/16 checks', meta: 'ENGINE · ' + detailInv.date + ' 09:15', dot: detailValOk ? 'var(--status-green-text)' : 'var(--status-amber-text)', line: '20px' },
    { event: detailInv.status === 'Rejected' ? 'Rejected — validation failed' : 'Approved for transmission', meta: (detailInv.status === 'Rejected' ? 'I. BELLO · ' : 'T. ADEYEMI · ') + detailInv.date + ' 11:02', dot: detailInv.status === 'Rejected' ? 'var(--status-red-text)' : 'var(--status-green-text)', line: detailInv.status === 'Transmitted' ? '20px' : '0px' },
  ]
  if (detailInv.status === 'Transmitted') audit.push({ event: 'Transmitted to FIRS', meta: 'MBS ADAPTER · CSID-' + detailInv.number.slice(-5), dot: 'var(--accent)', line: '0px' })

  const dType = detailInv.docType || 'B2B'
  const isTransmitted = detailInv.status === 'Transmitted'
  const detailTransmittable = detailInv.status === 'Approved'
  const st = statusStyle(detailInv.status)
  const transmitBtn = isTransmitted
    ? { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)', cursor: 'default', label: 'Transmitted', glyph: tickGlyph13 }
    : detailTransmittable
      ? { bg: 'var(--accent)', color: '#fff', cursor: 'pointer', label: 'Transmit to FIRS', glyph: sendGlyph }
      : { bg: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed', label: 'Not approved', glyph: sendGlyph }

  const fiscal = isTransmitted ? fiscalRecord(detailInv) : null

  return (
    <div style={{ padding: '24px 36px 56px', maxWidth: 1080, margin: '0 auto' }}>
      <button onClick={() => ctx.nav('invoices')} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 32, padding: '0 12px', fontSize: 13, marginBottom: 18 }}>
        ← All invoices
      </button>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 22, gap: 24, flexWrap: 'wrap' }}>
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 6 }}>
            <h1 className="mono" style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.01em', margin: 0, whiteSpace: 'nowrap' }}>{detailInv.number}</h1>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '4px 10px' }}>
              <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
              <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
            </span>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7, background: 'var(--bg-3)', border: '1px solid var(--line-2)', borderRadius: 999, padding: '4px 10px' }}>
              <span className="mono" style={{ fontSize: 10, fontWeight: 700, color: 'var(--fg-2)' }}>{dType}</span>
              <span style={{ fontSize: 11, color: 'var(--fg-3)', whiteSpace: 'nowrap' }}>{DOC_FULL[dType]}</span>
            </span>
          </div>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{detailInv.buyer} · {detailInv.date}</p>
        </div>
        <div style={{ display: 'flex', gap: 10 }}>
          <button onClick={ctx.openXml} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36 }}>
            <span style={{ display: 'inline-flex' }}>{docGlyph2}</span> View UBL/XML
          </button>
          <button className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36 }}>
            <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span> PDF
          </button>
          <button onClick={ctx.transmit} className="v2-btn pf-btn" style={{ height: 36, background: transmitBtn.bg, color: transmitBtn.color, cursor: transmitBtn.cursor }}>
            <span style={{ display: 'inline-flex' }}>{transmitBtn.glyph}</span> {transmitBtn.label}
          </button>
        </div>
      </div>
      <div className="pf-detail-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 340px', gap: 16, alignItems: 'start' }}>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
          <div style={{ padding: 24, borderBottom: '1px solid var(--line-1)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 24, gap: 24 }}>
              <div>
                <div style={{ fontSize: 16, fontWeight: 700, letterSpacing: '-0.02em' }}>{active.name}</div>
                <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>
                  TIN {active.tin}
                </div>
              </div>
              <div style={{ textAlign: 'right' }}>
                <div className="label" style={{ marginBottom: 3 }}>
                  Bill to
                </div>
                <div style={{ fontSize: 13, fontWeight: 600 }}>{detailInv.buyer}</div>
                <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                  {detailInv.buyerTin}
                </div>
              </div>
            </div>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 6, overflow: 'hidden' }}>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 60px 120px 120px', gap: 10, padding: '9px 14px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
                <span className="label">Description</span>
                <span className="label" style={{ textAlign: 'right' }}>Qty</span>
                <span className="label" style={{ textAlign: 'right' }}>Unit</span>
                <span className="label" style={{ textAlign: 'right' }}>Amount</span>
              </div>
              {detailInv.items.map((it, i) => (
                <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 60px 120px 120px', gap: 10, padding: '11px 14px', borderBottom: '1px solid var(--line-1)' }}>
                  <span style={{ fontSize: 13 }}>{it.desc}</span>
                  <span className="mono" style={{ fontSize: 12, textAlign: 'right', color: 'var(--fg-2)' }}>{it.qty}</span>
                  <span className="money" style={{ fontSize: 12, textAlign: 'right', color: 'var(--fg-2)' }}>{fmtPlain(it.price)}</span>
                  <span className="money" style={{ fontSize: 12.5, textAlign: 'right', fontWeight: 600 }}>{fmt(it.qty * it.price)}</span>
                </div>
              ))}
            </div>
          </div>
          <div style={{ padding: '16px 24px', display: 'flex', justifyContent: 'flex-end' }}>
            <div style={{ width: 240, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>Subtotal</span>
                <span className="money" style={{ fontSize: 13 }}>{fmt(dsub)}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>VAT · 7.5%</span>
                <span className="money" style={{ fontSize: 13 }}>{fmt(dvat)}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', paddingTop: 9, borderTop: '1px solid var(--line-1)' }}>
                <span style={{ fontSize: 14, fontWeight: 600 }}>Total</span>
                <span className="money" style={{ fontSize: 16, fontWeight: 700 }}>{fmt(dsub + dvat)}</span>
              </div>
            </div>
          </div>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {isTransmitted && fiscal && (
            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--accent)', borderRadius: 8, overflow: 'hidden' }}>
              <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ color: 'var(--accent)', display: 'inline-flex' }}>{shieldGlyph}</span>
                <span style={{ fontSize: 14, fontWeight: 600 }}>FIRS fiscal record</span>
              </div>
              <div style={{ padding: '16px 18px' }}>
                <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
                  <div style={{ flex: 'none', padding: 7, background: '#fff', border: '1px solid var(--line-1)', borderRadius: 8 }}>
                    <Qr seed={fiscal.irn} size={116} />
                  </div>
                  <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 11 }}>
                    <div>
                      <div className="label" style={{ marginBottom: 3 }}>
                        IRN
                      </div>
                      <div className="mono" style={{ fontSize: 11.5, fontWeight: 600, wordBreak: 'break-all', lineHeight: 1.4 }}>{fiscal.irn}</div>
                    </div>
                    <div>
                      <div className="label" style={{ marginBottom: 3 }}>
                        CSID
                      </div>
                      <div className="mono" style={{ fontSize: 11, color: 'var(--fg-2)', wordBreak: 'break-all', lineHeight: 1.4 }}>{fiscal.csid}</div>
                    </div>
                  </div>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 14, paddingTop: 13, borderTop: '1px solid var(--line-1)' }}>
                  <span style={{ width: 6, height: 6, borderRadius: 99, background: 'var(--status-green-text)' }} />
                  <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                    SIGNED &amp; STAMPED · {fiscal.stampedAt}
                  </span>
                </div>
              </div>
            </div>
          )}
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
            <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>Validation</span>
              <span className="mono" style={{ fontSize: 11, color: detailValOk ? 'var(--status-green-text)' : 'var(--status-red-text)', fontWeight: 600 }}>
                {detailValOk ? '16/16 PASSED' : dErr + dWarn + ' OPEN'}
              </span>
            </div>
            <div style={{ padding: '8px 0' }}>
              {detailChecks.map((c, i) => (
                <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '7px 18px' }}>
                  <span style={{ flex: 'none', width: 16, height: 16, borderRadius: 99, background: c.bg, color: c.fg, display: 'grid', placeItems: 'center' }}>{c.glyph}</span>
                  <span style={{ flex: 1, fontSize: 12.5, color: 'var(--fg-2)' }}>{c.label}</span>
                </div>
              ))}
            </div>
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
            <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>Audit trail</span>
            </div>
            <div style={{ padding: '16px 18px' }}>
              {audit.map((a, i) => (
                <div key={i} style={{ display: 'flex', gap: 12 }}>
                  <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flex: 'none' }}>
                    <span style={{ width: 8, height: 8, borderRadius: 99, background: a.dot, marginTop: 4 }} />
                    <span style={{ width: 1, flex: 1, background: 'var(--line-2)', minHeight: a.line }} />
                  </div>
                  <div style={{ paddingBottom: 16 }}>
                    <div style={{ fontSize: 13, fontWeight: 500 }}>{a.event}</div>
                    <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                      {a.meta}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

// --- Live detail surface (M4-09-05, task-186) -------------------------------
//
// Own component (not extra hooks bolted onto InvoiceDetail's conditional-return body)
// because InvoiceDetail's `target.kind === 'imported'` branch returns before this point —
// calling useAsync/useState after that return would break the rules of hooks. Mirrors
// ClientsView/ValidationView: gatewayBase() + useAsync + a Loading/ErrorState/ready
// ladder, zero network when no gateway is configured.
const EDIT_FIELD_KEYS = [
  'issue_date',
  'supplier_tin',
  'supplier_name',
  'buyer_tin',
  'buyer_name',
  'currency',
  'subtotal',
  'vat',
  'total',
] as const

type EditFieldKey = (typeof EDIT_FIELD_KEYS)[number]
type EditFormState = Record<EditFieldKey, string>

function formFromInvoice(inv: InvoiceRecord): EditFormState {
  return {
    issue_date: inv.issue_date ?? '',
    supplier_tin: inv.supplier_tin ?? '',
    supplier_name: inv.supplier_name ?? '',
    buyer_tin: inv.buyer_tin ?? '',
    buyer_name: inv.buyer_name ?? '',
    currency: inv.currency ?? '',
    subtotal: inv.subtotal ?? '',
    vat: inv.vat ?? '',
    total: inv.total ?? '',
  }
}

// PATCH /v1/invoices/{id} treats an absent key as "leave this field alone" (the Go
// handler decodes into *string/*time.Time pointers, nil when the key is missing) and a
// present key — including "" — as "set it". Sending all 9 fields on every submit would
// silently blank out the 8 the user didn't touch; diffing against the invoice this form
// was seeded from keeps the PATCH to only what actually changed (mirrors
// EntityFormModal's toEntityUpdateInput diff-then-skip-if-empty convention).
//
// issue_date is special-cased: editReq.IssueDate decodes into a *time.Time
// (handlers.go:71/invoice.go:89), which json.Unmarshal only accepts as a full RFC3339
// string — a bare "YYYY-MM-DD" (the field's own placeholder) or "" fails to decode and
// 400s BEFORE Store.Edit ever runs (verified against Go's actual time.Time
// UnmarshalJSON). Normalize a bare date to midnight UTC so the field the placeholder
// invites the user to type actually round-trips. A cleared ("") date is skipped rather
// than sent: json "null" and an absent key both decode to a nil pointer ("leave
// unchanged", [D9]), so an explicit clear-to-blank cannot be represented over this PATCH
// at all — sending "" would just surface a confusing decode-failure for an operation the
// backend has no way to honor.
function diffEditInput(original: InvoiceRecord, form: EditFormState): InvoiceEditInput {
  const patch: InvoiceEditInput = {}
  for (const key of EDIT_FIELD_KEYS) {
    if (form[key] === (original[key] ?? '')) continue
    if (key === 'issue_date') {
      const value = form.issue_date.trim()
      if (!value) continue
      patch.issue_date = /^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T00:00:00Z` : value
      continue
    }
    patch[key] = form[key]
  }
  return patch
}

function LiveInvoiceDetail({ ctx, invoiceId }: { ctx: PlatformCtx; invoiceId: string }) {
  const base = gatewayBase()
  // Same `base ? … : …` narrowing as ClientsView/ValidationView ([A-e]/[A-m]) —
  // `immediate: shouldFetchInvoices(base)` keeps a no-gateway build at zero network.
  const detail = useAsync<InvoiceRecord>(
    () => (base ? getInvoice(ctx.authedFetch, base, invoiceId) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchInvoices(base), deps: [invoiceId] },
  )
  const history = useAsync<StatusChange[]>(
    () => (base ? getInvoiceHistory(ctx.authedFetch, base, invoiceId) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchInvoices(base), deps: [invoiceId] },
  )
  // Within-session fix-loop indicator (Core AC #7 / [stale-violations-honest] /
  // [stale-is-session-state]): set on a successful edit, cleared on Re-validate. On
  // initial load this stays false, so the stored verdict renders WITHOUT a stale banner
  // — the on-load honesty derivation is [stale-on-load-followup], deferred.
  const [staleSinceEdit, setStaleSinceEdit] = useState(false)
  const [revalidating, setRevalidating] = useState(false)
  const [revalidateError, setRevalidateError] = useState<string | null>(null)

  let content: ReactNode

  // invoicesViewState (lib/invoices.ts) is pinned to AsyncState<InvoiceRecord[]> (the
  // list surface's shape) and can't type-check against this single-record fetch, so the
  // same base==null -> idle short-circuit is inlined here rather than widening that
  // helper — this subtask's edit map scopes changes to InvoiceDetail.tsx only.
  if (base == null) {
    content = <EmptyState title="No gateway configured" message="Connect a gateway to load this invoice." />
  } else if (detail.status === 'loading') {
    content = <Loading label="Loading invoice…" />
  } else if (detail.status === 'error') {
    content = detail.error ? <ErrorState error={detail.error} onRetry={detail.run} /> : null
  } else if (detail.status !== 'ready' || !detail.data) {
    content = <EmptyState title="Invoice not found" message="This invoice could not be loaded." />
  } else {
    const inv = detail.data
    const st = invoiceStatusStyle(inv.status)
    const items = inv.line_items ?? []
    const subtotal = inv.subtotal != null ? Number(inv.subtotal) : null
    const vat = inv.vat != null ? Number(inv.vat) : null
    const total = inv.total != null ? Number(inv.total) : null
    const verdict = verdictStatus(staleSinceEdit)

    // Arrow functions (not `function` declarations): narrowing of `base` to non-null
    // (established by the `if (base == null)` branch above) does not survive into a
    // nested function DECLARATION — TS resets it there because declarations are
    // hoisted — but does survive into a closure/arrow function.
    const handleSaved = () => {
      setStaleSinceEdit(true)
      detail.run()
      history.run()
    }

    // isFixable(inv.status) gates this button on for both draft AND validated (see
    // below) so it stays visible when nothing has been edited yet -- clicking it on an
    // untouched 'validated' invoice hits Store.ApplyValidation's draft-only gate
    // ([gate-scope-draft-only]) and 409s (ErrNotDraft). Caught + surfaced here (mirrors
    // InvoiceEditForm's formError) rather than left as an unhandled rejection with no
    // user feedback.
    const handleRevalidate = async () => {
      if (revalidating) return
      setRevalidating(true)
      setRevalidateError(null)
      try {
        await revalidateInvoice(ctx.authedFetch, base, invoiceId)
        setStaleSinceEdit(false)
        detail.run()
        history.run()
      } catch (err) {
        setRevalidateError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        setRevalidating(false)
      }
    }

    content = (
      <>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 22, gap: 24, flexWrap: 'wrap' }}>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 6 }}>
              <h1 className="mono" style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.01em', margin: 0, whiteSpace: 'nowrap' }}>{inv.invoice_number}</h1>
              <span data-testid="invoice-status-badge" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '4px 10px' }}>
                <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
              </span>
            </div>
            <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{inv.buyer_name ?? '—'} · {fmtDate(inv.issue_date ?? inv.created_at)}</p>
          </div>
        </div>

        <div className="pf-detail-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 340px', gap: 16, alignItems: 'start' }}>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
            <div style={{ padding: 24, borderBottom: '1px solid var(--line-1)' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 24, gap: 24 }}>
                <div>
                  <div style={{ fontSize: 16, fontWeight: 700, letterSpacing: '-0.02em' }}>{inv.supplier_name ?? '—'}</div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>TIN {inv.supplier_tin ?? '—'}</div>
                </div>
                <div style={{ textAlign: 'right' }}>
                  <div className="label" style={{ marginBottom: 3 }}>Bill to</div>
                  <div style={{ fontSize: 13, fontWeight: 600 }}>{inv.buyer_name ?? '—'}</div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{inv.buyer_tin ?? '—'}</div>
                </div>
              </div>
              <div style={{ border: '1px solid var(--line-1)', borderRadius: 6, overflow: 'hidden' }}>
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 60px 120px 120px', gap: 10, padding: '9px 14px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="label">Description</span>
                  <span className="label" style={{ textAlign: 'right' }}>Qty</span>
                  <span className="label" style={{ textAlign: 'right' }}>Unit</span>
                  <span className="label" style={{ textAlign: 'right' }}>Amount</span>
                </div>
                {items.map((it) => (
                  <div key={it.id} style={{ display: 'grid', gridTemplateColumns: '1fr 60px 120px 120px', gap: 10, padding: '11px 14px', borderBottom: '1px solid var(--line-1)' }}>
                    <span style={{ fontSize: 13 }}>{it.description ?? '—'}</span>
                    <span className="mono" style={{ fontSize: 12, textAlign: 'right', color: 'var(--fg-2)' }}>{it.quantity ?? '—'}</span>
                    <span className="money" style={{ fontSize: 12, textAlign: 'right', color: 'var(--fg-2)' }}>{it.unit_price != null ? fmtPlain(Number(it.unit_price)) : '—'}</span>
                    <span className="money" style={{ fontSize: 12.5, textAlign: 'right', fontWeight: 600 }}>{it.line_total != null ? fmt(Number(it.line_total)) : '—'}</span>
                  </div>
                ))}
              </div>
            </div>
            <div style={{ padding: '16px 24px', display: 'flex', justifyContent: 'flex-end' }}>
              <div style={{ width: 240, display: 'flex', flexDirection: 'column', gap: 8 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                  <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>Subtotal</span>
                  <span className="money" style={{ fontSize: 13 }}>{subtotal != null ? fmt(subtotal) : '—'}</span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                  <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>VAT</span>
                  <span className="money" style={{ fontSize: 13 }}>{vat != null ? fmt(vat) : '—'}</span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'space-between', paddingTop: 9, borderTop: '1px solid var(--line-1)' }}>
                  <span style={{ fontSize: 14, fontWeight: 600 }}>Total</span>
                  <span className="money" style={{ fontSize: 16, fontWeight: 700 }}>{total != null ? fmt(total) : '—'}</span>
                </div>
              </div>
            </div>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
              <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
                <span style={{ fontSize: 14, fontWeight: 600 }}>Compliance</span>
              </div>
              <div style={{ padding: 16 }}>
                {verdict === 'stale' && (
                  <div
                    data-testid="stale-verdict"
                    style={{ marginBottom: 12, padding: '10px 12px', borderRadius: 6, background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)' }}
                  >
                    Edited since the last validation — this verdict is stale. Run Re-validate to refresh it.
                  </div>
                )}
                {inv.rule_set_version != null ? (
                  <div data-testid="violations-table">
                    <ViolationsTable violations={inv.violations} ruleSetVersion={inv.rule_set_version} />
                  </div>
                ) : (
                  <div
                    data-testid="not-validated"
                    style={{ padding: '12px 14px', borderRadius: 6, background: 'var(--bg-3)', border: '1px solid var(--line-2)', fontSize: 12.5, color: 'var(--fg-2)' }}
                  >
                    Not yet validated — run Re-validate to check compliance.
                  </div>
                )}
              </div>
            </div>

            {isFixable(inv.status) && (
              <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
                <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span style={{ fontSize: 14, fontWeight: 600 }}>Fix &amp; re-validate</span>
                  <button
                    type="button"
                    onClick={handleRevalidate}
                    disabled={revalidating}
                    data-testid="revalidate"
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={{ height: 30, padding: '0 12px', fontSize: 12.5 }}
                  >
                    {revalidating ? 'Revalidating…' : 'Re-validate'}
                  </button>
                </div>
                {revalidateError && (
                  <div style={{ margin: '12px 16px 0', padding: '10px 12px', borderRadius: 6, background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
                    {revalidateError}
                  </div>
                )}
                <InvoiceEditForm ctx={ctx} base={base} invoiceId={invoiceId} inv={inv} onSaved={handleSaved} />
              </div>
            )}

            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
              <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
                <span style={{ fontSize: 14, fontWeight: 600 }}>Status history</span>
              </div>
              <div data-testid="status-history" style={{ padding: '16px 18px' }}>
                {history.status === 'loading' && <Loading label="Loading history…" />}
                {history.status === 'error' && history.error && <ErrorState error={history.error} onRetry={history.run} />}
                {history.status === 'empty' && <div style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>No history yet.</div>}
                {history.status === 'ready' &&
                  (history.data ?? []).map((h, i, arr) => (
                    <div key={i} data-testid="status-history-row" style={{ display: 'flex', gap: 12 }}>
                      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flex: 'none' }}>
                        <span style={{ width: 8, height: 8, borderRadius: 99, background: 'var(--fg-3)', marginTop: 4 }} />
                        <span style={{ width: 1, flex: 1, background: 'var(--line-2)', minHeight: i === arr.length - 1 ? '0px' : '20px' }} />
                      </div>
                      <div style={{ paddingBottom: 16 }}>
                        <div style={{ fontSize: 13, fontWeight: 500 }}>
                          {h.from_status === null ? `Created · ${h.to_status}` : `${h.from_status} → ${h.to_status}`}
                        </div>
                        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>{h.actor} · {h.changed_at}</div>
                      </div>
                    </div>
                  ))}
              </div>
            </div>
          </div>
        </div>
      </>
    )
  }

  return (
    <div data-testid="invoice-detail" style={{ padding: '24px 36px 56px', maxWidth: 1080, margin: '0 auto' }}>
      <button onClick={() => ctx.nav('invoices')} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 32, padding: '0 12px', fontSize: 13, marginBottom: 18 }}>
        ← All invoices
      </button>
      {content}
    </div>
  )
}

// The 9-field PATCH form ([edit-form-nine-fields]) — line items stay read-only, editing
// them is not in scope ([D9]). Reuses ValidationView.tsx's field-label + `.pf-input`
// markup convention. Form state is seeded once from `inv` at mount ([A-l]-style: this
// card only mounts while `isFixable`, matching EntityFormModal's own once-per-open init)
// — diffEditInput always diffs against the current `inv` prop (fresh on every parent
// re-render), so a later edit's patch is computed against the latest saved content even
// though the form's own untouched fields were seeded once.
function InvoiceEditForm({
  ctx,
  base,
  invoiceId,
  inv,
  onSaved,
}: {
  ctx: PlatformCtx
  base: string
  invoiceId: string
  inv: InvoiceRecord
  onSaved: () => void
}) {
  const [form, setForm] = useState<EditFormState>(() => formFromInvoice(inv))
  const [submitting, setSubmitting] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  function updateField(field: EditFieldKey, value: string) {
    setForm((f) => ({ ...f, [field]: value }))
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting) return
    const patch = diffEditInput(inv, form)
    if (Object.keys(patch).length === 0) return // nothing changed — skip the PATCH, avoids the backend's all-nil 400
    setSubmitting(true)
    setFormError(null)
    try {
      await editInvoice(ctx.authedFetch, base, invoiceId, patch)
      onSaved()
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form data-testid="edit-invoice" onSubmit={handleSubmit} style={{ padding: 16 }}>
      {formError && (
        <div style={{ marginBottom: 12, padding: '10px 12px', borderRadius: 6, background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
          {formError}
        </div>
      )}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginBottom: 14 }}>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Issue date</div>
          <input className="pf-input" type="text" value={form.issue_date} onChange={(e) => updateField('issue_date', e.target.value)} placeholder="YYYY-MM-DD" style={{ fontFamily: 'var(--font-mono)' }} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Currency</div>
          <input className="pf-input" type="text" value={form.currency} onChange={(e) => updateField('currency', e.target.value)} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Supplier name</div>
          <input className="pf-input" type="text" value={form.supplier_name} onChange={(e) => updateField('supplier_name', e.target.value)} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Supplier TIN</div>
          <input className="pf-input" type="text" value={form.supplier_tin} onChange={(e) => updateField('supplier_tin', e.target.value)} placeholder="########-####" style={{ fontFamily: 'var(--font-mono)' }} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer name</div>
          <input className="pf-input" type="text" value={form.buyer_name} onChange={(e) => updateField('buyer_name', e.target.value)} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer TIN</div>
          <input className="pf-input" type="text" value={form.buyer_tin} onChange={(e) => updateField('buyer_tin', e.target.value)} placeholder="########-####" style={{ fontFamily: 'var(--font-mono)' }} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Subtotal</div>
          <input className="pf-input" type="text" value={form.subtotal} onChange={(e) => updateField('subtotal', e.target.value)} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>VAT</div>
          <input className="pf-input" type="text" value={form.vat} onChange={(e) => updateField('vat', e.target.value)} disabled={submitting} />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Total</div>
          <input className="pf-input" type="text" value={form.total} onChange={(e) => updateField('total', e.target.value)} disabled={submitting} />
        </div>
      </div>
      <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary pf-btn" style={{ height: 34, fontSize: 13 }}>
        {submitting ? 'Saving…' : 'Save changes'}
      </button>
    </form>
  )
}
