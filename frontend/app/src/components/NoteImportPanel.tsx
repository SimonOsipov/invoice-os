// The review's ask for a held credit or debit note: pick the original, confirm the number, import.
// Pinned by NoteImportPanel.test.tsx.

import { useRef, useState, type CSSProperties } from 'react'

import { importDocument, type ImportReport, type NoteOriginals } from '../lib/importApi'
import type { ExtractionFieldState } from '../lib/extractionReview'
import { ENTITY_REQUIRED_REASON } from '../lib/invoiceDraft'
import { isNumberTakenError, NOTE_COPY, suggestedNoteNumber, type NoteType } from '../lib/notes'
import { toDateInputValue } from '../lib/format'
import type { PlatformCtx } from '../types'

// `.v2-btn-primary:hover` brightens with no `:disabled` guard, so a disabled button needs this spread.
export const SAVE_DISABLED: CSSProperties = {
  background: 'var(--bg-3)',
  color: 'var(--fg-4)',
  cursor: 'not-allowed',
  filter: 'none',
}

// The document-type strip's slot and treatment, wrapping so a long original never widens the screen.
const NOTE_PANEL: CSSProperties = {
  flex: 'none',
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: 10,
  minWidth: 0,
  padding: '9px 16px',
  background: 'var(--bg-3)',
  borderBottom: '1px solid var(--line-1)',
}

const ROW: CSSProperties = { flexBasis: '100%', fontSize: 12, lineHeight: 1.4 }
const INPUT: CSSProperties = { height: 34, fontSize: 13, padding: '0 10px', minWidth: 0, maxWidth: '100%' }
const BUTTON: CSSProperties = { flex: 'none' }

const EMPTY_SENTENCE =
  'This client has no accepted invoice with an IRN, so this note has nothing to attach to. If it is an invoice, import it as one.'
const PICK_FIRST = 'Choose the invoice this note belongs to.'
const NUMBER_FIRST = 'Enter a number for this note.'

function readValue(fields: ExtractionFieldState[], name: string): string {
  return fields.find((f) => f.name === name)?.value?.trim() ?? ''
}

export function NoteImportPanel({
  ctx,
  base,
  documentId,
  noteType,
  fields,
  originals,
  onFiled,
}: {
  ctx: PlatformCtx
  base: string
  documentId: string
  noteType: NoteType
  fields: ExtractionFieldState[]
  originals: NoteOriginals
  onFiled: () => void
}) {
  const rows = originals.originals
  const readOriginal = readValue(fields, 'original_invoice_number')
  const readNumber = readValue(fields, 'invoice_number')

  // null until the user picks or types: the read decides what shows, and a user choice wins over it.
  const [picked, setPicked] = useState<string | null>(null)
  const [typed, setTyped] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [numberError, setNumberError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [handOff, setHandOff] = useState(false)
  const inFlight = useRef(false)

  const matched = readOriginal === '' ? undefined : rows.find((o) => o.invoice_number === readOriginal)
  const chosenId = picked ?? matched?.id ?? ''
  const chosen = rows.find((o) => o.id === chosenId)
  const number =
    typed ?? (readNumber !== '' ? readNumber : chosen ? suggestedNoteNumber(noteType, chosen.invoice_number, chosen.note_count) : '')

  const importReason =
    rows.length === 0 ? EMPTY_SENTENCE : chosen === undefined ? PICK_FIRST : number.trim() === '' ? NUMBER_FIRST : null
  const importOff = busy || importReason !== null
  const blocked = ctx.activeEntity === null

  // A refused write throws; a quarantine comes back as a report with no invoice.
  async function send(body: Parameters<typeof importDocument>[2]) {
    if (inFlight.current) return
    inFlight.current = true
    setBusy(true)
    setNumberError(null)
    setError(null)
    setHandOff(false)
    try {
      const report: ImportReport = await importDocument(ctx.authedFetch, base, body)
      const first = report.errors[0]
      if (report.ready_invoices > 0 || first === undefined || first.rule_key !== undefined) {
        onFiled()
      } else {
        setError(first.message)
        setHandOff(first.field === 'invoice_number')
      }
    } catch (e) {
      if (isNumberTakenError(e)) setNumberError(NOTE_COPY.numberTaken)
      else setError(e instanceof Error ? e.message : 'Something went wrong. Please try again.')
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }

  const noteLabel = noteType === 'credit_note' ? 'credit note' : 'debit note'

  return (
    <div data-testid="extraction-note-panel" style={NOTE_PANEL}>
      <select
        data-testid="extraction-note-original"
        aria-label="Original invoice"
        className="pf-input"
        value={chosenId}
        disabled={rows.length === 0 || busy}
        onChange={(e) => setPicked(e.target.value)}
        style={{ ...INPUT, flex: '1 1 260px' }}
      >
        <option value="">Choose an invoice</option>
        {rows.map((o) => (
          <option key={o.id} value={o.id}>
            {[o.invoice_number, toDateInputValue(o.issue_date), `${o.currency ?? 'NGN'} ${o.total ?? '—'}`, o.buyer_name ?? '—'].join(' · ')}
          </option>
        ))}
      </select>
      <div style={{ flex: '0 1 200px', minWidth: 0 }}>
        <input
          data-testid="extraction-note-number"
          data-hs-do-not-collect="true"
          aria-label="Note number"
          className="pf-input"
          type="text"
          value={number}
          disabled={busy}
          onChange={(e) => {
            setTyped(e.target.value)
            setNumberError(null)
          }}
          style={{ ...INPUT, width: '100%', fontFamily: 'var(--font-mono)' }}
        />
        {numberError && (
          <div data-testid="extraction-note-number-error" style={{ fontSize: 12, color: 'var(--status-red-text)', marginTop: 5, lineHeight: 1.4 }}>
            {numberError}
          </div>
        )}
      </div>
      <button
        type="button"
        data-testid="extraction-note-import"
        className="v2-btn v2-btn-primary pf-btn"
        disabled={importOff}
        title={importReason ?? undefined}
        style={importOff ? { ...BUTTON, ...SAVE_DISABLED } : BUTTON}
        onClick={() =>
          chosen &&
          void send({
            entityId: originals.entity_id,
            documentId,
            documentType: noteType,
            originalInvoiceId: chosen.id,
            invoiceNumber: number.trim(),
          })
        }
      >
        Import as {noteLabel}
      </button>
      <button
        type="button"
        data-testid="extraction-note-as-invoice"
        className="v2-btn v2-btn-ghost pf-btn"
        disabled={busy}
        style={BUTTON}
        onClick={() => void send({ entityId: originals.entity_id, documentId, documentType: 'commercial_invoice' })}
      >
        This is an invoice
      </button>
      {rows.length === 0 && (
        <div data-testid="extraction-note-empty" style={{ ...ROW, color: 'var(--fg-2)' }}>
          {EMPTY_SENTENCE}
        </div>
      )}
      {error !== null && (
        <div data-testid="extraction-note-error" style={{ ...ROW, color: 'var(--status-red-text)' }}>
          {error}
        </div>
      )}
      {handOff && (
        <div style={{ ...ROW, display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap' }}>
          <button
            type="button"
            data-testid="extraction-note-enter-by-hand"
            className="v2-btn v2-btn-ghost pf-btn"
            disabled={blocked}
            title={blocked ? ENTITY_REQUIRED_REASON : undefined}
            style={{ height: 30, padding: '0 12px', fontSize: 12.5, flex: 'none', ...(blocked ? { background: 'transparent', opacity: 0.45, cursor: 'not-allowed' } : null) }}
            onClick={() => ctx.enterByHand(documentId)}
          >
            Enter it by hand
          </button>
          {blocked && <span style={{ fontSize: 12, color: 'var(--fg-3)' }}>{ENTITY_REQUIRED_REASON}</span>}
        </div>
      )}
    </div>
  )
}
