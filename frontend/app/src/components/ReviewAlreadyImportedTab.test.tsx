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

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={5} batchIds={['b1']} onOpenInvoice={onOpenInvoice} unit="spreadsheet" />)

    fireEvent.click(screen.getByRole('button', { name: 'View invoice' }))

    expect(onOpenInvoice).toHaveBeenCalledTimes(1)
    expect(onOpenInvoice).toHaveBeenCalledWith('inv-1')
  })

  it('AIMPTAB-2: an unresolved row disables the control with a visible reason, never hides it', () => {
    const rows: AlreadyImportedRowAll[] = [{ file: 'june.csv', row: 5, invoiceId: null }]
    const onOpenInvoice = vi.fn()

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={5} batchIds={['b1']} onOpenInvoice={onOpenInvoice} unit="spreadsheet" />)

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

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={10} batchIds={['b1']} onOpenInvoice={vi.fn()} unit="spreadsheet" />)

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

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={1} batchIds={['b1']} onOpenInvoice={vi.fn()} unit="spreadsheet" />)

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

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={2} batchIds={['b1']} onOpenInvoice={vi.fn()} unit="spreadsheet" />)

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
    render(<ReviewAlreadyImportedTab rows={[]} rowsTotal={0} batchIds={['b1']} onOpenInvoice={vi.fn()} unit="spreadsheet" />)

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

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={900} batchIds={['b1']} onOpenInvoice={onOpenInvoice} unit="spreadsheet" />)

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
  }, 20000) // 750-row jsdom render is slow on a loaded CI runner, past vitest's 5s default
})

describe('ReviewAlreadyImportedTab: QA -- mixed resolved/unresolved routing', () => {
  it('AIMPTAB-QA-3: resolved rows route their own id even with an unresolved row between them', () => {
    const rows: AlreadyImportedRowAll[] = [
      { file: 'a.csv', row: 1, invoiceId: 'inv-A' },
      { file: 'b.csv', row: 2, invoiceId: null },
      { file: 'c.csv', row: 3, invoiceId: 'inv-C' },
    ]
    const onOpenInvoice = vi.fn()

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={3} batchIds={['b1']} onOpenInvoice={onOpenInvoice} unit="spreadsheet" />)

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

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={3} batchIds={['b1', 'b2']} onOpenInvoice={onOpenInvoice} unit="spreadsheet" />)

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

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={10} batchIds={['b1', 'b2']} onOpenInvoice={vi.fn()} unit="spreadsheet" />)
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
    expect(new TextDecoder('utf-8').decode(bytes.slice(3))).toBe(alreadyImportedCsvAll(rows, 'spreadsheet'))

    createSpy.mockRestore()
    revokeSpy.mockRestore()
    clickSpy.mockRestore()
  })
})

describe('ReviewAlreadyImportedTab: QA -- keyboard / accessibility of the disabled control', () => {
  it('AIMPTAB-QA-6: a disabled row control cannot take focus, and its reason text matches aria-describedby', () => {
    const rows: AlreadyImportedRowAll[] = [{ file: 'june.csv', row: 5, invoiceId: null }]

    render(<ReviewAlreadyImportedTab rows={rows} rowsTotal={1} batchIds={['b1']} onOpenInvoice={vi.fn()} unit="spreadsheet" />)

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

// --- EXTR-15-09 (task-835, Mode A / test-first): the unit branch on the two tabs --------
//
// RED reason on landing: `unit` is not a prop yet, so React drops it and both branches
// render today's spreadsheet copy. Every spec below fails on a WRONG RENDERED STRING, or
// on a wrong element count -- never on an import error or a missing component.
//
// The components are reached through a cast because their props types do not carry `unit`
// yet and an unknown JSX prop is a compile error, which would take this file's shipped
// AIMPTAB specs down with it. The cast target IS the props shape being demanded; SW-4
// (ReviewUnreadableTab.test.tsx) is what makes the prop REQUIRED, and that one is red
// under `typecheck`, not under vitest.
import type { ReviewUnit } from '../lib/reviewBatch'
import { ReviewUnreadableTab } from './ReviewUnreadableTab'

type WithUnit<C> = C extends (p: infer P) => infer R ? (p: P & { unit: ReviewUnit }) => R : never
const AlreadyImportedTab = ReviewAlreadyImportedTab as unknown as WithUnit<typeof ReviewAlreadyImportedTab>
const UnreadableTab = ReviewUnreadableTab as unknown as WithUnit<typeof ReviewUnreadableTab>

// The table is the header's own parent: header first, then one child per data row.
function headerOf(root: HTMLElement): HTMLElement {
  const header = root.querySelector('.label')
  expect(header, 'the tab rendered no `.label` header row').not.toBeNull()
  return header as HTMLElement
}

describe('EXTR-15-09 SW-5 (AC-5): the middle column header matches the cells beside it', () => {
  // D4, and the census rows U4/A5 are WRONG where they proposed `<span>Document</span>`.
  // A `Document` column would be empty on every line -- the three document-path RowError
  // constructions (internal/importer/document.go) all omit Row, so `row` is null and the
  // cell beside this header already renders an em dash. The header follows the cells.
  it('SW-5 (GREEN since EXTR-15-09): document shows an em dash over em-dash cells; spreadsheet shows Row over the number', () => {
    const { unmount } = render(
      <AlreadyImportedTab
        rows={[{ file: 'june.pdf', row: null, invoiceId: 'inv-1' }]}
        rowsTotal={1}
        batchIds={['b1']}
        onOpenInvoice={vi.fn()}
        unit="document"
      />,
    )
    const docHeader = headerOf(screen.getByTestId('review-already-imported-tab'))
    const docTable = docHeader.parentElement as HTMLElement
    expect(docHeader.children[1].textContent, 'the document header must be an em dash, never the word Document').toBe('—')
    expect(docTable.children[1].children[1].textContent, 'the cell beside it is already an em dash').toBe('—')
    unmount()

    render(
      <AlreadyImportedTab
        rows={[{ file: 'june.csv', row: 7, invoiceId: 'inv-1' }]}
        rowsTotal={1}
        batchIds={['b1']}
        onOpenInvoice={vi.fn()}
        unit="spreadsheet"
      />,
    )
    const ssHeader = headerOf(screen.getByTestId('review-already-imported-tab'))
    const ssTable = ssHeader.parentElement as HTMLElement
    expect(ssHeader.children[1].textContent, 'AC-2: the spreadsheet header is unchanged').toBe('Row')
    expect(ssTable.children[1].children[1].textContent).toBe('7')
  })
})

describe('EXTR-15-09 SW-6 (AC-6): the branch changes copy, never layout', () => {
  // Two halves, and the first is what stops the second passing vacuously: a `unit` prop
  // React silently drops renders two IDENTICAL trees, and identical trees trivially have
  // equal column counts. So the branches are required to DIFFER first.
  it('SW-6 (GREEN since EXTR-15-09): both branches differ in copy and declare the same number of header columns', () => {
    const renderTabs = (unit: ReviewUnit) => {
      const { container, unmount } = render(
        <>
          <AlreadyImportedTab rows={[{ file: 'f', row: null, invoiceId: 'inv-1' }]} rowsTotal={1} batchIds={['b1']} onOpenInvoice={vi.fn()} unit={unit} />
          <UnreadableTab rows={[{ file: 'f', row: null, column: 'issue_date', message: 'unreadable', documentId: null }]} rowsTotal={1} batchIds={['b1']} onImportCorrected={vi.fn()} unit={unit} />
        </>,
      )
      const labels = Array.from(container.querySelectorAll('.label')) as HTMLElement[]
      const shape = labels.map((l) => l.children.length)
      const middles = labels.map((l) => l.children[1]?.textContent ?? '')
      unmount()
      return { shape, middles }
    }

    const spreadsheet = renderTabs('spreadsheet')
    const document_ = renderTabs('document')

    // Discrimination control. Without this, a dropped prop passes the parity check below.
    expect(document_.middles, 'the two branches rendered the same header; the unit prop is not wired').not.toEqual(spreadsheet.middles)

    // AC-6: one grid child per column in BOTH arms. Today: 3 on the already-imported tab,
    // 4 on the unreadable tab.
    expect(spreadsheet.shape).toEqual([3, 4])
    expect(document_.shape).toEqual([3, 4])
  })

  it('SW-6 constants (GREEN on landing): the two grid literals and their use sites are untouched', () => {
    const already = readFileSync(path.join(process.cwd(), 'src/components/ReviewAlreadyImportedTab.tsx'), 'utf8')
    const unreadable = readFileSync(path.join(process.cwd(), 'src/components/ReviewUnreadableTab.tsx'), 'utf8')

    expect(already).toContain("const ALREADY_IMPORTED_GRID = '150px 90px 1fr'")
    expect(unreadable).toContain("const UNREADABLE_GRID = '150px 90px 170px 1fr'")

    // Exactly two use sites each -- the header row and the data row. A third, or a
    // unit-keyed second constant, is a layout branch and AC-6 forbids it.
    expect(already.split('gridTemplateColumns: ALREADY_IMPORTED_GRID').length - 1).toBe(2)
    expect(unreadable.split('gridTemplateColumns: UNREADABLE_GRID').length - 1).toBe(2)
    expect(already.split('gridTemplateColumns').length - 1).toBe(2)
    expect(unreadable.split('gridTemplateColumns').length - 1).toBe(2)
  })
})

describe('EXTR-15-09 SW-7 (AC-1): a duplicate is "already in the register" for a document', () => {
  // "your ledger" is the spreadsheet unit's phrase and stays byte-identical there (AC-2).
  // Asserted over rendered TEXT, not source, so a branch that shipped the register wording
  // into an unreachable arm cannot satisfy it.
  it('SW-7 (GREEN since EXTR-15-09): the document tab says register and never ledger; the spreadsheet tab says the reverse', () => {
    const textOf = (unit: ReviewUnit) => {
      const { unmount } = render(
        <AlreadyImportedTab
          rows={[{ file: 'f', row: null, invoiceId: 'inv-1' }]}
          rowsTotal={4}
          batchIds={['b1']}
          onOpenInvoice={vi.fn()}
          unit={unit}
        />,
      )
      const text = screen.getByTestId('review-already-imported-tab').textContent ?? ''
      unmount()
      return text
    }

    const documentText = textOf('document')
    const spreadsheetText = textOf('spreadsheet')

    // A3, A4, A6 and A7 all carry the phrase, so four is the floor, not one.
    expect(documentText.split('already in the register').length - 1, 'A3/A4/A6/A7 must all read "already in the register"').toBeGreaterThanOrEqual(4)
    expect(documentText).not.toContain('already in your ledger')

    // AC-2, the freeze: the spreadsheet arm is untouched. A4's spreadsheet sentence does
    // not carry the phrase at all ("These rows match invoices this workspace already
    // holds"), so its floor is three, not four.
    expect(spreadsheetText.split('already in your ledger').length - 1, 'A3/A6/A7 must still read "already in your ledger"').toBeGreaterThanOrEqual(3)
    expect(spreadsheetText).toContain('These rows match invoices this workspace already holds')
    expect(spreadsheetText).not.toContain('already in the register')
  })
})
