// Invoice detail — fiscal record (IRN/CSID/QR after transmit), status pill, doc-type
// badge, line items + totals, validation panel, audit trail, and the View-XML / PDF /
// Transmit actions. Ported from Platform.dc.html ~L598-694 + the detail slice of
// renderVals() (~L1366-1395).

import { amount, fmt, fmtPlain } from '../lib/format'
import { statusStyle, defaultDraft } from '../lib/clients'
import { validate } from '../lib/validation'
import { fiscalRecord, Qr } from '../lib/qr'
import { detailTarget } from '../lib/importReport'
import { crossGlyph, docGlyph2, downloadGlyph, sendGlyph, shieldGlyph, tickGlyph13, warnGlyph } from '../glyphs'
import type { ReactNode } from 'react'
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
  // M4-09 replaces this branch with a real fetch; nothing else about the wiring changes.
  const target = detailTarget({ selectedId, importedInvoiceId: ctx.importedInvoiceId })
  if (target.kind === 'imported') {
    return (
      <div style={{ padding: '24px 36px 56px', maxWidth: 1080, margin: '0 auto' }}>
        <button onClick={() => ctx.nav('invoices')} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 32, padding: '0 12px', fontSize: 13, marginBottom: 18 }}>
          ← All invoices
        </button>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: '28px 24px' }}>
          <h1 style={{ fontSize: 17, fontWeight: 600, letterSpacing: '-0.01em', margin: '0 0 8px' }}>Imported invoice</h1>
          <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: '0 0 18px', lineHeight: 1.6, maxWidth: 560 }}>
            This invoice was created by the import and lives on the server. The detail view does not read live invoice data yet, so there is nothing to show here — it will not fall back to another invoice.
          </p>
          <div className="label" style={{ marginBottom: 4 }}>
            Invoice id
          </div>
          <div className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', wordBreak: 'break-all' }}>{target.invoiceId}</div>
        </div>
      </div>
    )
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
