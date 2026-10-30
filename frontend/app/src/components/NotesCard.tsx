// The notes issued against an accepted invoice. Not a fetcher: InvoiceDetail owns the read.
import type { ReactNode } from 'react'

import { ErrorState, Loading, type AsyncState } from '@invoice-os/api-client'

import { fmt } from '../lib/format'
import { invoiceStatusStyle } from '../lib/invoices'
import { noteTypeBadge, signedNoteTotal, type NotesOnInvoice } from '../lib/notes'

export function NotesCard({
  run,
  onOpen,
}: {
  run: AsyncState<NotesOnInvoice> & { run: () => void }
  onOpen: (id: string) => void
}): ReactNode {
  const data = run.data
  let body: ReactNode
  if (run.error) {
    body = <ErrorState error={run.error} onRetry={run.run} />
  } else if (data == null) {
    body = <Loading label="Loading notes" />
  } else {
    body = (
      <>
        {data.notes.length === 0 ? (
          <div data-testid="notes-empty" style={{ color: 'var(--fg-3)' }}>No credit or debit notes yet.</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {data.notes.map((n) => {
              const st = invoiceStatusStyle(n.status)
              return (
                <div key={n.id} data-testid="note-row" style={{ display: 'grid', gridTemplateColumns: 'auto minmax(0, 1fr) auto auto', alignItems: 'center', gap: 8 }}>
                  <span data-testid="note-row-type" className="mono" style={{ fontSize: 10, fontWeight: 600, color: 'var(--fg-3)' }}>
                    {noteTypeBadge(n.document_type)?.label}
                  </span>
                  <button
                    type="button"
                    data-testid="note-row-open"
                    onClick={() => onOpen(n.id)}
                    title={n.invoice_number}
                    className="mono"
                    style={{ minWidth: 0, textAlign: 'left', background: 'none', border: 'none', padding: 0, cursor: 'pointer', color: 'var(--fg-1)', fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                  >
                    {n.invoice_number}
                  </button>
                  <span data-testid="note-row-status" style={{ flex: 'none', background: st.bg, border: `1px solid ${st.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 8px' }}>
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
                  </span>
                  <span data-testid="note-row-total" className="money" style={{ fontSize: 12, whiteSpace: 'nowrap' }}>
                    {signedNoteTotal(n.document_type, n.total)}
                  </span>
                </div>
              )
            })}
          </div>
        )}
        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 8, marginTop: 12 }}>
          <span className="label">Still creditable</span>
          <span data-testid="still-creditable" className="money" style={{ fontWeight: 600 }}>
            {data.still_creditable === null ? '—' : fmt(Number(data.still_creditable))}
          </span>
        </div>
      </>
    )
  }

  return (
    <div data-testid="notes-card" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
        <span className="card-title">Notes</span>
      </div>
      <div style={{ padding: '14px 18px', fontSize: 12.5, lineHeight: 1.5 }}>{body}</div>
    </div>
  )
}
