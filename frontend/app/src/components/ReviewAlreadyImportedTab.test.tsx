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

import { alreadyImportedCsvAll, type AlreadyImportedRowAll } from '../lib/reviewBatch'
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

// --- QA adversarial coverage (task-406 verification), new tests only ---

describe('ReviewAlreadyImportedTab: QA -- zero rows', () => {
  it('AIMPTAB-QA-1: an empty channel renders a truthful zero state, no row controls', () => {
    render(<ReviewAlreadyImportedTab rows={[]} rowsTotal={0} batchIds={['b1']} onOpenInvoice={vi.fn()} />)

    expect(screen.getByText('0 rows were already in your ledger')).toBeTruthy()
    expect(screen.getByText('0 of 0 rows were already in your ledger.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'View invoice' })).toBeNull()
  })
})

describe('ReviewAlreadyImportedTab: QA -- scale (the real repro: 750 rows / 250 invoices)', () => {
  it('AIMPTAB-QA-2: per-row reason ids stay unique and counts stay consistent at 750 rows', () => {
    const rows: AlreadyImportedRowAll[] = []
    for (let i = 0; i < 500; i++) {
      rows.push({ file: `batch-${i % 5}.csv`, row: i + 1, invoiceId: `inv-${i % 250}` })
    }
    for (let i = 0; i < 250; i++) {
      rows.push({ file: `batch-${i % 5}.csv`, row: 500 + i + 1, invoiceId: null })
    }
    const onOpenInvoice = vi.fn()

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={900} batchIds={['b1']} onOpenInvoice={onOpenInvoice} />)

    expect(screen.getByText('750 rows were already in your ledger')).toBeTruthy()
    expect(screen.getByText('750 of 900 rows were already in your ledger.')).toBeTruthy()

    const buttons = screen.getAllByRole('button', { name: 'View invoice' }) as HTMLButtonElement[]
    expect(buttons).toHaveLength(750)

    const disabled = buttons.filter((b) => b.disabled)
    expect(disabled).toHaveLength(250) // the 250 unresolved rows, none dropped

    const reasonIds = disabled.map((b) => b.getAttribute('aria-describedby'))
    expect(reasonIds.every((id) => id != null)).toBe(true)
    expect(new Set(reasonIds).size).toBe(250) // all distinct at scale, not just at N=2 (AIMPTAB-6)
    for (const id of reasonIds) expect(document.getElementById(id as string)).not.toBeNull()

    // Spot-check routing still resolves the RIGHT id at the ends of a 750-row render.
    fireEvent.click(buttons[0])
    expect(onOpenInvoice).toHaveBeenLastCalledWith('inv-0')
    fireEvent.click(buttons[499]) // last resolved row
    expect(onOpenInvoice).toHaveBeenLastCalledWith('inv-249')
  })
})

describe('ReviewAlreadyImportedTab: QA -- mixed resolved/unresolved routing', () => {
  it('AIMPTAB-QA-3: resolved rows route their own id even with an unresolved row between them', () => {
    const rows: AlreadyImportedRowAll[] = [
      { file: 'a.csv', row: 1, invoiceId: 'inv-A' },
      { file: 'b.csv', row: 2, invoiceId: null },
      { file: 'c.csv', row: 3, invoiceId: 'inv-C' },
    ]
    const onOpenInvoice = vi.fn()

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={3} batchIds={['b1']} onOpenInvoice={onOpenInvoice} />)

    const buttons = screen.getAllByRole('button', { name: 'View invoice' }) as HTMLButtonElement[]
    expect(buttons).toHaveLength(3)
    expect(buttons[1].disabled).toBe(true)

    fireEvent.click(buttons[2])
    expect(onOpenInvoice).toHaveBeenCalledTimes(1)
    expect(onOpenInvoice).toHaveBeenLastCalledWith('inv-C')

    fireEvent.click(buttons[0])
    expect(onOpenInvoice).toHaveBeenCalledTimes(2)
    expect(onOpenInvoice).toHaveBeenLastCalledWith('inv-A')

    fireEvent.click(buttons[1]) // disabled -- must not add a third call
    expect(onOpenInvoice).toHaveBeenCalledTimes(2)
  })
})

describe('ReviewAlreadyImportedTab: QA -- per-row file label in a multi-file run', () => {
  it('AIMPTAB-QA-4: each row pairs its own file, row number and invoice id -- no cross-row bleed', () => {
    const rows: AlreadyImportedRowAll[] = [
      { file: 'june.csv', row: 5, invoiceId: 'inv-1' },
      { file: 'july.csv', row: 8, invoiceId: 'inv-2' },
      { file: 'source not recorded', row: null, invoiceId: 'inv-3' },
    ]
    const onOpenInvoice = vi.fn()

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={3} batchIds={['b1', 'b2']} onOpenInvoice={onOpenInvoice} />)

    for (const [file, rowLabel, invoiceId] of [
      ['june.csv', '5', 'inv-1'],
      ['july.csv', '8', 'inv-2'],
      ['source not recorded', '—', 'inv-3'],
    ] as const) {
      const fileCell = screen.getByText(file)
      const rowContainer = fileCell.parentElement as HTMLElement
      expect(rowContainer.textContent, `row for ${file} must show ${rowLabel}`).toContain(rowLabel)
      fireEvent.click(rowContainer.querySelector('button') as HTMLButtonElement)
      expect(onOpenInvoice).toHaveBeenLastCalledWith(invoiceId)
    }
  })
})

describe('ReviewAlreadyImportedTab: QA -- CSV download wiring', () => {
  it('AIMPTAB-QA-5: downloads the right filename and the exact CSV content for a mixed set', async () => {
    const rows: AlreadyImportedRowAll[] = [
      { file: 'june.csv', row: 5, invoiceId: 'inv-1' },
      { file: 'july.csv', row: null, invoiceId: null },
    ]
    let capturedBlob: Blob | null = null
    const createSpy = vi
      .spyOn(URL, 'createObjectURL')
      .mockImplementation((b) => {
        capturedBlob = b as Blob
        return 'blob:test-already-imported'
      })
    const revokeSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    let capturedHref = ''
    let capturedDownload = ''
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      capturedHref = this.href
      capturedDownload = this.download
    })

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={10} batchIds={['b1', 'b2']} onOpenInvoice={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Download this list (CSV)' }))

    expect(capturedDownload).toBe('already-imported-rows-b1-b2.csv') // own file, own name -- not the unreadable export's
    expect(capturedHref).toBe('blob:test-already-imported')
    expect(revokeSpy).toHaveBeenCalledWith('blob:test-already-imported')

    // readAsText silently eats a leading BOM (it's an encoding signature, not data) --
    // ArrayBuffer + raw bytes is the only way to prove the BOM is actually there.
    const buf = await new Promise<ArrayBuffer>((resolve, reject) => {
      const r = new FileReader()
      r.onload = () => resolve(r.result as ArrayBuffer)
      r.onerror = reject
      r.readAsArrayBuffer(capturedBlob as Blob)
    })
    const bytes = new Uint8Array(buf)
    expect(Array.from(bytes.slice(0, 3))).toEqual([0xef, 0xbb, 0xbf]) // UTF-8 BOM, byte-exact
    expect(new TextDecoder('utf-8').decode(bytes.slice(3))).toBe(alreadyImportedCsvAll(rows))

    createSpy.mockRestore()
    revokeSpy.mockRestore()
    clickSpy.mockRestore()
  })
})

describe('ReviewAlreadyImportedTab: QA -- keyboard / accessibility of the disabled control', () => {
  it('AIMPTAB-QA-6: a disabled row control cannot take focus, and its reason text matches aria-describedby', () => {
    const rows: AlreadyImportedRowAll[] = [{ file: 'june.csv', row: 5, invoiceId: null }]

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={1} batchIds={['b1']} onOpenInvoice={vi.fn()} />)

    const button = screen.getByRole('button', { name: 'View invoice' }) as HTMLButtonElement
    button.focus()
    expect(document.activeElement).not.toBe(button) // disabled -- genuinely out of the tab order

    const describedbyId = button.getAttribute('aria-describedby')
    expect(describedbyId).not.toBeNull()
    const reasonEl = document.getElementById(describedbyId as string)
    expect(reasonEl?.textContent).toBe('The matching invoice was not recorded for this row.')
    expect(button.getAttribute('title')).toBe('The matching invoice was not recorded for this row.')
  })
})
