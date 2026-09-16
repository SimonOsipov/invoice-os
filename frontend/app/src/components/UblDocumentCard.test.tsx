// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// jsdom 27.4.0: Blob has no text(), so bytes are read back with FileReader; spy
// HTMLAnchorElement.prototype.click to silence "Not implemented: navigation" and capture the anchor.

import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { SourceDocumentCard } from './SourceDocumentCard'
import type { SourceDocumentAsync } from './SourceDocumentStates'
import { UblDocumentCard } from './UblDocumentCard'
import { LOAD_FAILED, ublFilename } from './XmlModal'

const BASE = 'https://gw'
const ID = 'inv-1'
const NUMBER = 'INV/2026 0001'
const FILENAME = 'INV-2026-0001.xml'
const UBL_URL = `${BASE}/api/invoice/v1/invoices/${ID}/ubl`
const META = 'UBL 2.1 · PEPPOL BIS 3.0'
const DOC = '<?xml version="1.0" encoding="UTF-8"?>\n<Invoice>₦ Ọ̀yọ́</Invoice>'
const REASON = 'This invoice cannot be rendered as a UBL document — it is missing at least one line item.'

type Props = {
  canView?: boolean
  blockedReason?: string | null
  editing?: boolean
  invoiceNumber?: string
  onView?: () => void
}

function cardCtx(): PlatformCtx {
  return { authedFetch: createAuthedFetch(() => 'tok', vi.fn()) } as unknown as PlatformCtx
}

function renderCard(over: Props = {}) {
  const onView = over.onView ?? vi.fn()
  const utils = render(
    <UblDocumentCard
      ctx={cardCtx()}
      base={BASE}
      invoiceId={ID}
      invoiceNumber={over.invoiceNumber ?? NUMBER}
      canView={over.canView ?? true}
      blockedReason={over.blockedReason ?? null}
      editing={over.editing ?? false}
      onView={onView}
    />,
  )
  return { ...utils, onView }
}

function body(): HTMLElement {
  return screen.getByTestId('ubl-document-card')
}

function outer(): HTMLElement {
  return body().parentElement as HTMLElement
}

function stubOk(text: string) {
  const mock = vi.fn((_url: string, _init?: RequestInit) => Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(text) }))
  vi.stubGlobal('fetch', mock)
  return mock
}

function stubFail(status: number, error: string) {
  const mock = vi.fn((_url: string, _init?: RequestInit) => Promise.resolve({ ok: false, status, json: () => Promise.resolve({ error }) }))
  vi.stubGlobal('fetch', mock)
  return mock
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

function sourceMeta(withDocument = true): SourceDocumentAsync {
  const document: SourceDocumentRecord = {
    id: 'doc-1',
    filename: 'june-sales.pdf',
    declared_content_type: 'application/pdf',
    size_bytes: 151_552,
    content_hash: '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560',
    uploaded_at: '2026-06-12T11:42:00Z',
    uploaded_by: 'c0000000-0000-0000-0000-000000000001',
    invoices_created: 1,
    other_invoice_rows: [],
  }
  const data: SourceDocumentResponse = { invoice_id: ID, source_rows: [1], document: withDocument ? document : null }
  return { status: 'ready', data, error: null, run: vi.fn() }
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

describe('UblDocumentCard', () => {
  it('T02-1: uses the Source document card recipe', () => {
    render(<SourceDocumentCard meta={sourceMeta()} onOpen={vi.fn()} extraction={{ jobId: 'job-1', loading: false, failed: false }} onOpenExtraction={vi.fn()} />)
    const sourceOuter = screen.getByTestId('source-document-card').parentElement as HTMLElement
    renderCard()

    expect(outer().getAttribute('style'), 'floor: the outer card has a style').toContain('overflow: hidden')
    expect(outer().getAttribute('style')).toBe(sourceOuter.getAttribute('style'))
    const header = outer().firstElementChild as HTMLElement
    const sourceHeader = sourceOuter.firstElementChild as HTMLElement
    expect(header.style.padding).toBe('13px 18px')
    expect(header.getAttribute('style')).toBe(sourceHeader.getAttribute('style'))
    expect(header.children[1].getAttribute('style')).toBe(sourceHeader.children[1].getAttribute('style'))
    expect(body().getAttribute('style')).toBe(screen.getByTestId('source-document-card').getAttribute('style'))

    const buttons = within(outer()).getAllByRole('button')
    expect(buttons).toHaveLength(2)
    for (const b of buttons) {
      expect(b.className).toBe('v2-btn v2-btn-ghost pf-btn')
      expect(b.getAttribute('type')).toBe('button')
      expect(b.style.width).toBe('100%')
      expect(b.style.height).toBe('34px')
      expect(b.style.fontSize).toBe('13px')
    }
  })

  it('T02-2: names the document from props', () => {
    renderCard()
    expect(screen.getByTestId('ubl-card-filename').textContent).toBe(FILENAME)
    expect(ublFilename(NUMBER)).toBe(FILENAME)
    expect(screen.getByTestId('ubl-card-meta').textContent).toBe(META)
    const header = outer().firstElementChild as HTMLElement
    expect(header.children[0].className).toBe('card-title')
    expect(header.children[0].textContent).toBe('UBL 2.1 document')
    expect(header.children[1].textContent).toBe('READ ONLY')
  })

  it('T02-3: mounting issues no request in any state', async () => {
    const arms: Array<[string, Props]> = [
      ['offered', {}],
      ['refused', { canView: false, blockedReason: REASON }],
      ['editing', { editing: true }],
    ]
    for (const [label, props] of arms) {
      const mock = stubOk(DOC)
      renderCard(props)
      await settle()
      expect(screen.queryAllByTestId('ubl-document-card'), `floor: ${label} rendered`).toHaveLength(1)
      expect(mock, `${label} mount fetched`).toHaveBeenCalledTimes(0)
      cleanup()
      vi.unstubAllGlobals()
    }
  })

  it('T02-4: View calls onView and fetches nothing', async () => {
    const mock = stubOk(DOC)
    const { onView } = renderCard()
    fireEvent.click(screen.getByTestId('ubl-card-view'))
    await settle()
    expect(onView).toHaveBeenCalledTimes(1)
    expect(mock).toHaveBeenCalledTimes(0)
  })

  it('T02-5: Download saves the server body verbatim, with no viewer', async () => {
    const mock = stubOk(DOC)
    const create = vi.spyOn(URL, 'createObjectURL')
    let seen: { href: string; download: string } | null = null
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      seen = { href: this.href, download: this.download }
    })
    const { onView } = renderCard()

    fireEvent.click(screen.getByTestId('ubl-card-download'))
    await settle()

    expect(mock).toHaveBeenCalledTimes(1)
    expect(String(mock.mock.calls[0][0])).toBe(UBL_URL)
    expect(create).toHaveBeenCalledTimes(1)
    const blob = create.mock.calls[0][0] as Blob
    expect(blob.type).toBe('application/xml')
    expect(await readBlob(blob)).toBe(DOC)
    const captured = seen as { href: string; download: string } | null
    expect(captured, 'the anchor was clicked').not.toBeNull()
    expect(captured?.download).toBe(ublFilename(NUMBER))
    expect(captured?.download).toBe(FILENAME)
    expect(onView).not.toHaveBeenCalled()
    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(0)
    expect(screen.queryAllByTestId('ubl-card-download-error')).toHaveLength(0)
  })

  it('T02-6: a 409 prints the server sentence and saves nothing', async () => {
    stubFail(409, REASON)
    const create = vi.spyOn(URL, 'createObjectURL')
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderCard()

    fireEvent.click(screen.getByTestId('ubl-card-download'))
    await settle()

    expect(screen.getByTestId('ubl-card-download-error').textContent).toBe(REASON)
    expect(create).not.toHaveBeenCalled()
    expect(click).not.toHaveBeenCalled()
  })

  it.each([
    ['500', () => stubFail(500, 'internal server error'), 'internal server error'],
    ['404', () => stubFail(404, 'not found'), 'not found'],
    ['network', () => vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch')))), 'Failed to fetch'],
    ['empty 200', () => stubOk(''), null],
  ] as Array<[string, () => void, string | null]>)('T02-7: %s prints LOAD_FAILED and saves nothing', async (_label, stub, leak) => {
    stub()
    const create = vi.spyOn(URL, 'createObjectURL')
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderCard()

    fireEvent.click(screen.getByTestId('ubl-card-download'))
    await settle()

    expect(screen.getByTestId('ubl-card-download-error').textContent).toBe(LOAD_FAILED)
    if (leak !== null) expect(outer().textContent).not.toContain(leak)
    expect(create).not.toHaveBeenCalled()
    expect(click).not.toHaveBeenCalled()
  })

  it('T02-8: a refused document shows the reason as text and offers no action', () => {
    const reason = 'Only an <b>admin</b> can view this document.'
    renderCard({ canView: false, blockedReason: reason })
    expect(screen.getByTestId('ubl-card-blocked').textContent).toBe(reason)
    expect(outer().querySelector('b')).toBeNull()
    expect(outer().querySelectorAll('button')).toHaveLength(0)
    expect(screen.getByTestId('ubl-card-filename').textContent).toBe(FILENAME)
    expect(screen.getByTestId('ubl-card-meta').textContent).toBe(META)
  })

  it('T02-9: a refused document with no reason prints nothing extra', () => {
    renderCard({ canView: false, blockedReason: null })
    expect(body().textContent, 'floor: identity row').toBe(FILENAME + META)
    expect(screen.queryAllByTestId('ubl-card-blocked')).toHaveLength(0)
    expect(outer().querySelectorAll('button')).toHaveLength(0)
  })

  it('T02-10: a contradictory wire follows canView', () => {
    renderCard({ canView: true, blockedReason: REASON })
    expect(screen.queryAllByTestId('ubl-card-view')).toHaveLength(1)
    expect(screen.queryAllByTestId('ubl-card-download')).toHaveLength(1)
    expect(screen.queryAllByTestId('ubl-card-blocked')).toHaveLength(0)
  })

  it('T02-11: while editing the card keeps its identity and offers no action', () => {
    renderCard({ editing: true, canView: true })
    expect(screen.getByTestId('ubl-card-filename').textContent, 'floor: identity row').toBe(FILENAME)
    expect(outer().querySelectorAll('button')).toHaveLength(0)
    expect(screen.queryAllByTestId('ubl-card-blocked')).toHaveLength(0)
    cleanup()

    renderCard({ editing: true, canView: false, blockedReason: REASON })
    expect(screen.getByTestId('ubl-card-blocked').textContent).toBe(REASON)
    expect(outer().querySelectorAll('button')).toHaveLength(0)
  })

  it('T02-12: a double click issues one request while it is in flight', async () => {
    let release: ((v: unknown) => void) | null = null
    const mock = vi.fn(() => new Promise((resolve) => { release = resolve }))
    vi.stubGlobal('fetch', mock)
    vi.spyOn(URL, 'createObjectURL')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderCard()
    const btn = screen.getByTestId('ubl-card-download') as HTMLButtonElement

    // One act: React has not re-rendered `disabled` between the clicks, so only the ref can stop the second.
    act(() => {
      btn.click()
      btn.click()
    })

    expect(mock).toHaveBeenCalledTimes(1)
    expect(btn.disabled).toBe(true)

    await act(async () => {
      release?.({ ok: true, status: 200, text: () => Promise.resolve(DOC) })
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(btn.disabled).toBe(false)
    expect(mock).toHaveBeenCalledTimes(1)
  })

  it('T02-13: the identity row, refusal block, disabled Download and error copy the Source card', async () => {
    const withDoc = render(<SourceDocumentCard meta={sourceMeta()} onOpen={vi.fn()} extraction={{ jobId: null, loading: false, failed: true }} onOpenExtraction={vi.fn()} />)
    const srcBody = within(withDoc.container).getByTestId('source-document-card')
    const srcRow = srcBody.firstElementChild as HTMLElement
    const srcDisabled = within(withDoc.container).getByTestId('open-extraction-review') as HTMLButtonElement
    const srcReason = srcDisabled.nextElementSibling as HTMLElement
    expect(srcDisabled.disabled, 'floor: the Source control is disabled').toBe(true)
    const noDoc = render(<SourceDocumentCard meta={sourceMeta(false)} onOpen={vi.fn()} extraction={{ jobId: null, loading: false, failed: false }} onOpenExtraction={vi.fn()} />)
    const srcDashed = within(noDoc.container).getByTestId('source-document-card').firstElementChild as HTMLElement
    expect(srcDashed.getAttribute('style'), 'floor: the Source dashed block').toContain('dashed')

    const refused = render(
      <UblDocumentCard ctx={cardCtx()} base={BASE} invoiceId={ID} invoiceNumber={NUMBER} canView={false} blockedReason={REASON} editing={false} onView={vi.fn()} />,
    )
    const blocked = within(refused.container).getByTestId('ubl-card-blocked')
    expect(blocked.getAttribute('style')).toBe(`margin-top: 12px; ${srcDashed.getAttribute('style')}`)
    const reasonDiv = blocked.firstElementChild as HTMLElement
    expect(reasonDiv.textContent).toBe(REASON)
    expect(reasonDiv.style.fontSize).toBe('12.5px')
    expect(reasonDiv.style.lineHeight).toBe('1.55')
    expect(reasonDiv.style.color).toBe('var(--fg-3)')
    refused.unmount()

    const mock = vi.fn(() => new Promise(() => {}))
    vi.stubGlobal('fetch', mock)
    renderCard()
    const row = body().firstElementChild as HTMLElement
    expect(row.getAttribute('style')).toBe(srcRow.getAttribute('style'))
    const tile = row.firstElementChild as HTMLElement
    const srcTile = srcRow.firstElementChild as HTMLElement
    for (const prop of ['flex', 'width', 'height', 'border-radius', 'display', 'place-items']) {
      expect(tile.style.getPropertyValue(prop), prop).toBe(srcTile.style.getPropertyValue(prop))
    }
    expect(tile.style.width).toBe('38px')
    expect(tile.style.background).toBe('var(--bg-3)')
    expect(tile.style.color).toBe('var(--action)')
    expect(tile.querySelector('svg'), 'the tile holds the document glyph').not.toBeNull()
    expect(screen.getByTestId('ubl-card-filename').getAttribute('style')).toBe((srcRow.children[1].firstElementChild as HTMLElement).getAttribute('style'))
    expect(screen.getByTestId('ubl-card-filename').style.wordBreak).toBe('break-all')
    expect(screen.getByTestId('ubl-card-meta').getAttribute('style')).toBe(within(withDoc.container).getByTestId('source-document-card-meta').getAttribute('style'))

    const btn = screen.getByTestId('ubl-card-download') as HTMLButtonElement
    const idle = btn.getAttribute('style')
    expect(idle).toBe((within(withDoc.container).getByTestId('view-source-document') as HTMLElement).getAttribute('style'))
    fireEvent.click(btn)
    expect(mock).toHaveBeenCalledTimes(1)
    expect(btn.disabled).toBe(true)
    expect(btn.getAttribute('style')).toBe(srcDisabled.getAttribute('style'))
    cleanup()
    vi.unstubAllGlobals()

    stubFail(500, 'internal server error')
    renderCard()
    fireEvent.click(screen.getByTestId('ubl-card-download'))
    await settle()
    const error = screen.getByTestId('ubl-card-download-error')
    expect(error.textContent).toBe(LOAD_FAILED)
    expect(error.getAttribute('style')).toBe(srcReason.getAttribute('style'))
    expect(screen.getByTestId('ubl-card-download').nextElementSibling).toBe(error)
  })

  it('T02-14: a failed Download re-arms the button, and a retry that succeeds clears the error and saves once', async () => {
    const fail = stubFail(500, 'internal server error')
    const create = vi.spyOn(URL, 'createObjectURL')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderCard()
    const btn = screen.getByTestId('ubl-card-download') as HTMLButtonElement

    fireEvent.click(btn)
    await settle()
    expect(fail).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('ubl-card-download-error').textContent).toBe(LOAD_FAILED)
    expect(btn.disabled, 'a failure re-enables Download').toBe(false)

    const ok = stubOk(DOC)
    fireEvent.click(btn)
    await settle()
    expect(ok, 'the retry reached the wire').toHaveBeenCalledTimes(1)
    expect(create).toHaveBeenCalledTimes(1)
    expect(await readBlob(create.mock.calls[0][0] as Blob)).toBe(DOC)
    expect(screen.queryAllByTestId('ubl-card-download-error'), 'the old error is cleared').toHaveLength(0)
    expect(btn.disabled).toBe(false)
  })

  it('T02-15: Download saves the exact bytes, line endings and edges included', async () => {
    // `e\u0301` is NFD: a normalising save would change its bytes.
    const doc = '<?xml version="1.0" encoding="UTF-8"?>\r\n<Invoice>\r\n  <Note>₦ Ọ̀yọ́ Ade\u0301 🇳🇬</Note>\r\n</Invoice>\n'
    stubOk(doc)
    const create = vi.spyOn(URL, 'createObjectURL')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderCard()

    fireEvent.click(screen.getByTestId('ubl-card-download'))
    await settle()

    expect(create).toHaveBeenCalledTimes(1)
    // readAsText strips a leading BOM, so compare raw bytes.
    const bytes = await new Promise<number[]>((resolve, reject) => {
      const fr = new FileReader()
      fr.onload = () => resolve(Array.from(new Uint8Array(fr.result as ArrayBuffer)))
      fr.onerror = () => reject(fr.error)
      fr.readAsArrayBuffer(create.mock.calls[0][0] as Blob)
    })
    const want = Array.from(new TextEncoder().encode(doc))
    expect(doc.normalize('NFC'), 'floor: the fixture is not NFC').not.toBe(doc)
    expect(want.length, 'floor: the fixture has bytes').toBeGreaterThan(doc.length)
    expect(bytes).toEqual(want)
  })

  it('T02-16: editing hides the actions even when the wire also sends a reason', () => {
    renderCard({ editing: true, canView: true, blockedReason: REASON })
    expect(screen.getByTestId('ubl-card-filename').textContent, 'floor: identity row').toBe(FILENAME)
    expect(outer().querySelectorAll('button')).toHaveLength(0)
    expect(screen.queryAllByTestId('ubl-card-blocked')).toHaveLength(0)
  })

  it('T02-17: the two actions carry their labels', () => {
    renderCard()
    expect(screen.getByRole('button', { name: 'View UBL/XML' }).getAttribute('data-testid')).toBe('ubl-card-view')
    expect(screen.getByRole('button', { name: 'Download .xml' }).getAttribute('data-testid')).toBe('ubl-card-download')
  })
})
