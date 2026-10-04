// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// jsdom 27.4.0 DOES implement URL.createObjectURL/revokeObjectURL (measured: a real call
// returns `blob:nodedata:…`), so the lifecycle specs spy and restore. Assigning and then
// `delete`-ing would strip a working global for the rest of the worker.
//
// jsdom round-trips `boxShadow`, `transform` and `width` raw and normalises `0` to `0px` in
// shorthands (`padding: 0 11px` reads back `0px 11px`).

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
  return { invoice_id: 'inv-1', source_rows: null, header_row: null, document: pdfRecord(), ...over }
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

describe('SourceDocumentPdf toolbar', () => {
  it('the PDF toolbar follows the prototype', () => {
    render(<SourceDocumentPdf url="blob:pdf-1" />)

    const toolbar = screen.getByTestId('pdf-toolbar')
    expect(toolbar.textContent, 'control: the toolbar is read').toContain(PDF_STAMP)
    expect.soft(toolbar.style.padding).toBe('11px 16px')
    expect.soft(toolbar.style.gap).toBe('12px')
    expect(toolbar.style.background, 'pin').toBe('var(--bg-2)')
    expect(toolbar.style.borderBottom, 'pin').toBe('1px solid var(--line-1)')

    const note = Array.from(toolbar.children).find((c) => c.textContent === PDF_NOTE) as HTMLElement
    expect(note, 'control: the note span').toBeDefined()
    expect.soft(note.style.fontSize).toBe('12.5px')
    expect.soft(note.style.color).toBe('var(--fg-2)')

    const stamp = Array.from(toolbar.children).find((c) => c.textContent === PDF_STAMP) as HTMLElement
    expect(stamp.className, 'pin').toContain('mono')
    expect.soft(stamp.style.fontSize).toBe('10px')
    expect.soft(stamp.style.letterSpacing).toBe('0.06em')
    expect.soft(stamp.style.color).toBe('var(--fg-3)')

    // pin: a blob: PDF cannot be zoomed from here, so the PDF toolbar offers no control
    expect(toolbar.querySelectorAll('button').length).toBe(0)
  })
})

describe('SourceDocumentPdf ground', () => {
  it('the PDF ground is a padded --bg-3 wrapper around the embed', () => {
    const { container } = render(<SourceDocumentPdf url="blob:pdf-1" />)

    const embed = screen.getByTestId('pdf-embed')
    const canvas = screen.getByTestId('source-document-pdf')
    const ground = embed.parentElement as HTMLElement
    expect(ground, 'control: the embed sits in its own wrapper').not.toBe(canvas)
    expect(canvas.contains(ground), 'control: the wrapper is inside the canvas').toBe(true)
    expect.soft(ground.style.padding).toBe('22px')
    expect.soft(ground.style.background).toBe('var(--bg-3)')
    expect.soft(ground.style.display).toBe('flex')
    expect.soft(container.innerHTML, 'no oklch literal').not.toContain('oklch')
  })
})

describe('SourceDocumentImage', () => {
  it('the image canvas renders the photograph on the surface ground', () => {
    const { container } = render(<SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />)

    const img = screen.getByTestId('source-image')
    expect(img.getAttribute('src')).toBe('blob:img-1')
    expect(img.getAttribute('alt')).toBe('receipt.jpg')
    expect(img.style.width).toBe('520px')
    expect(img.style.transform).toBe('rotate(-1.1deg)')

    const ground = screen.getByTestId('image-ground')
    expect.soft(ground.style.background).toBe('var(--surface-2)')
    expect.soft(ground.style.padding).toBe('30px')
    expect(ground.style.overflow, 'pin: live zoom scrolls the ground').toBe('auto')
    expect.soft(ground.style.display, 'grid keeps a zoomed photo scrollable from its left edge').toBe('grid')
    expect.soft(ground.style.placeItems).toBe('center')
    expect.soft(img.style.boxShadow).toBe('var(--shadow-card)')
    expect.soft(container.innerHTML, 'no oklch literal').not.toContain('oklch')

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

describe('SourceDocumentImage toolbar', () => {
  it('zoom buttons are separate 4px mono chips', () => {
    render(<SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />)
    fireEvent.click(screen.getByTestId('zoom-150'))

    const buttons = ['zoom-50', 'zoom-100', 'zoom-150'].map((id) => screen.getByTestId(id))
    const toolbar = screen.getByTestId('image-toolbar')
    expect(screen.getByTestId('zoom-150').getAttribute('aria-pressed'), 'control: the click landed').toBe('true')

    for (const b of buttons) {
      const id = b.getAttribute('data-testid')
      expect.soft(b.parentElement, `${id}: no track around the buttons`).toBe(toolbar)
      expect.soft(b.style.borderRadius, `${id}: radius`).toBe('var(--radius-sm)')
      expect.soft(b.style.fontFamily, `${id}: font`).toBe('var(--font-mono)')
      expect.soft(b.style.fontSize, `${id}: size`).toBe('11px')
      expect.soft(b.style.fontWeight, `${id}: weight`).toBe('600')
      expect.soft(b.style.height, `${id}: height`).toBe('26px')
      expect.soft(b.style.padding, `${id}: padding`).toBe('0px 11px')
    }

    const active = screen.getByTestId('zoom-150')
    expect.soft(active.style.background).toBe('var(--fg-1)')
    expect.soft(active.style.color).toBe('var(--bg-2)')
    expect.soft(active.style.border).toContain('--fg-1')
    for (const id of ['zoom-50', 'zoom-100']) {
      const b = screen.getByTestId(id)
      expect.soft(b.style.background, `${id}: background`).toBe('transparent')
      expect.soft(b.style.color, `${id}: colour`).toBe('var(--fg-3)')
      expect.soft(b.style.border, `${id}: border`).toContain('--line-2')
    }
  })

  it('the image toolbar and caption follow the prototype', () => {
    render(<SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />)

    const toolbar = screen.getByTestId('image-toolbar')
    expect.soft(toolbar.style.padding).toBe('11px 16px')
    expect(toolbar.style.gap, 'pin').toBe('10px')
    expect(toolbar.style.background, 'pin').toBe('var(--bg-2)')

    const caption = Array.from(toolbar.children).find((c) => c.textContent === IMAGE_NOTE) as HTMLElement
    expect(caption, 'control: the caption span').toBeDefined()
    expect.soft(caption.style.marginLeft).toBe('6px')
    expect.soft(caption.style.fontSize).toBe('12.5px')
    expect.soft(caption.style.color).toBe('var(--fg-2)')
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

describe('QA adversarial coverage', () => {
  it('an empty url renders without throwing on either canvas', () => {
    // React special-cases an empty-string `src`: it never writes the DOM attribute (so the
    // browser cannot re-request the page), only the JS property -- measured on both
    // elements. `hasAttribute` is the honest check; `getAttribute` would read back `null`.
    render(<SourceDocumentPdf url="" />)
    const embed = screen.getByTestId('pdf-embed')
    expect(embed.hasAttribute('src')).toBe(false)
    expect((embed as unknown as { src: string }).src).toBe('')

    cleanup()
    render(<SourceDocumentImage url="" filename="receipt.jpg" />)
    const img = screen.getByTestId('source-image')
    expect(img.hasAttribute('src')).toBe(false)
    expect((img as unknown as { src: string }).src).toBe('')
  })

  // jsdom never decodes an <img src> -- naturalWidth stays 0 for a real blob: URL and for
  // garbage alike, so a real-decode assertion (the QR-code e2e's naturalWidth > 0) has
  // nothing to observe here. This surface has no e2e path at all (`[all-six-states]`: the
  // picker stays csv/xlsx), so the only honest check is that nothing here reacts to
  // load/error in the first place -- a url that never resolves cannot leave anything stuck.
  it('the image canvas has no onLoad/onError wiring to race an unresolved decode', () => {
    render(<SourceDocumentImage url="blob:never-resolves" filename="receipt.jpg" />)
    const img = screen.getByTestId('source-image')
    expect(img.getAttribute('src')).toBe('blob:never-resolves') // floor: the element rendered
    expect(img.getAttribute('onerror')).toBeNull()
    expect(img.getAttribute('onload')).toBeNull()
  })

  it('the photograph shadow is the card token and the tilt stays', () => {
    render(<SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />)
    const img = screen.getByTestId('source-image')
    expect.soft(img.style.boxShadow).toBe('var(--shadow-card)')
    expect(img.style.transform, 'pin').toBe('rotate(-1.1deg)')
  })

  it('a zoom round trip returns exactly to 100%', () => {
    render(<SourceDocumentImage url="blob:img-1" filename="receipt.jpg" />)
    const img = () => screen.getByTestId('source-image')
    const pressed = (id: string) => screen.getByTestId(id).getAttribute('aria-pressed')

    fireEvent.click(screen.getByTestId('zoom-150'))
    fireEvent.click(screen.getByTestId('zoom-50'))
    fireEvent.click(screen.getByTestId('zoom-100'))

    expect(img().style.width).toBe('520px') // floor: the round trip really moved and came back
    expect(pressed('zoom-100')).toBe('true')
    expect(pressed('zoom-150')).toBe('false')
    expect(pressed('zoom-50')).toBe('false')
  })
})

describe('the shell owns the object URL -- QA adversarial coverage', () => {
  it('a rapid open/close/open cycle leaks nothing across two full mounts', async () => {
    mockBytesFetch()
    const first = render(modalElement(metaAsync()))
    expect((await screen.findByTestId('pdf-embed')).getAttribute('src')).toBe('blob:1')

    first.unmount()
    expect(revoked()).toEqual(['blob:1'])

    const second = render(modalElement(metaAsync()))
    expect((await screen.findByTestId('pdf-embed')).getAttribute('src')).toBe('blob:2')
    expect(revoked()).toEqual(['blob:1']) // the fresh mount's own handle is still live

    second.unmount()
    expect(revoked()).toEqual(['blob:1', 'blob:2'])
  })

  it('switching document kind pdf -> image -> pdf releases each url and drops stale UI', async () => {
    mockBytesFetch()
    const { rerender, unmount } = render(modalElement(metaAsync()))

    expect((await screen.findByTestId('pdf-embed')).getAttribute('src')).toBe('blob:1')

    rerender(
      modalElement(
        metaAsync({
          data: response({ document: pdfRecord({ id: 'doc-2', filename: 'receipt.jpg', declared_content_type: 'image/jpeg' }) }),
        }),
      ),
    )
    const img = await screen.findByTestId('source-image')
    expect(img.getAttribute('src')).toBe('blob:2')
    expect(screen.getByTestId('zoom-100').getAttribute('aria-pressed')).toBe('true') // fresh mount, zoom reset
    expect(screen.queryByTestId('pdf-embed')).toBeNull()
    expect(revoked()).toEqual(['blob:1'])

    fireEvent.click(screen.getByTestId('zoom-150')) // proves the switch back below is a fresh instance, not reused

    rerender(modalElement(metaAsync({ data: response({ document: pdfRecord({ id: 'doc-3' }) }) })))
    const embed = await screen.findByTestId('pdf-embed')
    expect(embed.getAttribute('src')).toBe('blob:3')
    expect(screen.queryByTestId('source-image')).toBeNull()
    expect(revoked()).toEqual(['blob:1', 'blob:2'])

    unmount()
    expect(revoked()).toEqual(['blob:1', 'blob:2', 'blob:3'])
  })

  // A stronger sibling of "arrives after the modal closes": TWO requests are still
  // in flight -- a switch mid-flight, not just one -- when the modal closes, and both
  // resolve only afterward. `disposed.current` is checked per-handle in the producer, so
  // this is not a new code path, but the four-scenario list (open/close, switch mid-flight,
  // unmount mid-flight, straggler after its successor) never combines switch + unmount
  // with BOTH requests still outstanding, so it earns its own spec.
  it('two requests still pending at unmount are both released once they land', async () => {
    const openers: Record<string, () => void> = {}
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (url: string) =>
          new Promise((resolve) => {
            const id = /\/documents\/([^/]+)$/.exec(url)?.[1]
            if (id) openers[id] = () => resolve(bytesResponse())
          }),
      ),
    )

    const { rerender, unmount } = render(modalElement(metaAsync()))
    await screen.findByTestId('source-document-loading')

    rerender(modalElement(metaAsync({ data: response({ document: pdfRecord({ id: 'doc-2' }) }) })))
    await waitFor(() => expect(openers['doc-2']).toBeDefined())
    expect(createSpy).not.toHaveBeenCalled() // floor: both requests really are still pending

    unmount()
    openers['doc-1']()
    openers['doc-2']()

    await waitFor(() => expect(revoked().sort()).toEqual(['blob:1', 'blob:2']))
    expect(urls().sort()).toEqual(['blob:1', 'blob:2'])
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
