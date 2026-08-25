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
