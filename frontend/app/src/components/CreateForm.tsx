// Create flow · step "form" — the draft editor (doc type, buyer details, line items,
// summary with VAT/WHT). Ported from Platform.dc.html ~L469-528 + the draft/totals
// slices of renderVals() (~L1350-1355, 1386-1388).

import { DOC_TYPE_DEFS } from '../data'
import { amount, fmt } from '../lib/format'
import { plusGlyph, shieldGlyph, tickGlyph13 } from '../glyphs'
import type { PlatformCtx } from '../types'

const WHT_RE = /servic|consult|support|warehous|leasing/i

export function CreateForm({ ctx }: { ctx: PlatformCtx }) {
  const { active, draft, uploadFile } = ctx
  const hasUploadFile = !!uploadFile
  const selFileName = uploadFile === 'pdf' ? 'lagos-freight-INV-0482.pdf' : uploadFile === 'img' ? 'scan-invoice-0482.jpg' : ''

  const sub = amount(draft.items)
  const vat = sub * 0.075
  const whtServices = draft.items.filter((it) => WHT_RE.test(it.desc)).reduce((s, it) => s + it.qty * it.price, 0)
  const whtAmt = draft.wht ? whtServices * 0.05 : 0

  return (
    <div className="pf-create-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 320px', gap: 16, alignItems: 'start' }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
        {hasUploadFile && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '10px 20px', background: 'var(--accent-tint)', borderBottom: '1px solid var(--line-1)' }}>
            <span style={{ color: 'var(--accent)', display: 'inline-flex' }}>{tickGlyph13}</span>
            <span style={{ fontSize: 12.5, color: 'var(--accent)', fontWeight: 500 }}>Pre-filled from {selFileName} — review and edit below.</span>
          </div>
        )}
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span style={{ fontSize: 15, fontWeight: 600 }}>New invoice · {active.short}</span>
          <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>
            {draft.number}
          </span>
        </div>
        <div style={{ padding: 20 }}>
          <div className="label" style={{ marginBottom: 12 }}>
            Document type
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 22 }}>
            {DOC_TYPE_DEFS.map(([code, label, desc]) => {
              const a = (draft.docType || 'B2B') === code
              const color = a ? 'var(--accent)' : 'var(--fg-2)'
              return (
                <button
                  key={code}
                  onClick={() => ctx.updateDraft('docType', code as 'B2B' | 'B2G' | 'B2C')}
                  className="pf-btn"
                  style={{ display: 'flex', alignItems: 'center', gap: 11, width: '100%', textAlign: 'left', border: `1px solid ${a ? 'var(--accent)' : 'var(--line-2)'}`, background: a ? 'var(--accent-tint)' : 'var(--bg-1)', borderRadius: 8, padding: '11px 13px', cursor: 'pointer', fontFamily: 'var(--font-sans)' }}
                >
                  <span className="mono" style={{ flex: 'none', fontSize: 11, fontWeight: 700, color, border: `1px solid ${a ? 'var(--accent)' : 'var(--line-2)'}`, borderRadius: 4, padding: '2px 6px' }}>{code}</span>
                  <span style={{ flex: 'none', fontSize: 13, fontWeight: 600, color }}>{label}</span>
                  <span style={{ flex: 1, minWidth: 0, fontSize: 11.5, color: a ? 'var(--accent)' : 'var(--fg-3)', textAlign: 'right', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{desc}</span>
                </button>
              )
            })}
          </div>
          <div className="label" style={{ marginBottom: 12 }}>
            Buyer details
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 14 }}>
            <div>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer name</div>
              <input className="pf-input" value={draft.buyer} onChange={(e) => ctx.updateDraft('buyer', e.target.value)} />
            </div>
            <div>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer TIN</div>
              <input className="pf-input" value={draft.buyerTin} onChange={(e) => ctx.updateDraft('buyerTin', e.target.value)} placeholder="########-####" style={{ fontFamily: 'var(--font-mono)' }} />
            </div>
          </div>
          <div style={{ marginBottom: 20 }}>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Billing address</div>
            <input className="pf-input" value={draft.buyerAddress} onChange={(e) => ctx.updateDraft('buyerAddress', e.target.value)} placeholder="Required for MBS" />
          </div>
          <div className="label" style={{ marginBottom: 12 }}>
            Line items
          </div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 6, overflow: 'hidden', marginBottom: 14 }}>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 70px 120px 120px', gap: 10, padding: '9px 12px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
              <span className="label">Description</span>
              <span className="label" style={{ textAlign: 'right' }}>Qty</span>
              <span className="label" style={{ textAlign: 'right' }}>Unit ₦</span>
              <span className="label" style={{ textAlign: 'right' }}>Amount</span>
            </div>
            {draft.items.map((it, i) => (
              <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 70px 120px 120px', gap: 10, padding: '9px 12px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
                <span style={{ fontSize: 13, color: 'var(--fg-1)' }}>{it.desc}</span>
                <input className="pf-num" type="number" value={it.qty} onChange={(e) => ctx.updateItem(i, 'qty', e.target.value)} />
                <input className="pf-num" type="number" value={it.price} onChange={(e) => ctx.updateItem(i, 'price', e.target.value)} />
                <span className="money" style={{ fontSize: 13, textAlign: 'right', fontWeight: 600 }}>{fmt(it.qty * it.price)}</span>
              </div>
            ))}
          </div>
          <button className="pf-chip" style={{ height: 30, padding: '0 12px', borderRadius: 6, fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 500, border: '1px dashed var(--line-3)', background: 'transparent', color: 'var(--fg-2)', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Add line
          </button>
        </div>
      </div>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: 20, position: 'sticky', top: 0 }}>
        <div className="label" style={{ marginBottom: 16 }}>
          Summary
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 11, marginBottom: 16 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>Subtotal</span>
            <span className="money" style={{ fontSize: 13, fontWeight: 600 }}>{fmt(sub)}</span>
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>VAT · 7.5%</span>
            <span className="money" style={{ fontSize: 13, fontWeight: 600 }}>{fmt(vat)}</span>
          </div>
          {draft.wht && (
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>WHT · 5%</span>
              <span className="money" style={{ fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)' }}>{'−' + fmt(whtAmt)}</span>
            </div>
          )}
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', paddingTop: 14, borderTop: '1px solid var(--line-1)', marginBottom: 20 }}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>Total due</span>
          <span className="money" style={{ fontSize: 18, fontWeight: 700 }}>{fmt(sub + vat - whtAmt)}</span>
        </div>
        <button onClick={ctx.runValidation} className="v2-btn v2-btn-primary pf-btn" style={{ width: '100%', justifyContent: 'center', height: 42 }}>
          <span style={{ display: 'inline-flex' }}>{shieldGlyph}</span> Run validation
        </button>
        <p style={{ fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>16 checks against the Nigeria MBS rule pack.</p>
      </div>
    </div>
  )
}
