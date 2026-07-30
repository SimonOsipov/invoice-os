// Create flow · step "form" — the manual entry screen: invoice details, buyer details,
// line items, and the summary panel that FILES the invoice. Ported from
// Platform.dc.html ~L469-528 + the draft/totals slices of renderVals() (~L1350-1355,
// 1386-1388), then rebuilt by INVCR-01-03 into a real entry form.
//
// This is the only from-scratch creation path in production, and its primary now performs
// one real POST /v1/invoices. Two things follow, and neither is cosmetic:
//
//  - NOTHING here may affirm a filing. There is no success banner, no tick, no optimistic
//    row: the affirmation is the real invoice detail screen rendering the server's own row,
//    reached only after the 201. `ctx.fileDraft` has no dep that could report success.
//  - The summary must render the number that actually crosses the wire. The mapper's
//    `total` is subtotal + vat with NO withholding deduction, so the old `sub + vat − wht`
//    row and its WHT line were removed rather than restyled — they showed a total the
//    invoice does not contain.
//
// Every field on this screen is editable and every one of them is transmitted. Fields with
// no column behind them (billing address, WHT, document type) were removed outright: a
// field whose value is silently discarded is the same lie as the mock approve it replaced.
// `currency` is the one deliberate exception to "editable" — NGN-only is the real
// currency-allowed rule parameter, so there is nothing to choose.

import { amount, fmt } from '../lib/format'
import { fileDraftGate } from '../lib/invoiceDraft'
import { plusGlyph, xSmallGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

const ROW_COLS = '1fr 70px 120px 120px 28px'

export function CreateForm({ ctx }: { ctx: PlatformCtx }) {
  const { active, activeEntity, draft, filing, filingError } = ctx

  const sub = amount(draft.items)
  const vat = sub * 0.075

  // The SAME pure gate App.tsx re-checks before firing the request, so the label and the
  // handler can never disagree about why filing is unavailable. Entity first (an in-house
  // workspace cannot resolve it from here at all), blank number second (the operator can).
  const gate = fileDraftGate(draft, activeEntity)
  // Never an armed control that swallows the click: when the gate fails the button is
  // genuinely disabled AND its label names the reason.
  const primary = filing
    ? { label: 'Filing…', bg: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'progress' }
    : gate.canFile
      ? { label: 'File invoice', bg: 'var(--action)', color: 'var(--text-on-dark)', cursor: 'pointer' }
      : { label: gate.reason, bg: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' }
  // One line remaining is the floor: draftToCreateRequest maps an empty item list to
  // `line_items: []` with every declared total null, which the server would accept — a
  // lineless invoice is not something this form should be able to file.
  const canRemoveLine = draft.items.length > 1

  return (
    <div className="pf-create-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 320px', gap: 16, alignItems: 'start' }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          {/* No mono echo of the invoice number here any more: it became an editable field
              below, and a second read-only copy of the same value drifts from it visually
              the moment the operator types. */}
          <span className="card-title">New invoice · {active.short}</span>
        </div>
        <div style={{ padding: 20 }}>
          <div className="label" style={{ marginBottom: 12 }}>
            Invoice details
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 22 }}>
            <div>
              {/* Editable because the (tenant_id, entity_id, invoice_number) UNIQUE index
                  makes a fixed number single-use: the second filing under a company 409s on
                  it forever. Never auto-uniquified — an invoice number is a fiscal
                  identifier the product does not guess. */}
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Invoice number</div>
              <input className="pf-input" value={draft.number} onChange={(e) => ctx.updateDraft('number', e.target.value)} placeholder="INV-0000-00000" style={{ fontFamily: 'var(--font-mono)' }} />
            </div>
            <div>
              {/* Plain text + YYYY-MM-DD, matching InvoiceDetail's own issue_date editor
                  rather than a native date picker, so the two surfaces edit the same field
                  the same way. The mapper turns this into RFC3339 (or null when blank). */}
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Issue date</div>
              <input className="pf-input" value={draft.date} onChange={(e) => ctx.updateDraft('date', e.target.value)} placeholder="YYYY-MM-DD" style={{ fontFamily: 'var(--font-mono)' }} />
            </div>
          </div>
          <div className="label" style={{ marginBottom: 12 }}>
            Buyer details
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 22 }}>
            <div>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer name</div>
              <input className="pf-input" value={draft.buyer} onChange={(e) => ctx.updateDraft('buyer', e.target.value)} />
            </div>
            <div>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer TIN</div>
              <input className="pf-input" value={draft.buyerTin} onChange={(e) => ctx.updateDraft('buyerTin', e.target.value)} placeholder="########-####" style={{ fontFamily: 'var(--font-mono)' }} />
            </div>
          </div>
          <div className="label" style={{ marginBottom: 12 }}>
            Line items
          </div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-input)', overflow: 'hidden', marginBottom: 14 }}>
            <div style={{ display: 'grid', gridTemplateColumns: ROW_COLS, gap: 10, padding: '9px 12px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
              <span className="label">Description</span>
              <span className="label" style={{ textAlign: 'right' }}>Qty</span>
              <span className="label" style={{ textAlign: 'right' }}>Unit ₦</span>
              <span className="label" style={{ textAlign: 'right' }}>Amount</span>
              <span />
            </div>
            {draft.items.map((it, i) => (
              <div key={i} style={{ display: 'grid', gridTemplateColumns: ROW_COLS, gap: 10, padding: '9px 12px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
                {/* Was a read-only <span>: a description is the one line-item field that is
                    recoverable NOWHERE else — the detail screen's 9 editable fields are all
                    header fields — so filing an invoice whose lines describe the wrong goods
                    was unfixable after the fact. */}
                <input className="pf-input" value={it.desc} onChange={(e) => ctx.updateItemDesc(i, e.target.value)} placeholder="Description" />
                <input className="pf-num" type="number" value={it.qty} onChange={(e) => ctx.updateItem(i, 'qty', e.target.value)} />
                <input className="pf-num" type="number" value={it.price} onChange={(e) => ctx.updateItem(i, 'price', e.target.value)} />
                <span className="money" style={{ fontSize: 13, textAlign: 'right', fontWeight: 600 }}>{fmt(it.qty * it.price)}</span>
                {/* No visible text, so it carries an aria-label — otherwise the row's only
                    destructive control is nameless to a screen reader and to Playwright.
                    Rendered round by .pf-btn's own `border-radius: var(--radius-pill)
                    !important`, so no radius is set here; an inline one would never apply. */}
                <button
                  onClick={() => ctx.removeItem(i)}
                  disabled={!canRemoveLine}
                  aria-label={`Remove line ${i + 1}`}
                  className="pf-btn"
                  style={{ width: 24, height: 24, display: 'grid', placeItems: 'center', border: '1px solid var(--line-2)', background: 'transparent', color: canRemoveLine ? 'var(--fg-3)' : 'var(--fg-4)', cursor: canRemoveLine ? 'pointer' : 'not-allowed' }}
                >
                  {xSmallGlyph}
                </button>
              </div>
            ))}
          </div>
          <button onClick={ctx.addItem} className="pf-chip" style={{ height: 30, padding: '0 12px', borderRadius: 'var(--radius-input)', fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 500, border: '1px dashed var(--line-3)', background: 'transparent', color: 'var(--fg-2)', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Add line
          </button>
        </div>
      </div>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 20, position: 'sticky', top: 0 }}>
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
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', paddingTop: 14, borderTop: '1px solid var(--line-1)', marginBottom: 20 }}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>Total due</span>
          <span className="money" style={{ fontSize: 18, fontWeight: 700 }}>{fmt(sub + vat)}</span>
        </div>
        <button
          onClick={ctx.fileDraft}
          disabled={filing || !gate.canFile}
          className="v2-btn pf-btn"
          style={{ width: '100%', justifyContent: 'center', height: 42, gap: 8, background: primary.bg, color: primary.color, cursor: primary.cursor }}
        >
          {/* Indeterminate only, borrowed from the map step's own in-flight idiom:
              everything after the request leaves (server insert, the row coming back) is
              unobservable, so there is no progress to report and no stage list to fake.
              Track is --line-2, not the map step's --bg-3: the in-flight button's own fill
              IS --bg-3, so that ring would be invisible against it. */}
          {filing && <span style={{ width: 13, height: 13, borderRadius: 99, border: '2px solid var(--line-2)', borderTopColor: 'var(--fg-4)', display: 'block', animation: 'spin 0.7s linear infinite' }} />}
          {primary.label}
        </button>
        {/* The server's own words, verbatim — including 409 `duplicate invoice number`,
            which the editable number field above makes resolvable without leaving. */}
        {filingError && (
          <p style={{ fontSize: 12, color: 'var(--status-red-text)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>{filingError.message}</p>
        )}
        {/* Replaces "16 checks against the Nigeria MBS rule pack." — POST /v1/invoices is
            wired straight to the store with NO rule engine behind it, so the row lands as a
            draft with zero violations and no rule-set version. Promising checks that do not
            run is the same class of lie as the mock verdict this screen replaced. */}
        <p style={{ fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>
          Filed as a draft under {active.short}. The rule engine&rsquo;s verdict comes from the server — run Re-validate on the invoice.
        </p>
      </div>
    </div>
  )
}
