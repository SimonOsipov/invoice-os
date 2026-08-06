// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// RED specs (task-402, BUG-04-06, Mode A). Subject is the post-05 component, which still
// makes the transitional `getInvoice` call; against these text-only stubs that resolves to
// ApiError('malformed'), so every row below fails on its own assertion rather than on an
// import or a type error.
//
// jsdom 27.4.0 facts these specs depend on, measured in this worktree:
// - `Blob.prototype` is ['constructor','slice','size','type'] -- no `text()`. Bytes are
//   read back with FileReader.readAsText, which round-trips multibyte exactly.
// - `URL.createObjectURL`/`revokeObjectURL` are real: spy and restore, never
//   assign-and-delete (SourceDocumentPages.test.tsx:4-6).
// - A real `a.click()` prints "Not implemented: navigation to another Document". Spying
//   `HTMLAnchorElement.prototype.click` silences it AND captures `this.href`/`this.download`.

import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { PlatformCtx } from '../types'
import { XmlModal } from './XmlModal'

const BASE = 'https://gw'
const ID = 'inv-ubl-1'
const NUMBER = 'INV-2026-0001'
const UBL_URL = `${BASE}/api/invoice/v1/invoices/${ID}/ubl`

// Declaration, newlines, two `cbc:` nesting levels and a multibyte run -- so "verbatim"
// and "byte-identical" are claims a UTF-8 slip could actually fail.
const DOC = [
  '<?xml version="1.0" encoding="UTF-8"?>',
  '<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2">',
  '  <cbc:ID>INV-2026-0001</cbc:ID>',
  '  <cbc:IssueDate>2026-07-01</cbc:IssueDate>',
  '  <cac:AccountingSupplierParty>',
  '    <cbc:Name>Ọlá Holdings — Lagos</cbc:Name>',
  '  </cac:AccountingSupplierParty>',
  '  <cac:LegalMonetaryTotal>',
  '    <cbc:PayableAmount currencyID="NGN">1075.00</cbc:PayableAmount>',
  '  </cac:LegalMonetaryTotal>',
  '</Invoice>',
  '',
].join('\n')

// The server's own refusal sentence, em dash U+2014 intact (ubl.go's ublGate).
const REASON =
  'This invoice cannot be rendered as a UBL document — it is missing at least one line item.'
const LOAD_FAILED = 'This document could not be loaded.'
const NOT_FOUND_TITLE = 'Invoice not found'
const NOT_FOUND_MESSAGE = 'This invoice could not be loaded.'
const PROVENANCE =
  'Rendered from the stored invoice record when you opened this view. It is not a copy of what was transmitted to the access point.'

function modalCtx(): PlatformCtx {
  const ctx: Pick<PlatformCtx, 'authedFetch'> = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
  }
  return ctx as unknown as PlatformCtx
}

// URL-agnostic on purpose: the route the viewer asks for is what V2 asserts, so the stub
// must not be the thing that enforces it.
function stubOk(body: string) {
  const mock = vi.fn((_url: string, _init?: RequestInit) =>
    Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(body) }),
  )
  vi.stubGlobal('fetch', mock)
  return mock
}

function stubFail(status: number, error: string) {
  const mock = vi.fn((_url: string, _init?: RequestInit) =>
    Promise.resolve({ ok: false, status, json: () => Promise.resolve({ error }) }),
  )
  vi.stubGlobal('fetch', mock)
  return mock
}

function stubPending() {
  const mock = vi.fn((_url: string, _init?: RequestInit) => new Promise<never>(() => {}))
  vi.stubGlobal('fetch', mock)
  return mock
}

function renderModal(over: { invoiceNumber?: string; onClose?: () => void } = {}) {
  const onClose = over.onClose ?? vi.fn()
  const utils = render(
    <XmlModal
      ctx={modalCtx()}
      base={BASE}
      invoiceId={ID}
      invoiceNumber={over.invoiceNumber ?? NUMBER}
      onClose={onClose}
    />,
  )
  return { ...utils, onClose }
}

async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function readBlob(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader()
    fr.onload = () => resolve(String(fr.result))
    fr.onerror = () => reject(fr.error)
    fr.readAsText(blob)
  })
}

function panelOf(): HTMLElement {
  return screen.getByTestId('ubl-modal').firstElementChild as HTMLElement
}

function downloadByName(): HTMLElement | null {
  return screen.queryByRole('button', { name: /download/i })
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', BASE)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('XmlModal body -- the server document (task-402)', () => {
  it('V1/AC1: renders the server bytes verbatim', async () => {
    stubOk(DOC)
    renderModal()
    await settle()

    const pre = screen.queryByTestId('ubl-xml')
    expect(pre, 'the loaded document renders in a ubl-xml <pre>').not.toBeNull()
    expect(pre?.textContent).toBe(DOC)
  })

  it('V2/AC2: issues exactly one request, to the ubl route', async () => {
    const mock = stubOk(DOC)
    renderModal()
    await settle()

    expect(mock).toHaveBeenCalledTimes(1)
    expect(String(mock.mock.calls[0][0])).toBe(UBL_URL)
  })
})

describe('XmlModal failure branches -- keyed on error.status (task-402)', () => {
  it('V3/AC9: a 409 shows the server refusal verbatim and offers no retry', async () => {
    stubFail(409, REASON)
    renderModal()
    await settle()

    // Vacuous on its own -- today's ErrorState prints error.message too. The absent Retry
    // is the discriminator, and is also the correct product behaviour: a content-derived
    // refusal can only 409 again.
    expect(screen.queryByText(REASON), 'the blocked reason renders byte-identically').not.toBeNull()
    expect(screen.queryByRole('button', { name: /retry/i }), 'a 409 offers no Retry').toBeNull()
    expect(screen.queryByText('Something went wrong'), 'a 409 is a refusal, not an error card').toBeNull()
    expect(downloadByName(), 'nothing to download on a refusal').toBeNull()
  })

  it('V4/AC9: a 404 shows the not-found copy and never the raw server message', async () => {
    stubFail(404, 'not found')
    renderModal()
    await settle()

    expect(screen.queryByText(NOT_FOUND_TITLE), 'the shipped not-found title renders').not.toBeNull()
    expect(screen.queryByText(NOT_FOUND_MESSAGE), 'the shipped not-found message renders').not.toBeNull()
    expect(screen.queryByText(/^not found$/), 'the wire sentinel must not surface').toBeNull()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
  })

  it('V5/AC9: a 400 never leaks the validation sentinel and still offers retry', async () => {
    stubFail(400, 'invoice: validation')
    renderModal()
    await settle()

    expect(screen.queryByText(/invoice: validation/), 'the internal sentinel must not surface').toBeNull()
    expect(screen.queryByText(LOAD_FAILED), 'the fixed client sentence renders instead').not.toBeNull()
    expect(screen.queryByRole('button', { name: /retry/i }), 'a transient failure is retryable').not.toBeNull()
  })

  it('V6/AC9+AC1: a 500 leaks nothing and assembles no fallback document', async () => {
    stubFail(500, 'internal server error')
    renderModal()
    await settle()

    expect(screen.queryByText(/internal server error/i), 'the internal sentinel must not surface').toBeNull()
    expect(screen.queryByText(LOAD_FAILED)).not.toBeNull()
    expect(screen.queryAllByTestId('ubl-xml')).toHaveLength(0)

    const text = screen.getByTestId('ubl-modal').textContent ?? ''
    expect(text.length, 'floor: the modal really rendered').toBeGreaterThan(0)
    expect(text, 'no client-assembled document on a failure').not.toContain('<Invoice')
    expect(text).not.toContain('cbc:')
  })
})

describe('XmlModal download (task-402)', () => {
  it('V7/AC3: no download control while the document is loading', async () => {
    stubPending()
    renderModal()
    await settle()

    expect(screen.queryByText('Loading document…'), 'floor: this really is the loading window').not.toBeNull()
    expect(screen.queryAllByTestId('download-ubl')).toHaveLength(0)
    // By accessible name, not testid: the shipped dead button carries no testid, so a
    // testid-only assertion passes vacuously.
    expect(downloadByName(), 'no download affordance before there are bytes').toBeNull()
  })

  it('V8/AC3: no download control on a failure', async () => {
    stubFail(500, 'internal server error')
    renderModal()
    await settle()

    expect(screen.queryAllByTestId('download-ubl')).toHaveLength(0)
    expect(downloadByName(), 'no download affordance when the load failed').toBeNull()
    expect(screen.queryByText(LOAD_FAILED), 'floor: this really is the failure branch').not.toBeNull()
  })

  it('V9/AC3: the download saves exactly the bytes on screen, with no second fetch', async () => {
    const mock = stubOk(DOC)
    const create = vi.spyOn(URL, 'createObjectURL')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    renderModal()
    await settle()

    const btn = screen.queryByTestId('download-ubl')
    expect(btn, 'a loaded document offers a download').not.toBeNull()
    fireEvent.click(btn as HTMLElement)

    expect(create).toHaveBeenCalledTimes(1)
    const blob = create.mock.calls[0][0] as Blob
    expect(blob.type).toBe('application/xml')
    // Anchored to the fixture as well as to the <pre>: an empty blob beside an empty <pre>
    // would satisfy the identity alone.
    expect(screen.getByTestId('ubl-xml').textContent, 'floor: real bytes are on screen').toBe(DOC)
    expect(await readBlob(blob)).toBe(screen.getByTestId('ubl-xml').textContent)
    expect(mock, 'the blob comes from state, never a second fetch').toHaveBeenCalledTimes(1)
  })

  it('V10/AC3: the object URL is revoked after the click', async () => {
    stubOk(DOC)
    const create = vi.spyOn(URL, 'createObjectURL')
    const revoke = vi.spyOn(URL, 'revokeObjectURL')
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    renderModal()
    await settle()

    const btn = screen.queryByTestId('download-ubl')
    expect(btn, 'a loaded document offers a download').not.toBeNull()
    fireEvent.click(btn as HTMLElement)

    expect(revoke).toHaveBeenCalledTimes(1)
    expect(revoke).toHaveBeenCalledWith(create.mock.results[0].value)
    expect(create.mock.invocationCallOrder[0]).toBeLessThan(click.mock.invocationCallOrder[0])
    expect(click.mock.invocationCallOrder[0]).toBeLessThan(revoke.mock.invocationCallOrder[0])
  })

  it.each([
    ['INV/2026 0001', 'INV-2026-0001.xml'],
    ['', 'invoice.xml'],
    ['A.B_C-1', 'A.B_C-1.xml'],
  ])('V11/AC4: %s downloads as %s', async (invoiceNumber, filename) => {
    stubOk(DOC)
    const create = vi.spyOn(URL, 'createObjectURL')
    let seen: { href: string; download: string } | null = null
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      seen = { href: this.href, download: this.download }
    })

    renderModal({ invoiceNumber })
    await settle()

    const btn = screen.queryByTestId('download-ubl')
    expect(btn, 'a loaded document offers a download').not.toBeNull()
    fireEvent.click(btn as HTMLElement)

    const captured = seen as { href: string; download: string } | null
    expect(captured, 'the anchor was clicked').not.toBeNull()
    expect(captured?.download).toBe(filename)
    expect(captured?.href).toBe(create.mock.results[0].value)
  })
})

describe('XmlModal provenance (task-402)', () => {
  it('V12/AC5: the provenance sentence renders exactly', async () => {
    stubOk(DOC)
    renderModal()
    await settle()

    const band = screen.queryByTestId('ubl-provenance')
    expect(band, 'the viewer states where the document came from').not.toBeNull()
    expect(band?.textContent).toBe(PROVENANCE)
  })

  // The modal takes no invoice status, so the load ladder is the only axis it can vary on.
  it('V13/AC5: the provenance sentence is status-independent', async () => {
    const arms: Array<[string, () => void]> = [
      ['loading', () => stubPending()],
      ['409', () => stubFail(409, REASON)],
      ['404', () => stubFail(404, 'not found')],
      ['ready', () => stubOk(DOC)],
      ['500', () => stubFail(500, 'internal server error')],
    ]

    for (const [label, stub] of arms) {
      stub()
      renderModal()
      await settle()

      const band = screen.queryByTestId('ubl-provenance')
      expect(band, `provenance is missing on the ${label} arm`).not.toBeNull()
      expect(band?.textContent, `provenance differs on the ${label} arm`).toBe(PROVENANCE)

      cleanup()
      vi.unstubAllGlobals()
    }
  })
})

describe('XmlModal retired copy and the deleted client builder (task-402)', () => {
  const SRC_DIR = join(process.cwd(), 'src')

  // Both needles are assembled at runtime: this file lives under src/, so the walk below
  // reads its own source, and a contiguous literal would self-match.
  const RETIRED_COPY = ['choose ', String.fromCharCode(0x201c), 'View XML', String.fromCharCode(0x201d)].join('')
  const BUILDER = ['ubl', 'Xml'].join('')
  // The module specifier too, so a namespace import (which never names the function)
  // cannot slip through. Assembled for the same self-match reason -- the existsSync
  // paths below are joined at runtime rather than written out.
  const BUILDER_MODULE = ['lib', 'xml'].join('/')

  function walkSrc(): Array<{ path: string; content: string }> {
    return readdirSync(SRC_DIR, { recursive: true, withFileTypes: true })
      .filter((e) => e.isFile() && /\.(ts|tsx)$/.test(e.name))
      .map((e) => {
        const full = join(e.parentPath, e.name)
        return { path: full, content: readFileSync(full, 'utf8') }
      })
  }

  it('V14/AC6: the retired empty-state copy is gone from the SPA source', () => {
    for (const f of walkSrc()) {
      expect(f.content, `${f.path} still carries the retired empty-state copy`).not.toContain(RETIRED_COPY)
    }
  })

  it('V15/AC1+AC6: the client-side document builder is gone and has no importers', () => {
    for (const f of walkSrc()) {
      expect(f.content, `${f.path} still names the client-side builder`).not.toContain(BUILDER)
      expect(f.content, `${f.path} still imports the client-side builder`).not.toContain(BUILDER_MODULE)
    }
    expect(existsSync(join(SRC_DIR, 'lib', 'xml.ts')), 'the builder module must be deleted').toBe(false)
    expect(existsSync(join(SRC_DIR, 'lib', 'xml.test.ts')), "the builder's suite must be deleted").toBe(false)
  })

  it('V16: vacuity floor -- the walk read the tree', () => {
    const files = walkSrc()
    expect(files.length, 'the walk must have visited at least 100 files').toBeGreaterThanOrEqual(100)
    // Positive control from the SAME walk, keyed on a declaration this test file does not
    // contain: proves contents were read, not just names.
    expect(files.some((f) => f.content.includes('export function InvoiceDetail'))).toBe(true)
    expect(files.some((f) => f.content.includes('data-testid="ubl-modal"'))).toBe(true)
  })
})

describe('XmlModal shell -- unchanged chrome and dismissal (task-402)', () => {
  it('V17/AC7: scrim, panel and header chrome are unchanged', async () => {
    stubPending() // the header must read the PROP, so the fetch never answers
    renderModal()
    await settle()

    const scrim = screen.getByTestId('ubl-modal')
    expect(scrim.style.position).toBe('fixed')
    expect(scrim.style.inset).toBe('0') // React never appends px to a 0
    expect(scrim.style.zIndex).toBe('80')

    const panel = panelOf()
    expect(panel.style.width).toBe('760px')
    expect(panel.style.background).toBe('var(--bg-2)')
    expect(panel.style.borderRadius).toBe('var(--radius-md)')

    expect(screen.queryByText('UBL 2.1 document')).not.toBeNull()
    expect(within(scrim).getByText(/^PEPPOL BIS 3\.0 ·/).textContent).toBe(`PEPPOL BIS 3.0 · ${NUMBER}`)
  })

  it('V18/AC10: the panel is an accessible dialog', async () => {
    stubOk(DOC)
    renderModal()
    await settle()

    const dialog = screen.queryByRole('dialog')
    expect(dialog, 'the panel exposes role=dialog').not.toBeNull()
    expect(dialog).toBe(panelOf())
    expect(dialog?.getAttribute('aria-modal')).toBe('true')
    expect(screen.queryByRole('dialog', { name: /\S/ }), 'the dialog has a non-empty accessible name').not.toBeNull()
    expect(dialog?.parentElement).toBe(screen.getByTestId('ubl-modal'))
  })

  it('V19/AC10: Escape closes the viewer and other keys do not', async () => {
    stubOk(DOC)
    const { onClose } = renderModal()
    await settle()

    fireEvent.keyDown(window, { key: 'Enter' })
    expect(onClose, 'only Escape dismisses').not.toHaveBeenCalled()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('V20: the shell closers still work', async () => {
    stubOk(DOC)

    const closeBtn = renderModal()
    await settle()
    fireEvent.click(screen.getByTestId('ubl-modal-close'))
    expect(closeBtn.onClose).toHaveBeenCalledTimes(1)
    cleanup()

    const scrim = renderModal()
    await settle()
    fireEvent.click(screen.getByTestId('ubl-modal'))
    expect(scrim.onClose).toHaveBeenCalledTimes(1)
    cleanup()

    const inner = renderModal()
    await settle()
    fireEvent.click(panelOf())
    expect(inner.onClose, 'reading the document must not dismiss it').not.toHaveBeenCalled()
  })
})

// ---------------------------------------------------------------------------
// QA adversarial coverage (task-402, Mode B). Q1 and Q3 close mutants the V-rows
// above let live; the rest are the edge/negative cases they never reach.
// ---------------------------------------------------------------------------

describe('XmlModal -- QA adversarial coverage (task-402)', () => {
  it('Q1/AC9: an empty 200 body is a failure, not a blank document', async () => {
    // The truthiness gate. `text()` resolves '' without throwing and resolveStatus's
    // default isEmpty is Array-gated, so useAsync reports 'ready' with data ''. Without
    // the gate this ships a blank <pre> and a download of zero bytes.
    stubOk('')
    renderModal()
    await settle()

    expect(screen.queryAllByTestId('ubl-xml'), 'an empty body is not a document').toHaveLength(0)
    expect(downloadByName(), 'nothing to download from an empty body').toBeNull()
    expect(screen.queryAllByTestId('download-ubl')).toHaveLength(0)
    expect(screen.queryByText(LOAD_FAILED), 'it falls to the generic failure arm').not.toBeNull()
  })

  it('Q2/AC9: a 200 whose body cannot be read scrubs the malformed sentinel', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve({ ok: true, status: 200, text: () => Promise.reject(new Error('boom')) })),
    )
    renderModal()
    await settle()

    expect(screen.queryByText(/malformed response body/i), 'the client sentinel must not surface').toBeNull()
    expect(screen.queryByText(/boom/), 'the underlying reason must not surface').toBeNull()
    expect(screen.queryByText(LOAD_FAILED)).not.toBeNull()
    expect(screen.queryAllByTestId('ubl-xml')).toHaveLength(0)
  })

  it.each([
    // Per-character map, NOT a collapsing run: a `+` quantifier would give 'INV-2026-0001.xml'
    // for the first row and pass V11 unchanged.
    ['INV  2026//0001', 'INV--2026--0001.xml'],
    // Every disallowed char maps to '-', so a non-empty number can never reach the
    // 'invoice' fallback -- only the empty string does (V11).
    ['!!!', '---.xml'],
    ['   ', '---.xml'],
    // Already-legal punctuation is preserved verbatim, including a leading dot.
    ['..', '...xml'],
  ])('Q3/AC4: %s downloads as %s', async (invoiceNumber, filename) => {
    stubOk(DOC)
    let seen: string | null = null
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      seen = this.download
    })

    renderModal({ invoiceNumber })
    await settle()
    fireEvent.click(screen.getByTestId('download-ubl'))

    expect(seen as string | null).toBe(filename)
  })

  it('Q4/AC4: a very long invoice number is sanitised but never truncated', async () => {
    const long = `${'A'.repeat(200)}/${'B'.repeat(100)}`
    stubOk(DOC)
    let seen: string | null = null
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      seen = this.download
    })

    renderModal({ invoiceNumber: long })
    await settle()
    fireEvent.click(screen.getByTestId('download-ubl'))

    // No length cap by design (AC4 states no cap); pinned so a future cap is a deliberate change.
    expect(seen as string | null).toBe(`${'A'.repeat(200)}-${'B'.repeat(100)}.xml`)
  })

  it('Q5/AC1+AC3: multibyte content renders verbatim and downloads byte-identically', async () => {
    // Naira sign, CJK, a DECOMPOSED combining acute and an astral emoji: a UTF-8 slip or a
    // Unicode normalisation anywhere in the Blob/FileReader path breaks byte-identity.
    const unicode = [
      '<?xml version="1.0" encoding="UTF-8"?>',
      '<Invoice>',
      '  <cbc:PayableAmount currencyID="NGN">₦1,075.00</cbc:PayableAmount>',
      '  <cbc:Note>发票 é 🧾</cbc:Note>',
      '</Invoice>',
    ].join('\n')

    stubOk(unicode)
    const create = vi.spyOn(URL, 'createObjectURL')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    renderModal()
    await settle()

    expect(screen.getByTestId('ubl-xml').textContent, 'the naira sign survives to the DOM').toBe(unicode)
    fireEvent.click(screen.getByTestId('download-ubl'))

    const saved = await readBlob(create.mock.calls[0][0] as Blob)
    expect(saved).toBe(unicode)
    expect(saved, 'the combining acute is not normalised into U+00E9').toContain('é')
  })

  it('Q6/AC1+AC3: a multi-megabyte document renders in full and downloads intact', async () => {
    const line = '  <cac:InvoiceLine><cbc:ID>0000</cbc:ID></cac:InvoiceLine>\n'
    const big = `<Invoice>\n${line.repeat(36000)}</Invoice>` // ~2.1 MB
    expect(big.length, 'floor: this really is a multi-megabyte document').toBeGreaterThan(2_000_000)

    stubOk(big)
    const create = vi.spyOn(URL, 'createObjectURL')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    renderModal()
    await settle()

    // No truncation, no windowing: the <pre> holds every byte.
    expect(screen.getByTestId('ubl-xml').textContent?.length).toBe(big.length)
    fireEvent.click(screen.getByTestId('download-ubl'))
    expect((create.mock.calls[0][0] as Blob).size, 'the saved blob is the whole document').toBe(
      new TextEncoder().encode(big).length,
    )
  })

  it('Q7/AC9: Retry on the generic arm refetches and clears the stale error', async () => {
    let n = 0
    const mock = vi.fn(() => {
      n += 1
      return n === 1
        ? Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'internal server error' }) })
        : Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(DOC) })
    })
    vi.stubGlobal('fetch', mock)

    renderModal()
    await settle()
    expect(screen.queryByText(LOAD_FAILED), 'floor: the first load failed').not.toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    await settle()

    expect(mock, 'Retry issues a second request').toHaveBeenCalledTimes(2)
    expect(screen.getByTestId('ubl-xml').textContent, 'the document replaces the error').toBe(DOC)
    expect(screen.queryByText(LOAD_FAILED), 'the stale error is gone').toBeNull()
    expect(screen.queryByRole('button', { name: /retry/i }), 'and so is its Retry').toBeNull()
    expect(screen.queryByTestId('download-ubl'), 'the download appears once there are bytes').not.toBeNull()
  })

  it('Q8/AC3: each download creates and revokes its own object URL -- repeat clicks leak none', async () => {
    stubOk(DOC)
    const create = vi.spyOn(URL, 'createObjectURL')
    const revoke = vi.spyOn(URL, 'revokeObjectURL')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    renderModal()
    await settle()

    const btn = screen.getByTestId('download-ubl')
    fireEvent.click(btn)
    fireEvent.click(btn)
    fireEvent.click(btn)

    expect(create).toHaveBeenCalledTimes(3)
    expect(revoke, 'every created URL is revoked').toHaveBeenCalledTimes(3)
    const created = create.mock.results.map((r) => r.value)
    const revoked = revoke.mock.calls.map((c) => c[0])
    expect(revoked, 'and each one is the URL its own click created').toEqual(created)
  })

  it('Q9: mounting and unmounting without a download allocates no object URL', async () => {
    stubOk(DOC)
    const create = vi.spyOn(URL, 'createObjectURL')

    const { unmount } = renderModal()
    await settle()
    unmount()

    expect(create, 'nothing is allocated until the user asks to save').not.toHaveBeenCalled()
  })

  it('Q10/AC2: closing mid-flight drops the late response and reopening refetches exactly once', async () => {
    let release: ((v: unknown) => void) | null = null
    const mock = vi.fn(() => new Promise((resolve) => { release = resolve }))
    vi.stubGlobal('fetch', mock)
    const errs = vi.spyOn(console, 'error').mockImplementation(() => {})

    const { unmount } = renderModal()
    await settle()
    expect(mock).toHaveBeenCalledTimes(1)
    unmount()

    // The response lands after the viewer is gone: useAsync's runId guard must drop it
    // rather than set state on an unmounted tree.
    await act(async () => {
      release?.({ ok: true, status: 200, text: () => Promise.resolve(DOC) })
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(errs, 'no state update on an unmounted tree').not.toHaveBeenCalled()

    stubOk(DOC)
    renderModal()
    await settle()
    expect(screen.getByTestId('ubl-xml').textContent, 'reopening loads the document').toBe(DOC)
  })

  it('Q12/AC2: a parent re-render does not refetch -- one request per open, under polling', async () => {
    // InvoiceDetail live-polls (useLiveRefresh, InvoiceDetail.tsx:227) and keeps this modal
    // mounted across ticks, so a tick re-renders it with fresh props. The fetch is keyed on
    // invoiceId alone; the document deliberately stays the one fetched at open, which is
    // exactly what the provenance sentence discloses ("when you opened this view").
    const mock = stubOk(DOC)
    const { rerender } = renderModal()
    await settle()
    expect(mock).toHaveBeenCalledTimes(1)

    rerender(
      <XmlModal ctx={modalCtx()} base={BASE} invoiceId={ID} invoiceNumber="INV-2026-9999" onClose={vi.fn()} />,
    )
    await settle()

    expect(mock, 'a poll tick must not re-request the document').toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('ubl-xml').textContent, 'the opened document is still on screen').toBe(DOC)
    // The new number does reach the filename -- it is read at click time, not at open.
    let seen: string | null = null
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      seen = this.download
    })
    fireEvent.click(screen.getByTestId('download-ubl'))
    expect(seen as string | null).toBe('INV-2026-9999.xml')
  })

  it('Q11/AC9: the longest real blocked reason renders verbatim inside the refusal', async () => {
    // Six Missing() gaps joined -- the worst case ublGate can emit (ubl_test.go).
    const longest =
      'This invoice cannot be rendered as a UBL document — it is missing at least one line item, ' +
      'the supplier TIN, the supplier name, the buyer name, the invoice date and the currency code.'
    stubFail(409, longest)
    renderModal()
    await settle()

    expect(screen.queryByText(longest), 'the server sentence is not truncated or reflowed away').not.toBeNull()
    expect(screen.queryByRole('button', { name: /retry/i }), 'a content refusal can only 409 again').toBeNull()
    expect(downloadByName()).toBeNull()
  })
})
