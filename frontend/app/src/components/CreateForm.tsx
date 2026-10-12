// Create flow · step "form" — the manual entry screen: invoice details, buyer details,
// line items, and the summary panel that FILES the invoice. Ported from
// Platform.dc.html ~L469-528 + the draft/totals slices of renderVals() (~L1350-1355,
// 1386-1388), then rebuilt by INVCR-01-03 into a real entry form.
//
// This is the only from-scratch creation path in production, and its primary now performs
// one real POST /v1/invoices (in carried mode, POST /v1/imports/document/invoice). Two
// things follow, and neither is cosmetic:
//
//  - NOTHING here may affirm a filing. There is no success banner, no tick, no optimistic
//    row: the affirmation is the real invoice detail screen rendering the server's own row,
//    reached only after the 201. `ctx.fileDraft` has no dep that could report success.
//  - The summary renders the exact strings that cross the wire: manual mode shows
//    `draftTotals` (the mapper's own math), carried mode the reading. There is no WHT row:
//    the filed `total` is subtotal + vat with no withholding deduction.
//
// Every field on this screen is editable and every one of them is transmitted. Fields with
// no column behind them (WHT, document type) were removed outright: a field whose value is
// silently discarded is the same lie as the mock approve it replaced. The buyer address is
// the structured street..LGA fields. `currency` is not a field here: NGN-only is the real
// currency-allowed rule parameter, so there is nothing to choose.
// Carried mode (ctx.handOffReading) is the exception to all of this: the server files the
// reading, so its values (currency included) are read-only and only the number is typed.

import { gatewayBase } from '@invoice-os/api-client'
import { useRef } from 'react'

import { plusGlyph, xSmallGlyph } from '../glyphs'
import { FILEABLE_TIN, findCustomerByTin } from '../lib/buyerDirectory'
import { fmt } from '../lib/format'
import {
  applyBuyerRecord,
  BUYER_TYPE_NOTE,
  buyerRequirement,
  draftLineTaxes,
  draftTotals,
  fileDraftGate,
  type BuyerFill,
} from '../lib/invoiceDraft'
import { labelOf, type InvoiceField } from '../lib/invoiceFields'
import type { Draft, DraftLineNRSKey, PlatformCtx } from '../types'
import { NrsFieldControl } from './NrsFieldControl'

const ROW_COLS = '1fr 70px 120px 130px 24px'

// A carried null stays value='' and shows the Amount cell's '—' only as a placeholder (EXTR27-C4, EXTR27-C6).
const dash = (v: string | null) => (v === null ? '—' : undefined)

type HeaderKey = Exclude<keyof Draft, 'items' | 'number' | 'date' | 'currency' | 'buyer' | 'buyerTin'>
const DETAIL_KEYS: HeaderKey[] = ['due_date', 'issue_time', 'tax_point_date', 'payment_status', 'tax_currency_code']
const SUPPLIER_KEYS: HeaderKey[] = ['supplier_email', 'supplier_telephone', 'supplier_street', 'supplier_city', 'supplier_postal_zone', 'supplier_country', 'supplier_state', 'supplier_lga']
const BUYER_KEYS: HeaderKey[] = ['buyer_email', 'buyer_telephone', 'buyer_street', 'buyer_city', 'buyer_postal_zone', 'buyer_country', 'buyer_state', 'buyer_lga']
const LINE_KEYS: DraftLineNRSKey[] = ['tax_category', 'tax_percent', 'hsn_code', 'isic_code', 'product_category', 'service_category', 'sellers_item_identification', 'price_unit']
const PLACEHOLDER: Partial<Record<HeaderKey, string>> = { due_date: 'YYYY-MM-DD', tax_point_date: 'YYYY-MM-DD', issue_time: 'HH:MM:SS' }
const MONO: Partial<Record<HeaderKey, true>> = { due_date: true, tax_point_date: true, issue_time: true }

const labelStyle = { fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 } as const

export function CreateForm({ ctx }: { ctx: PlatformCtx }) {
  const { active, activeEntity, draft, filing, filingError, handOffReading: reading } = ctx

  const totals = draftTotals(draft.items)
  const lineTaxes = draftLineTaxes(draft.items)
  const required = buyerRequirement(draft.invoice_kind)
  const isRequired = (key: string) => (required.keys as readonly string[]).includes(key) || undefined

  // Buyer lookup: the latest draft is read at answer time; only the newest blur may land.
  const draftRef = useRef(draft)
  draftRef.current = draft
  const lookupSeq = useRef(0)
  const lastTin = useRef('')
  const lastFill = useRef<BuyerFill>({})

  function lookupBuyer() {
    const tin = draft.buyerTin.trim()
    const base = gatewayBase()
    if (base == null || !FILEABLE_TIN.test(tin)) {
      lookupSeq.current += 1
      lastTin.current = ''
      return
    }
    if (tin === lastTin.current) return
    lastTin.current = tin
    const seq = ++lookupSeq.current
    findCustomerByTin(ctx.authedFetch, base, tin).then(
      (record) => {
        if (seq !== lookupSeq.current) return
        const current = draftRef.current
        const { draft: next, fill } = applyBuyerRecord(current, record, lastFill.current)
        lastFill.current = fill
        const patch: Partial<Omit<Draft, 'items'>> = {}
        for (const key of Object.keys(next) as (keyof Omit<Draft, 'items'>)[]) if (next[key] !== current[key]) patch[key] = next[key]
        ctx.updateDraftFields(patch)
      },
      () => {
        if (seq === lookupSeq.current) lastTin.current = ''
      },
    )
  }

  const headerField = (key: HeaderKey) => (
    <div key={key}>
      <div style={labelStyle}>{labelOf(key as InvoiceField['key'])}</div>
      <NrsFieldControl
        fieldKey={key}
        value={draft[key]}
        onChange={(v) => ctx.updateDraft(key, v)}
        authedFetch={ctx.authedFetch}
        placeholder={PLACEHOLDER[key]}
        aria-label={labelOf(key as InvoiceField['key'])}
        style={MONO[key] ? { fontFamily: 'var(--font-mono)' } : undefined}
        aria-required={isRequired(key)}
      />
    </div>
  )

  // The SAME pure gate App.tsx re-checks before firing the request, so the label and the
  // handler can never disagree about why filing is unavailable. Entity first (an in-house
  // workspace cannot resolve it from here at all), blank number second (the operator can).
  const gate = fileDraftGate(draft, activeEntity)
  // Never an armed control that swallows the click: when the gate fails the button is
  // genuinely disabled AND its label names the reason.
  const primary = filing
    ? { label: 'Filing…', cursor: 'progress' }
    : gate.canFile
      ? { label: 'File invoice', cursor: 'pointer' }
      : { label: gate.reason, cursor: 'not-allowed' }
  const primaryDim = filing || !gate.canFile
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
          <span className="card-title" style={{ fontSize: 15 }}>New invoice · {active.short}</span>
        </div>
        <div style={{ padding: 20 }}>
          {reading !== null && (
            <p style={{ fontSize: 12.5, color: 'var(--fg-2)', margin: '0 0 16px', lineHeight: 1.5 }}>
              Read from the document. Enter the invoice number to file it.
            </p>
          )}
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
              {reading === null ? (
                <input className="pf-input" value={draft.date} onChange={(e) => ctx.updateDraft('date', e.target.value)} placeholder="YYYY-MM-DD" style={{ fontFamily: 'var(--font-mono)' }} />
              ) : (
                <input data-testid="carried-issue_date" className="pf-input" value={reading.issue_date ?? ''} placeholder={dash(reading.issue_date)} readOnly aria-readonly="true" style={{ fontFamily: 'var(--font-mono)', color: 'var(--fg-3)' }} />
              )}
            </div>
            {reading !== null && (
              <div>
                <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Currency</div>
                <input data-testid="carried-currency" className="pf-input" value={reading.currency ?? ''} placeholder={dash(reading.currency)} readOnly aria-readonly="true" style={{ fontFamily: 'var(--font-mono)', color: 'var(--fg-3)' }} />
              </div>
            )}
            {reading === null && DETAIL_KEYS.map((key) => headerField(key))}
          </div>
          {reading === null && (
            <>
              <div className="label" style={{ marginBottom: 12 }}>
                Supplier details
              </div>
              <p style={{ fontSize: 12.5, color: 'var(--fg-2)', margin: '0 0 12px', lineHeight: 1.5 }}>Blank fields use {active.short}&rsquo;s details.</p>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 22 }}>{SUPPLIER_KEYS.map((key) => headerField(key))}</div>
            </>
          )}
          <div className="label" style={{ marginBottom: 12 }}>
            Buyer details
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 22 }}>
            {reading === null && (
              <div style={{ gridColumn: '1 / -1' }}>
                {headerField('invoice_kind')}
                <p data-testid="buyer-type-note" style={{ fontSize: 12.5, color: 'var(--fg-2)', margin: '8px 0 0', lineHeight: 1.5 }}>{BUYER_TYPE_NOTE[required.level]}</p>
              </div>
            )}
            <div>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer name</div>
              {reading === null ? (
                <input className="pf-input" value={draft.buyer} onChange={(e) => ctx.updateDraft('buyer', e.target.value)} aria-required={isRequired('buyer')} />
              ) : (
                <input data-testid="carried-buyer_name" className="pf-input" value={reading.buyer_name ?? ''} placeholder={dash(reading.buyer_name)} readOnly aria-readonly="true" style={{ color: 'var(--fg-3)' }} />
              )}
            </div>
            <div>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer TIN</div>
              {reading === null ? (
                <input className="pf-input" value={draft.buyerTin} onChange={(e) => ctx.updateDraft('buyerTin', e.target.value)} onBlur={lookupBuyer} placeholder="########-####" aria-required={isRequired('buyerTin')} style={{ fontFamily: 'var(--font-mono)' }} />
              ) : (
                <input data-testid="carried-buyer_tin" className="pf-input" value={reading.buyer_tin ?? ''} placeholder={dash(reading.buyer_tin)} readOnly aria-readonly="true" style={{ fontFamily: 'var(--font-mono)', color: 'var(--fg-3)' }} />
              )}
            </div>
            {reading === null && BUYER_KEYS.map((key) => headerField(key))}
          </div>
          <div className="label" style={{ marginBottom: 12 }}>
            Line items
          </div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', marginBottom: 14 }}>
            <div style={{ display: 'grid', gridTemplateColumns: ROW_COLS, gap: 10, padding: '9px 12px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
              <span className="label">Description</span>
              <span className="label" style={{ textAlign: 'right' }}>Qty</span>
              <span className="label" style={{ textAlign: 'right' }}>Unit ₦</span>
              <span className="label" style={{ textAlign: 'right' }}>Amount</span>
              <span />
            </div>
            {reading === null
              ? draft.items.map((it, i) => (
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
                        `.pf-btn` forces `border-radius` with `!important`, so no radius is set
                        here; an inline one would never apply. */}
                    <button
                      onClick={() => ctx.removeItem(i)}
                      disabled={!canRemoveLine}
                      aria-label={`Remove line ${i + 1}`}
                      className="pf-btn"
                      style={{ width: 24, height: 24, display: 'grid', placeItems: 'center', border: '1px solid var(--line-2)', background: 'transparent', color: 'var(--fg-3)', cursor: canRemoveLine ? 'pointer' : 'not-allowed', ...(canRemoveLine ? {} : { opacity: 0.45 }) }}
                    >
                      {xSmallGlyph}
                    </button>
                    <div data-testid="draft-line-nrs" style={{ gridColumn: '1 / -1', display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: 10 }}>
                      {LINE_KEYS.map((key) => (
                        <div key={key}>
                          <div style={labelStyle}>{labelOf(key)}</div>
                          <NrsFieldControl
                            fieldKey={key}
                            value={it[key]}
                            onChange={(v) => ctx.updateItemField(i, key, v)}
                            onPick={key === 'tax_category' ? (entry) => entry.percent !== null && ctx.updateItemField(i, 'tax_percent', entry.percent) : undefined}
                            authedFetch={ctx.authedFetch}
                            data-line-field={key}
                            aria-label={`${labelOf(key)} ${i + 1}`}
                          />
                        </div>
                      ))}
                      <div>
                        <div style={labelStyle}>{labelOf('base_quantity')}</div>
                        <input className="pf-input" value="1" readOnly aria-readonly="true" aria-label={`${labelOf('base_quantity')} ${i + 1}`} style={{ color: 'var(--fg-3)' }} />
                      </div>
                      <div>
                        <div style={labelStyle}>{labelOf('line_tax')}</div>
                        <input data-testid="draft-line-tax" className="pf-input" value={lineTaxes[i] ?? ''} placeholder="—" readOnly aria-readonly="true" aria-label={`${labelOf('line_tax')} ${i + 1}`} style={{ fontFamily: 'var(--font-mono)', color: 'var(--fg-3)' }} />
                      </div>
                    </div>
                  </div>
                ))
              : reading.line_items.length === 0
                ? <div style={{ padding: '9px 12px', fontSize: 13, color: 'var(--fg-3)' }}>No line items were read.</div>
                : reading.line_items.map((line, i) => (
                    <div key={i} data-testid="carried-line-row" style={{ display: 'grid', gridTemplateColumns: ROW_COLS, gap: 10, padding: '9px 12px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
                      <input className="pf-input" value={line.description ?? ''} placeholder={dash(line.description)} readOnly aria-readonly="true" style={{ color: 'var(--fg-3)' }} />
                      <input className="pf-num" type="text" value={line.quantity ?? ''} placeholder={dash(line.quantity)} readOnly aria-readonly="true" style={{ color: 'var(--fg-3)' }} />
                      <input className="pf-num" type="text" value={line.unit_price ?? ''} placeholder={dash(line.unit_price)} readOnly aria-readonly="true" style={{ color: 'var(--fg-3)' }} />
                      <span className="money" style={{ fontSize: 13, textAlign: 'right', fontWeight: 600 }}>
                        {line.line_total ?? '—'}
                        {line.line_tax !== null && <span style={{ display: 'block', fontSize: 11, color: 'var(--fg-3)', fontWeight: 400 }}>Tax {line.line_tax}</span>}
                      </span>
                      <span />
                    </div>
                  ))}
          </div>
          {reading === null && (
            <button onClick={ctx.addItem} className="pf-chip" style={{ height: 30, padding: '0 12px', fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 500, border: '1px dashed var(--line-3)', background: 'transparent', color: 'var(--fg-2)', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Add line
            </button>
          )}
        </div>
      </div>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 20, position: 'sticky', top: 0 }}>
        <div className="label" style={{ marginBottom: 16 }}>
          Summary
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 11, marginBottom: 16 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>Subtotal</span>
            {reading === null ? (
              <span data-testid="summary-subtotal" className="money" style={{ fontSize: 13, fontWeight: 600 }}>{totals.subtotal ?? '—'}</span>
            ) : (
              <span data-testid="carried-subtotal" className="money" style={{ fontSize: 13, fontWeight: 600 }}>{reading.subtotal ?? '—'}</span>
            )}
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>{reading === null ? totals.vatLabel : 'VAT'}</span>
            {reading === null ? (
              <span data-testid="summary-vat" className="money" style={{ fontSize: 13, fontWeight: 600 }}>{totals.vat ?? '—'}</span>
            ) : (
              <span data-testid="carried-vat" className="money" style={{ fontSize: 13, fontWeight: 600 }}>{reading.vat ?? '—'}</span>
            )}
          </div>
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', paddingTop: 14, borderTop: '1px solid var(--line-1)', marginBottom: 20 }}>
          <span style={{ fontSize: 14, fontWeight: 600 }}>Total due</span>
          {reading === null ? (
            <span data-testid="summary-total" className="money" style={{ fontSize: 18, fontWeight: 700 }}>{totals.total ?? '—'}</span>
          ) : (
            <span data-testid="carried-total" className="money" style={{ fontSize: 18, fontWeight: 700 }}>{reading.total ?? '—'}</span>
          )}
        </div>
        <button
          onClick={ctx.fileDraft}
          disabled={filing || !gate.canFile}
          className="v2-btn pf-btn"
          style={{ width: '100%', justifyContent: 'center', height: 42, gap: 8, background: 'var(--action)', color: 'var(--primary-foreground)', cursor: primary.cursor, ...(primaryDim ? { opacity: 0.45, filter: 'none' } : {}) }}
        >
          {/* Indeterminate only, borrowed from the map step's own in-flight idiom:
              everything after the request leaves (server insert, the row coming back) is
              unobservable, so there is no progress to report and no stage list to fake. */}
          {filing && <span style={{ width: 13, height: 13, borderRadius: '50%', border: '2px solid var(--line-2)', borderTopColor: 'var(--fg-4)', display: 'block', animation: 'spin 0.7s linear infinite' }} />}
          {primary.label}
        </button>
        {/* verbatim — including 409 `duplicate invoice number`,
            which the editable number field above makes resolvable without leaving. In
            carried mode the 409 is the register sentence. */}
        {filingError && (
          <p style={{ fontSize: 12, color: 'var(--status-red-text)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>{filingError.message}</p>
        )}
        {/* Replaces "16 checks against the Nigeria MBS rule pack." — POST /v1/invoices is
            wired straight to the store with NO rule engine behind it, so the row lands as a
            draft with zero violations and no rule-set version. Promising checks that do not
            run is the same class of lie as the mock verdict this screen replaced. The supply
            route does run the rules, so carried mode says so. */}
        <p style={{ fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>
          {reading === null ? (
            <>Filed as a draft under {active.short}. The rule engine&rsquo;s verdict comes from the server — run Re-validate on the invoice.</>
          ) : (
            <>Filed as a draft under {active.short}, then checked against the rules.</>
          )}
        </p>
      </div>
    </div>
  )
}
