// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { NoteSummary, NotesOnInvoice } from '../lib/notes'
import { NotesCard } from './NotesCard'

afterEach(cleanup)

const note = (id: string, document_type: NoteSummary['document_type'], status: NoteSummary['status'], total: string | null): NoteSummary => ({
  id,
  document_type,
  invoice_number: `N-${id}`,
  status,
  total,
})

function renderCard(data: NotesOnInvoice, onOpen = vi.fn()) {
  const run = { status: 'ready', data, error: null, run: vi.fn() } as never
  render(<NotesCard run={run} onOpen={onOpen} />)
  return onOpen
}

describe('NotesCard', () => {
  it('each row shows the signed total and opens the note', () => {
    const onOpen = renderCard({
      notes: [note('a', 'credit_note', 'draft', '100.00'), note('b', 'debit_note', 'validated', '50.00'), note('c', 'credit_note', 'accepted', '25.00')],
      credited_so_far: '125.00',
      still_creditable: '275.00',
    })
    const rows = screen.getAllByTestId('note-row')
    expect(rows.map((r) => within(r).getByTestId('note-row-total').textContent)).toEqual(['-₦100', '₦50', '-₦25'])
    expect(rows.map((r) => within(r).getByTestId('note-row-open').textContent)).toEqual(['N-a', 'N-b', 'N-c'])
    expect(rows.map((r) => within(r).getByTestId('note-row-type').textContent)).toEqual(['CREDIT', 'DEBIT', 'CREDIT'])
    expect(rows.map((r) => within(r).getByTestId('note-row-status').textContent)).toEqual(['DRAFT', 'VALIDATED', 'ACCEPTED'])
    fireEvent.click(within(rows[1]!).getByTestId('note-row-open'))
    expect(onOpen).toHaveBeenCalledWith('b')
  })

  it('a note with no total shows a dash and a debit note is unsigned', () => {
    renderCard({ notes: [note('a', 'credit_note', 'draft', null), note('b', 'debit_note', 'accepted', '0.50')], credited_so_far: '0.00', still_creditable: '10.00' })
    const totals = screen.getAllByTestId('note-row-total').map((e) => e.textContent)
    expect(totals).toHaveLength(2)
    expect(totals[0]).toBe('—')
    expect(totals[1]).not.toMatch(/^-/)
  })

  it('shows loading, not the figure, until the read lands', () => {
    render(<NotesCard run={{ status: 'loading', data: null, error: null, run: vi.fn() } as never} onOpen={vi.fn()} />)
    expect(screen.getByTestId('notes-card')).toBeTruthy()
    expect(screen.queryByTestId('still-creditable')).toBeNull()
    expect(screen.queryByTestId('notes-empty')).toBeNull()
  })

  it('still creditable reads the wire', () => {
    renderCard({ notes: [note('a', 'credit_note', 'accepted', '100.00')], credited_so_far: '100.00', still_creditable: '400.00' })
    expect(screen.getByTestId('still-creditable').textContent).toBe('₦400')
    cleanup()

    renderCard({ notes: [], credited_so_far: '0.00', still_creditable: '1000.00' })
    expect(screen.getByTestId('notes-empty')).toBeTruthy()
    expect(screen.getByTestId('still-creditable').textContent).toBe('₦1,000')
    cleanup()

    renderCard({ notes: [], credited_so_far: '0.00', still_creditable: null })
    expect(screen.getByTestId('still-creditable').textContent).toBe('—')
  })
})
