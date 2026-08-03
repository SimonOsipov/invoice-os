// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// jsdom 27.4.0 DOES implement URL.createObjectURL/revokeObjectURL (measured: a real call
// returns `blob:nodedata:…`), so the lifecycle specs spy and restore. Assigning and then
// `delete`-ing would strip a working global for the rest of the worker.
//
// jsdom normalises `background`: `oklch(28% .015 210)` reads back `oklch(0.28 0.015 210)`.
// `boxShadow`, `transform` and `width` round-trip raw.

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { StrictMode } from 'react'

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { SourceDocumentModal } from './SourceDocumentModal'
import { SourceDocumentImage, SourceDocumentPdf } from './SourceDocumentPages'
import type { SourceDocumentAsync } from './SourceDocumentStates'

const HASH = '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560'

const PDF_NOTE = 'The page of this document that became this invoice was not recorded.'
const PDF_STAMP = 'RENDERED FROM THE STORED PDF · NO EDITS POSSIBLE'
const IMAGE_NOTE = 'This photograph became this invoice.'

const RADIUS_FORCING = ['pf-btn', 'pf-chip', 'v2-btn', 'ops-btn', 'dev-btn', 'ops-chip', 'dev-chip']

function pdfRecord(over: Partial<SourceDocumentRecord> = {}): SourceDocumentRecord {
  return {
    id: 'doc-1',
    filename: 'june-invoices.pdf',
    declared_content_type: 'application/pdf',
    size_bytes: 624640,
    content_hash: HASH,
    uploaded_at: '2026-06-12T11:42:00Z',
    uploaded_by: 'c0000000-0000-0000-0000-000000000001',
    invoices_created: 1,
    other_invoice_rows: [],
    ...over,
  }
}

function response(over: Partial<SourceDocumentResponse> = {}): SourceDocumentResponse {
  return { invoice_id: 'inv-1', source_rows: null, document: pdfRecord(), ...over }
}

function metaAsync(over: Partial<SourceDocumentAsync> = {}): SourceDocumentAsync {
  return { status: 'ready', data: response(), error: null, run: vi.fn(), ...over }
}

// Typed against the real PlatformCtx so a rename breaks the typecheck; the cast stands in
// for the ~90 fields the modal never touches.
type ModalCtx = Pick<PlatformCtx, 'authedFetch' | 'getToken' | 'mode' | 'user'> & {
  active: Pick<PlatformCtx['active'], 'name'>
}

function modalCtx(): PlatformCtx {
  const ctx: ModalCtx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    getToken: () => 'tok',
    mode: 'firm',
    user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
    active: { name: 'Lagos Logistics Ltd' },
  }
  return ctx as unknown as PlatformCtx
}

function modalElement(meta: SourceDocumentAsync) {
  return (
    <SourceDocumentModal
      ctx={modalCtx()}
      meta={meta}
      invoiceNumber="INV-2026-0037"
      invoiceCreatedAt="2026-06-12T09:15:00Z"
      createdBy="c0000000-0000-0000-0000-000000000001"
      onClose={vi.fn()}
    />
  )
}

// `Blob.prototype.arrayBuffer` is undefined under this jsdom, but fetchDocumentBytes calls
// `res.arrayBuffer()` on the RESPONSE, which is entirely ours.
function bytesResponse() {
  return { ok: true, status: 200, arrayBuffer: () => Promise.resolve(new ArrayBuffer(8)) }
}

function mockBytesFetch() {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(bytesResponse())))
}

/** Holds the bytes request open so a handle can be made to land after unmount. */
function deferredBytesFetch(): () => void {
  let open!: () => void
  const gate = new Promise<void>((r) => {
    open = r
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      await gate
      return bytesResponse()
    }),
  )
  return open
}

/** Holds doc-1's bytes request open; any other document resolves at once. */
function gatedFetchForDoc1(): () => void {
  let open!: () => void
  const gate = new Promise<void>((r) => {
    open = r
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      if (url.includes('/documents/doc-1')) await gate
      return bytesResponse()
    }),
  )
  return open
}

let createSpy: MockInstance<typeof URL.createObjectURL>
let revokeSpy: MockInstance<typeof URL.revokeObjectURL>

function urls(): string[] {
  return createSpy.mock.results.map((r) => String(r.value))
}

function revoked(): string[] {
  return revokeSpy.mock.calls.map((c) => c[0])
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
  let n = 0
  createSpy = vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:${++n}`)
  revokeSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('SourceDocumentPdf', () => {
  it('the PDF canvas states the page was not recorded', () => {
    render(<SourceDocumentPdf url="blob:pdf-1" />)

    const toolbar = screen.getByTestId('pdf-toolbar')
    expect((toolbar.textContent ?? '').length).toBeGreaterThan(0) // floor: the toolbar rendered at all

    expect(toolbar.textContent).toContain(PDF_NOTE)
    expect(toolbar.textContent).toContain(PDF_STAMP)
  })

  it('the PDF canvas renders bytes and claims no page it cannot count', () => {
    render(<SourceDocumentPdf url="blob:pdf-1" />)

    const embed = screen.getByTestId('pdf-embed')
    const src = embed.getAttribute('src') ?? ''
    expect(src.length).toBeGreaterThan(0) // floor: an src really was written
    expect(src.startsWith('blob:')).toBe(true)
    expect(src).toBe('blob:pdf-1')
    expect(embed.getAttribute('type')).toBe('application/pdf')

    // Nothing enumerates pages, so nothing may claim one.
    const canvas = screen.getByTestId('source-document-pdf')
    expect(canvas.querySelectorAll('[data-page]').length).toBe(0)

    const text = canvas.textContent ?? ''
    expect(text.length).toBeGreaterThan(0) // floor: the negative assertions have real text to fail on
    expect(text).not.toContain('BECAME THIS INVOICE')
    expect(text).not.toContain('THIS INVOICE')
    expect(text).not.toContain('Jump to the page')
    expect(text).not.toMatch(/\d+\s+pages?\b/i)
    expect(text).not.toMatch(/page\s+\d+/i)
  })
})

describe('SourceDocumentImage', () => {
  it('the image canvas renders the photograph on the dark ground', () => {
    render(<SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />)

    const img = screen.getByTestId('source-image')
    expect(img.getAttribute('src')).toBe('blob:img-1')
    expect(img.getAttribute('alt')).toBe('receipt.jpg')
    expect(img.style.width).toBe('520px')
    expect(img.style.transform).toBe('rotate(-1.1deg)')
    expect(img.style.boxShadow.length).toBeGreaterThan(0)

    // jsdom rewrites the percentage and the leading dot: `oklch(28% .015 210)` reads back
    // as `oklch(0.28 0.015 210)`. Asserting the authored literal fails.
    expect(screen.getByTestId('image-ground').style.background).toBe('oklch(0.28 0.015 210)')

    expect(screen.getByTestId('image-toolbar').textContent).toContain(IMAGE_NOTE)
  })

  it('a null filename still names the image', () => {
    render(<SourceDocumentImage url="blob:img-1" filename={null} />)

    const alt = screen.getByTestId('source-image').getAttribute('alt') ?? ''
    expect(alt.length).toBeGreaterThan(0) // floor: an alt exists to be wrong
    expect(alt).toBe('Source document')
  })

  it('clicking a zoom chip resizes the photograph', () => {
    render(<SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />)

    const img = () => screen.getByTestId('source-image')
    const pressed = (id: string) => screen.getByTestId(id).getAttribute('aria-pressed')

    // Floor: the default is a real pressed state, not an absent attribute.
    expect(pressed('zoom-100')).toBe('true')
    expect(img().style.width).toBe('520px')

    fireEvent.click(screen.getByTestId('zoom-150'))
    expect(img().style.width).toBe('780px')
    expect(pressed('zoom-150')).toBe('true')
    expect(pressed('zoom-100')).toBe('false')

    fireEvent.click(screen.getByTestId('zoom-50'))
    expect(img().style.width).toBe('260px')
    expect(pressed('zoom-50')).toBe('true')
    expect(pressed('zoom-150')).toBe('false')

    // Zoom moves width, never the rotation.
    expect(img().style.transform).toBe('rotate(-1.1deg)')
  })
})

describe('both canvases', () => {
  it('no control in either canvas carries a radius-forcing class', () => {
    render(
      <>
        <SourceDocumentPdf url="blob:pdf-1" />
        <SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />
      </>,
    )

    const buttons = Array.from(document.querySelectorAll('button'))
    expect(buttons.length).toBeGreaterThanOrEqual(3) // floor: the zoom chips really are here
    expect(buttons.map((b) => b.getAttribute('data-testid'))).toEqual(['zoom-50', 'zoom-100', 'zoom-150'])

    // Every element, not only the buttons: the wrapper carries a radius too.
    const roots = [screen.getByTestId('source-document-pdf'), screen.getByTestId('source-document-image')]
    const all = roots.flatMap((r) => [r, ...Array.from(r.querySelectorAll('*'))])
    expect(all.length).toBeGreaterThan(6) // floor: both trees really were walked

    for (const el of all) {
      const classes = (el.getAttribute('class') ?? '').split(/\s+/)
      for (const forced of RADIUS_FORCING) {
        expect(classes).not.toContain(forced)
      }
    }
  })

  it('neither canvas offers a download', () => {
    render(
      <>
        <SourceDocumentPdf url="blob:pdf-1" />
        <SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />
      </>,
    )

    const roots = [screen.getByTestId('source-document-pdf'), screen.getByTestId('source-document-image')]
    for (const root of roots) {
      expect((root.textContent ?? '').length).toBeGreaterThan(0) // floor: there is copy to match against
      expect(root.textContent).not.toMatch(/download/i)
      expect(root.querySelectorAll('a').length).toBe(0)
      expect(root.querySelectorAll('[download]').length).toBe(0)
    }
  })

  it('StrictMode does not revoke the URL the canvas was handed', () => {
    render(
      <StrictMode>
        <SourceDocumentPdf url="blob:pdf-1" />
        <SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />
      </StrictMode>,
    )

    expect(revoked()).toEqual([])
    expect(screen.getByTestId('pdf-embed').getAttribute('src')).toBe('blob:pdf-1')
    expect(screen.getByTestId('source-image').getAttribute('src')).toBe('blob:img-1')

    // Floor: the stub is live wire, so the empty array above is a fact and not a dead spy.
    URL.revokeObjectURL('blob:probe')
    expect(revoked()).toEqual(['blob:probe'])
  })
})

describe('the shell owns the object URL', () => {
  it('the shell releases the object URL it created', async () => {
    mockBytesFetch()
    const { unmount } = render(modalElement(metaAsync()))

    const embed = await screen.findByTestId('pdf-embed')
    expect(createSpy).toHaveBeenCalled() // floor: a URL really was created
    expect(urls()).toEqual(['blob:1'])
    expect(embed.getAttribute('src')).toBe('blob:1')

    // Not revoked while it is the thing on screen.
    expect(revoked()).toEqual([])

    unmount()
    expect(revoked()).toEqual(['blob:1'])
  })

  it('a URL that arrives after the modal closes is released at once', async () => {
    const open = deferredBytesFetch()
    const { unmount } = render(modalElement(metaAsync()))

    await screen.findByTestId('source-document-loading') // floor: the request really is in flight
    expect(createSpy).not.toHaveBeenCalled()

    unmount()
    open()

    await waitFor(() => expect(revoked()).toEqual(['blob:1']))
    expect(createSpy).toHaveBeenCalledTimes(1)
  })

  it('a superseded handle is released, not leaked', async () => {
    mockBytesFetch()
    const { rerender } = render(modalElement(metaAsync()))

    expect((await screen.findByTestId('pdf-embed')).getAttribute('src')).toBe('blob:1')

    rerender(modalElement(metaAsync({ data: response({ document: pdfRecord({ id: 'doc-2' }) }) })))

    await waitFor(() => expect(screen.getByTestId('pdf-embed').getAttribute('src')).toBe('blob:2'))

    // Floor: two distinct handles really were created.
    expect(urls()).toEqual(['blob:1', 'blob:2'])
    expect(revoked()).toEqual(['blob:1'])
  })

  // The switch case of the same bug: doc-1's handle is born AFTER its dispatch was discarded
  // by `runId`, so nothing in `useAsync` can ever hand it back. Only the producer's own
  // registration keeps it reachable.
  it('a handle whose dispatch was discarded mid-switch is still released', async () => {
    const openDoc1 = gatedFetchForDoc1()
    const { rerender, unmount } = render(modalElement(metaAsync()))

    await screen.findByTestId('source-document-loading') // floor: doc-1 really is in flight
    expect(createSpy).not.toHaveBeenCalled()

    rerender(modalElement(metaAsync({ data: response({ document: pdfRecord({ id: 'doc-2' }) }) })))
    const live = (await screen.findByTestId('pdf-embed')).getAttribute('src') ?? ''
    expect(urls()).toEqual([live]) // floor: only doc-2's handle exists so far

    openDoc1()
    await waitFor(() => expect(urls().length).toBe(2))
    const orphan = urls().find((u) => u !== live) as string

    // Landing after its successor, it waits for the next handle change or unmount — bounded
    // to the modal's life, never the page's.
    unmount()
    expect(revoked()).toContain(orphan)
    expect(revoked()).toContain(live)
  })

  // The doubled mount effect fires while `created` is still empty and `bytes.data` is still
  // null, so nothing is revoked out from under the live canvas. Deleting the `disposed`
  // reset in the mount effect fails this spec.
  it('StrictMode does not revoke the shell-owned URL out from under the canvas', async () => {
    mockBytesFetch()
    const { unmount } = render(<StrictMode>{modalElement(metaAsync())}</StrictMode>)

    const embed = await screen.findByTestId('pdf-embed')
    const live = embed.getAttribute('src') ?? ''
    expect(live.startsWith('blob:')).toBe(true) // floor: a real handle reached the canvas
    expect(revoked()).not.toContain(live)

    unmount()
    expect(revoked()).toContain(live)
    // Every handle StrictMode's doubled run produced, released exactly once each.
    expect([...new Set(revoked())].sort()).toEqual([...new Set(urls())].sort())
  })
})

describe('design-system surface', () => {
  it('neither canvas names anything the design system lacks', () => {
    // cwd, not import.meta.url: under jsdom the latter is an http: URL and fileURLToPath throws.
    const src = readFileSync(path.join(process.cwd(), 'src/components/SourceDocumentPages.tsx'), 'utf8')
    expect(src.length).toBeGreaterThan(800) // floor: the file really was read

    for (const absent of ['--accent-tint', '--warn', 'revokeObjectURL', 'release(', 'console.error', 'data-page', 'setTimeout']) {
      expect(src).not.toContain(absent)
    }
  })
})
