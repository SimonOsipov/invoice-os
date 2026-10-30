// The inline credit/debit note form: third body of the invoice detail's left card.

import { useRef, useState, type FormEvent } from 'react'

import { draftTotals } from '../lib/invoiceDraft'
import { watToday } from '../lib/dashboardHistory'
import type { InvoiceDetailRecord } from '../lib/invoices'
import {
  isDuplicateNumberError,
  issueNote,
  noteDraftLines,
  NOTE_COPY,
  noteLineError,
  suggestedNoteNumber,
  type NoteType,
} from '../lib/notes'
import type { PlatformCtx } from '../types'

// check, description, qty, unit, amount; the box scrolls sideways only below this floor.
export const NOTE_LINE_GRID = '28px minmax(120px, 1fr) 88px 110px 110px'

const INPUT = { height: 34, fontSize: 13, padding: '0 10px' } as const
const LINE_INPUT = { height: 30, fontSize: 13, fontFamily: 'var(--font-mono)', padding: '0 8px', width: '100%', minWidth: 0 } as const

interface Row {
  checked: boolean
  quantity: string
  unit_price: string
}

export function NoteForm({
  ctx,
  base,
  invoiceId,
  inv,
  documentType,
  noteCount,
  onCancel,
}: {
  ctx: PlatformCtx
  base: string
  invoiceId: string
  inv: InvoiceDetailRecord
  documentType: NoteType
  noteCount: number | null
  onCancel: () => void
}) {
  const lines = inv.line_items ?? []
  // null until typed: the suggestion shows once notes load and never overwrites typed text.
  const [typedNumber, setTypedNumber] = useState<string | null>(null)
  const [date, setDate] = useState(() => watToday(new Date()))
  const [rows, setRows] = useState<Row[]>(() =>
    lines.map((l) => ({ checked: false, quantity: l.quantity ?? '', unit_price: l.unit_price ?? '' })),
  )
  const [submitting, setSubmitting] = useState(false)
  const [numberError, setNumberError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const inFlight = useRef(false)

  const number =
    typedNumber ?? (noteCount === null ? '' : suggestedNoteNumber(documentType, inv.invoice_number, noteCount))

  function updateRow(idx: number, patch: Partial<Row>) {
    setRows((rs) => rs.map((r, i) => (i === idx ? { ...r, ...patch } : r)))
  }

  const rowError = (i: number): string | null =>
    noteLineError(documentType, 'quantity', rows[i].quantity, lines[i].quantity) ??
    noteLineError(documentType, 'unit_price', rows[i].unit_price, lines[i].unit_price)
  const errors = rows.map((_, i) => rowError(i))
  const picked = lines.flatMap((line, i) => (rows[i].checked ? [{ line, quantity: rows[i].quantity, unit_price: rows[i].unit_price }] : []))
  const pickedInvalid = rows.some((r, i) => r.checked && errors[i] !== null)
  // A ticked invalid row shows no figure: Number('') would otherwise count as a 0.00 line.
  const totals = pickedInvalid ? { subtotal: null, vat: null, total: null } : draftTotals(noteDraftLines(picked))
  const complete = number.trim() !== '' && date !== '' && picked.length > 0 && !pickedInvalid

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (inFlight.current || !complete) return
    inFlight.current = true
    setSubmitting(true)
    setNumberError(null)
    setFormError(null)
    try {
      const note = await issueNote(ctx.authedFetch, base, invoiceId, {
        document_type: documentType,
        invoice_number: number.trim(),
        issue_date: `${date}T00:00:00Z`,
        lines: picked.map(({ line, quantity, unit_price }) => ({ line_id: line.id, quantity, unit_price })),
      })
      ctx.openImportedInvoice(note.id)
    } catch (err) {
      inFlight.current = false
      setSubmitting(false)
      if (isDuplicateNumberError(err)) setNumberError(NOTE_COPY.numberTaken)
      else setFormError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    }
  }

  const rowStyle = { display: 'grid', gridTemplateColumns: NOTE_LINE_GRID, gap: 8, padding: '8px 12px', alignItems: 'center' } as const

  return (
    <form data-testid="note-form" data-hs-do-not-collect="true" onSubmit={handleSubmit}>
      <div style={{ padding: '22px 24px', display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ fontSize: 16, fontWeight: 700, letterSpacing: '-0.02em' }}>
          {documentType === 'credit_note' ? 'Issue credit note' : 'Issue debit note'}
        </div>
        {formError && (
          <div data-testid="note-form-error" style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
            {formError}
          </div>
        )}
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Note number</div>
            <input
              data-testid="note-number"
              className="pf-input"
              type="text"
              value={number}
              onChange={(e) => {
                setTypedNumber(e.target.value)
                setNumberError(null)
              }}
              style={{ ...INPUT, fontFamily: 'var(--font-mono)' }}
              disabled={submitting}
            />
            {numberError && (
              <div data-testid="note-number-error" style={{ fontSize: 12, color: 'var(--status-red-text)', marginTop: 5, lineHeight: 1.4 }}>
                {numberError}
              </div>
            )}
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Issue date</div>
            <input
              data-testid="note-issue-date"
              className="pf-input"
              type="date"
              value={date}
              onChange={(e) => setDate(e.target.value)}
              style={{ ...INPUT, fontFamily: 'var(--font-mono)' }}
              disabled={submitting}
            />
          </div>
        </div>

        <div>
          <div className="label" style={{ marginBottom: 5 }}>Lines</div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto' }}>
            <div style={{ minWidth: 'min-content' }}>
              <div style={{ ...rowStyle, background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
                <span />
                <span className="label">Description</span>
                <span className="label">Qty</span>
                <span className="label">Unit</span>
                <span className="label" style={{ textAlign: 'right' }}>Amount</span>
              </div>
              {lines.map((line, i) => (
                <div key={line.id} data-testid="note-line-row" style={{ borderBottom: '1px solid var(--line-1)' }}>
                  <div style={rowStyle}>
                    <input
                      data-testid="note-line-check"
                      type="checkbox"
                      aria-label={`Include line ${line.line_no}`}
                      checked={rows[i].checked}
                      onChange={(e) => updateRow(i, { checked: e.target.checked })}
                      disabled={submitting}
                    />
                    <span style={{ fontSize: 13, minWidth: 0, overflowWrap: 'anywhere' }}>{line.description ?? '—'}</span>
                    <input
                      data-testid="note-line-qty"
                      className="pf-input"
                      type="text"
                      inputMode="decimal"
                      aria-label={`Quantity, line ${line.line_no}`}
                      value={rows[i].quantity}
                      onChange={(e) => updateRow(i, { quantity: e.target.value })}
                      style={LINE_INPUT}
                      disabled={submitting}
                    />
                    <input
                      data-testid="note-line-price"
                      className="pf-input"
                      type="text"
                      inputMode="decimal"
                      aria-label={`Unit price, line ${line.line_no}`}
                      value={rows[i].unit_price}
                      onChange={(e) => updateRow(i, { unit_price: e.target.value })}
                      style={LINE_INPUT}
                      disabled={submitting}
                    />
                    <span data-testid="note-line-amount" className="money" style={{ fontSize: 13, textAlign: 'right' }}>
                      {errors[i] ? '—' : draftTotals(noteDraftLines([{ line, quantity: rows[i].quantity, unit_price: rows[i].unit_price }])).subtotal ?? '—'}
                    </span>
                  </div>
                  {rows[i].checked && errors[i] && (
                    <div data-testid="note-line-error" style={{ padding: '0 12px 8px', fontSize: 12, color: 'var(--status-red-text)', lineHeight: 1.4 }}>
                      {errors[i]}
                    </div>
                  )}
                </div>
              ))}
              <div style={{ padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 4 }}>
                {(
                  [
                    ['Subtotal', 'note-subtotal', totals.subtotal],
                    ['VAT', 'note-vat', totals.vat],
                    ['Total', 'note-total', totals.total],
                  ] as const
                ).map(([label, testid, value]) => (
                  <div key={testid} style={{ display: 'flex', justifyContent: 'space-between', gap: 16 }}>
                    <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>{label}</span>
                    <span data-testid={testid} className="money" style={{ fontSize: 13, fontWeight: 600 }}>{value ?? '—'}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, paddingTop: 14, borderTop: '1px solid var(--line-1)' }}>
          <button type="button" data-testid="note-form-cancel" onClick={onCancel} disabled={submitting} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34 }}>
            Cancel
          </button>
          <button
            type="submit"
            data-testid="note-form-submit"
            disabled={submitting || !complete}
            className="v2-btn v2-btn-primary pf-btn"
            style={{ height: 34, ...(submitting || !complete ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null) }}
          >
            {submitting ? 'Issuing…' : 'Issue note'}
          </button>
        </div>
      </div>
    </form>
  )
}
