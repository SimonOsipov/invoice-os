// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS } from '../auth'
import type { SourceDocumentRecord } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { SourceDocumentRail } from './SourceDocumentRail'

const HASH = '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560'
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

function record(over: Partial<SourceDocumentRecord> = {}): SourceDocumentRecord {
  return {
    id: 'doc-1',
    filename: 'june-sales.xlsx',
    declared_content_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    size_bytes: 624640,
    content_hash: HASH,
    uploaded_at: '2026-06-12T11:42:00Z',
    uploaded_by: APP_PERSONAS.firm.subject,
    invoices_created: 500,
    other_invoice_rows: [],
    ...over,
  }
}

// The rail reads three ctx fields, typed against the real PlatformCtx so a rename breaks
// the typecheck.
type RailCtx = Pick<PlatformCtx, 'mode' | 'user'> & { active: Pick<PlatformCtx['active'], 'name'> }

function railCtx(over: Partial<RailCtx> = {}): PlatformCtx {
  const ctx: RailCtx = {
    mode: 'firm',
    user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
    active: { name: 'Lagos Logistics Ltd' },
    ...over,
  }
  return ctx as unknown as PlatformCtx
}

function renderRail(over: { record?: SourceDocumentRecord | null; sheetRowsTotal?: number | null; ctx?: PlatformCtx } = {}) {
  return render(
    <SourceDocumentRail
      ctx={over.ctx ?? railCtx()}
      record={over.record === undefined ? record() : over.record}
      invoiceNumber="INV-2026-0037"
      sheetRowsTotal={over.sheetRowsTotal ?? null}
    />,
  )
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('SourceDocumentRail', () => {
  it('renders the SHA-256 as four 16-char lines', () => {
    renderRail()

    const lines = screen.getAllByTestId('hash-line')
    expect(lines.length).toBe(4)
    for (const line of lines) {
      expect(line.textContent ?? '').toMatch(/^[0-9a-f]{16}$/)
    }
    expect(lines.map((l) => l.textContent).join('')).toBe(HASH)
  })

  // Nothing recomputes SHA-256 in the browser, so the rail must never claim a match --
  // only that it wasn't checked this session. A fabricated "MATCHES" line is the failure
  // mode this pins against.
  it('the fingerprint status line never claims a verification this build cannot perform', () => {
    renderRail()

    const rail = screen.getByTestId('source-document-rail')
    expect(rail.textContent).toContain('NOT VERIFIED THIS SESSION')
    expect(rail.textContent).not.toMatch(/MATCHES/i)
    expect(rail.textContent).not.toMatch(/VERIFYING/i)
  })

  it('Copy flips to Copied and back', () => {
    vi.useFakeTimers()
    renderRail()

    const button = screen.getByTestId('copy-hash')
    expect(button.textContent).toContain('Copy')
    expect(button.textContent).not.toContain('Copied')

    fireEvent.click(button)
    expect(button.textContent).toContain('Copied')

    act(() => {
      vi.advanceTimersByTime(1800)
    })
    expect(button.textContent).toContain('Copy')
    expect(button.textContent).not.toContain('Copied')
  })

  it('uploaded_by renders a name, a raw subject, or Not recorded', () => {
    renderRail({ record: record({ uploaded_by: APP_PERSONAS.firm.subject }) })
    expect(screen.getByTestId('source-document-rail').textContent).toContain(
      `${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`,
    )
    cleanup()

    // An unknown subject renders raw and in mono -- never a fabricated identity. Asserted
    // as a well-formed uuid, not merely as "some text is present".
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    renderRail({ record: record({ uploaded_by: unknown }) })
    const rendered = screen.getByText(unknown)
    expect(rendered.textContent ?? '').toMatch(UUID)
    expect(rendered.className).toContain('mono')
    cleanup()

    renderRail({ record: record({ uploaded_by: null }) })
    expect(screen.getByTestId('source-document-rail').textContent).toContain('Not recorded')
  })

  it('the immutability note is last and names the scope owner', () => {
    renderRail({ ctx: railCtx({ mode: 'firm', active: { name: 'Lagos Logistics Ltd' } }) })

    const rail = screen.getByTestId('source-document-rail')
    const note = rail.lastElementChild
    expect(note).not.toBeNull()
    expect((note?.textContent ?? '').length).toBeGreaterThan(0) // vacuity floor
    expect(note?.textContent).toContain('cannot replace, rename or annotate a source document')
    // Non-modification only: a gated boot deletes this row on the four demo tenants
    // and re-inserts it under a new id, so no persistence claim holds for its readers.
    expect(note?.textContent).not.toContain('Stored once')
    expect(note?.textContent).not.toContain('delete')
    expect(note?.textContent).toContain('Lagos Logistics Ltd')
  })

  it('the no-source rail collapses to the dashed panel', () => {
    renderRail({ record: null })

    const rail = screen.getByTestId('source-document-rail')
    expect(rail.textContent).toContain('No file, no size, no fingerprint')
    expect(rail.textContent).toContain('the five stages INV-2026-0037 passes through')
    expect(rail.textContent).not.toContain('Original filename')
    expect(rail.textContent).not.toContain('File size')
    expect(rail.textContent).not.toContain('Uploaded by')
  })

  // `Pages`, `Dimensions` and `Rows read` are omitted rather than placeholdered: none is
  // derivable in this build, and a fabricated "Pages 3" on an evidence surface is worse
  // than an absent row.
  it('the rail omits facts this repo cannot derive', () => {
    const cases: Array<{ record: SourceDocumentRecord; absent: string }> = [
      { record: record({ filename: 'scan.pdf', declared_content_type: 'application/pdf' }), absent: 'Pages' },
      { record: record({ filename: 'ledger.dat', declared_content_type: null }), absent: 'Rows read' },
      { record: record({ filename: 'photo.jpg', declared_content_type: 'image/jpeg' }), absent: 'Dimensions' },
    ]

    for (const c of cases) {
      renderRail({ record: c.record })
      const rail = screen.getByTestId('source-document-rail')
      expect(rail.textContent).toContain('Original filename') // vacuity floor: the record block rendered
      expect(rail.textContent).not.toContain(c.absent)
      cleanup()
    }
  })
})

// The v2 look of the rail. jsdom reads inline styles; `0` in a shorthand
// reads back as `0px`.
/** The nearest ancestor of `el` (inclusive), below `root`, whose padding is `padding`. */
function sectionOf(root: HTMLElement, el: HTMLElement, padding: string): HTMLElement | null {
  for (let n: HTMLElement | null = el; n && n !== root; n = n.parentElement) if (n.style.padding === padding) return n
  return null
}

/** A text style as CSS would inherit it from inline styles, up to and including `stop`. */
function inherited(el: HTMLElement, stop: HTMLElement, prop: 'fontSize' | 'color' | 'lineHeight'): string {
  for (let n: HTMLElement | null = el; n; n = n.parentElement) {
    if (n.style[prop] !== '') return n.style[prop]
    if (n === stop) break
  }
  return ''
}

describe('SourceDocumentRail follows the prototype', () => {
  it('the three sections carry the prototype paddings and the divider', () => {
    renderRail({ sheetRowsTotal: 1479 })
    const rail = screen.getByTestId('source-document-rail')

    const fingerprint = sectionOf(rail, screen.getByTestId('copy-hash'), '18px 20px 22px')
    expect.soft(fingerprint, 'fingerprint section 18px 20px 22px').not.toBeNull()

    const heading = screen.getByText('Document record')
    const record = sectionOf(rail, heading, '16px 20px 20px')
    expect.soft(record, 'record section 16px 20px 20px').not.toBeNull()
    expect.soft(record?.style.borderTop, 'divider between fingerprint and record').toBe('1px solid var(--line-1)')

    const note = screen.getByText(/cannot replace, rename or annotate a source document/)
    const footer = sectionOf(rail, note, '16px 20px 24px')
    expect.soft(footer, 'footer section 16px 20px 24px').not.toBeNull()
    expect.soft(footer?.style.marginTop, 'footer pinned to the bottom of the scroll area').toBe('auto')
    expect.soft(footer?.style.background, 'footer has no fill').toBe('')
    expect.soft(footer?.style.gap).toBe('9px')
    expect.soft(footer?.style.borderTop, 'footer top border').toBe('1px solid var(--line-1)')

    // the footer is inside the scrolling area, not a fixed strip below it
    const scrolls = Array.from(rail.querySelectorAll<HTMLElement>('*')).find((el) => el.style.overflow === 'auto')
    expect(scrolls, 'control: the rail has a scroll area').toBeDefined()
    expect.soft(scrolls?.contains(note), 'the note scrolls with the record').toBe(true)
  })

  it('the fingerprint box follows the prototype', () => {
    renderRail()
    const rail = screen.getByTestId('source-document-rail')

    const copy = screen.getByTestId('copy-hash')
    expect(copy.textContent, 'control: the copy button is read').toContain('Copy')
    expect.soft(copy.className, 'Copy is the ghost button').toContain('v2-btn-ghost')
    expect.soft(copy.style.height).toBe('24px')
    expect.soft(copy.style.padding).toBe('0px 9px')
    expect.soft(copy.style.fontSize).toBe('11.5px')
    expect.soft(copy.style.flex, 'Copy does not shrink').toBe('0 0 auto')
    expect.soft(copy.style.gap, 'Copy has no glyph gap').toBe('')
    expect(copy.querySelector('svg'), 'Copy is text only').toBeNull()

    const header = copy.parentElement as HTMLElement
    expect.soft(header.style.padding).toBe('8px 12px')
    expect.soft(header.style.gap, 'header has no gap').toBe('')
    expect.soft(header.style.background).toBe('var(--bg-1)')
    expect.soft(header.style.borderBottom).toBe('1px solid var(--line-1)')
    const box = header.parentElement as HTMLElement
    expect(box.contains(screen.getAllByTestId('hash-line')[0]), 'control: the fingerprint box holds the hash').toBe(true)
    expect.soft(box.style.border, 'fingerprint box border').toBe('1px solid var(--line-2)')
    expect.soft(box.style.borderRadius).toBe('var(--radius-md)')
    expect.soft(box.style.overflow).toBe('hidden')
    const title = screen.getByText('Content fingerprint · SHA-256')
    expect.soft(title.className.split(/\s+/)).toContain('label')

    const lines = screen.getAllByTestId('hash-line')
    expect(lines).toHaveLength(4)
    for (const line of lines) {
      expect.soft(line.closest('.mono'), 'hash is mono').not.toBeNull()
      expect.soft(inherited(line, rail, 'fontSize'), 'hash size').toBe('11px')
      expect.soft(inherited(line, rail, 'color'), 'hash colour').toBe('var(--fg-1)')
      expect.soft(inherited(line, rail, 'lineHeight'), 'hash leading').toBe('1.65')
    }
    const hashBox = lines[0].parentElement as HTMLElement
    expect.soft(hashBox.style.padding).toBe('10px 12px')
    expect.soft(hashBox.style.background, 'no fill').toBe('')
  })

  it('NOT VERIFIED sits below the fingerprint box with the prototype dot', () => {
    renderRail()
    const text = screen.getByText('NOT VERIFIED THIS SESSION')
    const box = screen.getAllByTestId('hash-line')[0].closest<HTMLElement>('[style*="border"]')
    expect(box, 'control: the fingerprint box').not.toBeNull()
    expect.soft(box?.contains(text), 'rendered outside the fingerprint box').toBe(false)

    expect(text.className, 'pin').toContain('mono')
    expect.soft(text.style.fontSize).toBe('9.5px')
    expect.soft(text.style.fontWeight).toBe('700')
    expect.soft(text.style.letterSpacing).toBe('0.06em')
    expect(text.style.color, 'pin').toBe('var(--fg-3)')

    const row = text.parentElement as HTMLElement
    expect.soft(row.style.marginTop).toBe('10px')
    expect.soft(row.style.gap).toBe('8px')
    const dot = row.firstElementChild as HTMLElement
    expect(dot.style.width, 'control: the dot').toBe('6px')
    expect.soft(dot.style.borderRadius).toBe('50%')
    expect.soft(dot.style.background).toBe('var(--line-3)')

    const para = screen.getByText(/Recompute this hash on the original file/)
    expect.soft(para.style.fontSize).toBe('12px')
    expect.soft(para.style.color).toBe('var(--fg-3)')
    expect.soft(para.style.marginTop).toBe('12px')
  })

  it('the document record follows the prototype', () => {
    renderRail({ sheetRowsTotal: 1479 })

    const heading = screen.getByText('Document record')
    expect.soft(heading.className.split(/\s+/)).toContain('label')
    expect.soft(heading.style.marginBottom).toBe('12px')
    const list = screen.getByText('Original filename').parentElement?.parentElement as HTMLElement
    expect(list.children.length, 'control: the record rows share one list').toBeGreaterThanOrEqual(6)
    expect.soft(list.style.gap, 'gap between record rows').toBe('10px')
    expect.soft(list.style.flexDirection).toBe('column')

    for (const label of ['Original filename', 'File size', 'Uploaded', 'Uploaded by', 'Invoices created', 'Rows in file']) {
      const k = screen.getByText(label, { exact: true })
      const row = k.parentElement as HTMLElement
      const value = k.nextElementSibling as HTMLElement
      expect(value, `${label}: control: the value cell`).not.toBeNull()
      expect.soft(row.style.display, `${label}: row`).toBe('grid')
      expect.soft(row.style.gridTemplateColumns, `${label}: columns`).toBe('110px minmax(0, 1fr)')
      expect.soft(row.style.gap, `${label}: gap`).toBe('10px')
      expect.soft(k.className.split(/\s+/), `${label}: mixed-case label, not .label`).not.toContain('label')
      expect.soft(k.style.fontSize, `${label}: label size`).toBe('12px')
      expect.soft(k.style.color, `${label}: label colour`).toBe('var(--fg-3)')
      expect.soft(value.style.fontSize, `${label}: value size`).toBe('12.5px')
      expect.soft(value.style.color, `${label}: value colour`).toBe('var(--fg-1)')
      expect.soft(value.style.lineHeight, `${label}: value leading`).toBe('1.45')
    }
  })

  it('the footer shield and note follow the prototype', () => {
    renderRail()
    const note = screen.getByText(/cannot replace, rename or annotate a source document/)
    expect.soft(note.style.fontSize).toBe('12px')
    expect(note.style.color, 'pin').toBe('var(--fg-2)')
    expect.soft(note.style.lineHeight).toBe('1.55')
    const shield = note.previousElementSibling as HTMLElement
    expect(shield.querySelector('svg'), 'control: the shield glyph').not.toBeNull()
    expect.soft(shield.style.color).toBe('var(--fg-3)')
  })

  it('the no-source rail follows the prototype', () => {
    renderRail({ record: null })
    const rail = screen.getByTestId('source-document-rail')
    const text = screen.getByText(/No file, no size, no fingerprint/)

    const box = text.closest('[style*="dashed"]') as HTMLElement
    expect(box, 'control: the dashed box').not.toBeNull()
    expect(box.style.border, 'pin').toBe('1px dashed var(--line-3)')
    expect.soft(box.style.padding).toBe('14px')
    expect.soft(box.style.background).toBe('var(--bg-1)')
    expect.soft(inherited(text, rail, 'fontSize'), 'pin').toBe('12.5px')
    expect.soft(inherited(text, rail, 'color')).toBe('var(--fg-2)')
    expect.soft(inherited(text, rail, 'lineHeight')).toBe('1.55')
    expect.soft(sectionOf(rail, box, '18px 20px'), 'section padding 18px 20px').not.toBeNull()
  })

  it('the original filename value is sans', () => {
    renderRail({ record: record({ filename: 'june-sales.xlsx' }) })
    const value = screen.getByText('june-sales.xlsx', { selector: '[class]:not([data-testid]), span' })
    expect(value.className, 'pin').not.toContain('mono')
    expect(value.closest('.mono'), 'no mono ancestor in the record row').toBeNull()
    expect(value.style.fontFamily).toBe('')
  })

  it('the unresolved uploader id stays mono', () => {
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    renderRail({ record: record({ uploaded_by: unknown }) })
    expect(screen.getByText(unknown).className, 'pin').toContain('mono')
    cleanup()
    renderRail({ record: record({ uploaded_by: APP_PERSONAS.firm.subject }) })
    const name = screen.getByText(`${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`)
    expect(name.className, 'pin: a resolved name is not mono').not.toContain('mono')
  })
})
