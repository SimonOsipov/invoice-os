// @vitest-environment jsdom
// RED specs (BUG-08-03, task-406, Mode A) -- transcribe the architect's Test Specs table
// before Mode B implements the body. ReviewAlreadyImportedTab.tsx is a deliberately empty
// shell right now (its own file header), so every spec below fails on an ASSERTION --
// element/text not found, or a source-scan string missing -- never on an import/compile
// error. AIMPTAB-3 and AIMPTAB-6 pair their negative checks with a positive one so an
// empty render cannot vacuously pass them.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { AlreadyImportedRowAll } from '../lib/reviewBatch'
import { ReviewAlreadyImportedTab } from './ReviewAlreadyImportedTab'

afterEach(cleanup)

const UNRESOLVED_REASON = 'The matching invoice was not recorded for this row.'

describe('ReviewAlreadyImportedTab: AC-5, the per-row route-out', () => {
  it('AIMPTAB-1: a resolved row opens the colliding invoice', () => {
    const rows: AlreadyImportedRowAll[] = [{ file: 'june.csv', row: 5, invoiceId: 'inv-1' }]
    const onOpenInvoice = vi.fn()

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={5} batchIds={['b1']} onOpenInvoice={onOpenInvoice} />)

    fireEvent.click(screen.getByRole('button', { name: 'View invoice' }))

    expect(onOpenInvoice).toHaveBeenCalledTimes(1)
    expect(onOpenInvoice).toHaveBeenCalledWith('inv-1')
  })

  it('AIMPTAB-2: an unresolved row disables the control with a visible reason, never hides it', () => {
    const rows: AlreadyImportedRowAll[] = [{ file: 'june.csv', row: 5, invoiceId: null }]
    const onOpenInvoice = vi.fn()

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={5} batchIds={['b1']} onOpenInvoice={onOpenInvoice} />)

    const button = screen.getByRole('button', { name: 'View invoice' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    expect(screen.getByText(UNRESOLVED_REASON)).toBeTruthy()

    fireEvent.click(button)
    expect(onOpenInvoice).not.toHaveBeenCalled()
  })
})

describe('ReviewAlreadyImportedTab: AC-2, neutral framing', () => {
  it('AIMPTAB-3: nothing on the tab tells the accountant to correct anything', () => {
    const rows: AlreadyImportedRowAll[] = [
      { file: 'june.csv', row: 5, invoiceId: 'inv-1' },
      { file: 'july.csv', row: 8, invoiceId: null },
    ]

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={10} batchIds={['b1']} onOpenInvoice={vi.fn()} />)

    // Positive assertion first -- an empty render must not vacuously pass the negative
    // checks below just because it renders no text at all.
    expect(screen.getByText('2 rows were already in your ledger')).toBeTruthy()

    const body = document.body.textContent ?? ''
    for (const phrase of ['correct the rows', 'could not read', 'no rule was ever run', 'Import a corrected file', 'unreadable']) {
      expect(body, `must not contain "${phrase}"`).not.toContain(phrase)
    }
  })
})

describe('ReviewAlreadyImportedTab: AC-3, null-row and filename-fallback rendering', () => {
  it('AIMPTAB-4: a null row renders an em dash and the filename fallback survives', () => {
    const rows: AlreadyImportedRowAll[] = [{ file: 'source not recorded', row: null, invoiceId: 'inv-2' }]

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={1} batchIds={['b1']} onOpenInvoice={vi.fn()} />)

    expect(screen.getByText('—')).toBeTruthy()
    expect(screen.getByText('source not recorded')).toBeTruthy()
    const body = document.body.textContent ?? ''
    expect(body).not.toContain('null')
    expect(body).not.toContain('undefined')
  })
})

describe('ReviewAlreadyImportedTab: AC-2 guard, the neutral token family', () => {
  it('AIMPTAB-5: the tab uses the canonical neutral family and no verdict colour', () => {
    // cwd, not import.meta.url: under jsdom the latter is an http: URL and fileURLToPath
    // throws (ReportsView.test.tsx:249,294 precedent).
    const src = readFileSync(path.join(process.cwd(), 'src/components/ReviewAlreadyImportedTab.tsx'), 'utf8')
    expect(src.length).toBeGreaterThan(200) // floor: the file really was read

    expect(src).not.toContain('status-amber')
    expect(src).not.toContain('status-red')
    expect(src).toContain('status-muted')
  })
})

describe('ReviewAlreadyImportedTab: [C3] N rows can be unresolved at once, so the reason id must be per-row', () => {
  it('AIMPTAB-6: two unresolved rows get distinct aria-describedby ids, each pointing at its own reason', () => {
    // A module-const id (ReviewRow.tsx's REVALIDATE_REASON_ID precedent) is safe there
    // only because at most one row is ever expanded at once -- this tab renders N rows
    // at once, so a constant id here would emit duplicate DOM ids across unresolved rows.
    const rows: AlreadyImportedRowAll[] = [
      { file: 'june.csv', row: 5, invoiceId: null },
      { file: 'july.csv', row: 8, invoiceId: null },
    ]

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={2} batchIds={['b1']} onOpenInvoice={vi.fn()} />)

    const buttons = screen.getAllByRole('button', { name: 'View invoice' })
    expect(buttons).toHaveLength(2)

    const ids = buttons.map((b) => b.getAttribute('aria-describedby'))
    expect(ids[0]).not.toBeNull()
    expect(ids[1]).not.toBeNull()
    expect(ids[0]).not.toBe(ids[1])

    for (const id of ids) {
      expect(document.getElementById(id as string), `#${id} must exist as the reason element`).not.toBeNull()
    }
  })
})
