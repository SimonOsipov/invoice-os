// @vitest-environment jsdom
// EXTR-15-07 QA, Mode B adversarial coverage -- the App-level glue of the hand-off.
//
// CreateFlow.test.tsx's HO-5 asserts a MOCKED ctx.enterByHand was called; invoiceDraft.test.ts's
// HO-6 asserts the library forwards an id handed to it directly. Neither runs App.tsx's own
// glue, so three mutations to it survive the whole suite -- each spec below names the one it
// kills.
//
// Harness is App.documentStages.test.tsx's: the real <App/>, a stubbed session/localStorage
// and gateway, ctx captured through a mocked Sidebar. The filing leg is fetch (apiFetch), so
// the create body is read off the fetch stub; only the run spec's upload leg is XHR
// (importApi.ts's uploadSourceDocument), hence the local FakeXhr.

import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import type { CarriedReading } from './lib/importApi'
import { runFailures } from './lib/importRun'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

// Only what xhrJson (importApi.ts) touches for one POST /documents round trip; the run
// spec drives it to `onerror`, which is enough to land a failed row in `run`.
class FakeXhr {
  static instances: FakeXhr[] = []
  static last(): FakeXhr | undefined {
    return FakeXhr.instances[FakeXhr.instances.length - 1]
  }

  status = 0
  statusText = ''
  responseText = ''
  upload: { onprogress: (() => void) | null; onload: (() => void) | null } = { onprogress: null, onload: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null

  constructor() {
    FakeXhr.instances.push(this)
  }

  open(): void {}
  setRequestHeader(): void {}
  send(): void {}

  fireError(): void {
    this.onerror?.()
  }
}

// Node v25's native localStorage collides with jsdom's (App.standIn.test.tsx:74-75).
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  }
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
const ENTITY_B = 'bbbbbbbb-0000-4000-8000-000000000002'
const DOC_ID = 'dddddddd-0000-4000-8000-00000000000d'
const DOC_ID_2 = 'eeeeeeee-0000-4000-8000-00000000000e'
const CREATED_ID = 'cccccccc-0000-4000-8000-00000000000c'

// EXTR-27-03. RED against enterByHand's unmodified, synchronous body -- readingRequests
// stays empty until the executor wires the async read, so every A-test's control below
// fails cleanly rather than exercising the future behaviour by accident.
const READING: CarriedReading = {
  document_id: DOC_ID,
  extraction_job_id: 'j-1',
  issue_date: '2026-03-01',
  buyer_tin: '23456789-0001',
  buyer_name: 'Kano Mills Ltd',
  currency: 'NGN',
  subtotal: '1000.00',
  vat: '75.00',
  total: '1075.00',
  line_items: [
    { description: 'Bolts', quantity: '10', unit_price: '50.00', line_total: '500.00', line_tax: '37.50' },
    { description: 'Nuts', quantity: '5', unit_price: '100.00', line_total: '500.00', line_tax: null },
  ],
}

// A fetch-response-shaped resolution, matching what the real reading/supply routes return.
function ok(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

// Captured `resolve` so a test controls exactly when a reading GET settles.
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

// A successful filing navigates to the real invoice detail, whose ~40-field record this
// file has no business fabricating. Stubbed out so the landing is observed on ctx
// (view/importedInvoiceId) instead of on a screen that is not the claim.
vi.mock('./components/InvoiceDetail', () => ({
  InvoiceDetail: () => null,
}))

// Every POST body sent to the create endpoint, in call order. Parsed from the fetch init
// because apiFetch JSON.stringifies the body -- this is literally what crosses the wire.
let createBodies: Record<string, unknown>[] = []
// Every POST body sent to the supply-number endpoint, in call order.
let supplyBodies: Record<string, unknown>[] = []
// document_id query values the reading route was actually GET-ed with, in call order --
// the control every A-test leans on: enterByHand's unmodified body never touches this.
let readingRequests: string[] = []
// Per-document override for the reading GET's response; falls back to `{reading:null}`.
let readingReplies: Record<string, () => Promise<unknown>> = {}

beforeEach(() => {
  capturedCtx = undefined
  createBodies = []
  supplyBodies = []
  readingRequests = []
  readingReplies = {}
  FakeXhr.instances = []
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

// Two real entities (portfolio/v1/entities) so switchClient has somewhere to go and
// `activeEntity` resolves; everything else answers well enough not to crash a mounting
// Workspace (App.documentStages.test.tsx's routeFetch, same fallback shape).
function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; body?: string }) => {
      if (url.includes('/api/invoice/v1/imports/document/reading')) {
        const id = new URL(url).searchParams.get('document_id') ?? ''
        readingRequests.push(id)
        return readingReplies[id]?.() ?? ok({ reading: null })
      }
      if (url.endsWith('/api/invoice/v1/imports/document/invoice') && init?.method === 'POST') {
        supplyBodies.push(JSON.parse(init.body ?? '{}') as Record<string, unknown>)
        return ok({ id: CREATED_ID, invoice_number: 'N-1', status: 'draft' })
      }
      if (url.endsWith('/api/invoice/v1/invoices') && init?.method === 'POST') {
        createBodies.push(JSON.parse(init.body ?? '{}') as Record<string, unknown>)
        return Promise.resolve({
          ok: true,
          status: 201,
          json: () => Promise.resolve({ id: CREATED_ID, invoice_number: 'INV-2026-00482', status: 'draft' }),
        })
      }
      if (url.includes('/portfolio/v1/entities')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              entities: [
                entityRow(ENTITY_A, 'Hand-off Co A', '12345678-0001'),
                entityRow(ENTITY_B, 'Hand-off Co B', '12345678-0002'),
              ],
              pagination: { limit: 200, offset: 0, total: 2 },
            }),
        })
      }
      // One fallback shape for every other endpoint a mounting Workspace hits, widened
      // past App.documentStages.test.tsx's with the two keys the post-filing landing
      // needs: `clients`/`totals` for the dashboard rollup a company switch lands on,
      // `rejection_reasons` for the invoice detail a successful create navigates to.
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            entities: [],
            policies: [],
            members: [],
            roles: [],
            invoices: [],
            total: 0,
            clients: [],
            totals: EMPTY_BUCKET,
            rejection_reasons: [],
          }),
      })
    }),
  )
}

function entityRow(id: string, name: string, tin: string) {
  return {
    id,
    name,
    tin,
    registration: null,
    sector: null,
    address: null,
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
  }
}

async function renderApp() {
  routeFetch()
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

// The create form is never rendered here: the claim is about the wire, not the form UI.
// defaultDraft already carries a number, so fileDraftGate passes on the resolved entity.
async function openCreateAndSettle(entityId: string = ENTITY_A) {
  await act(async () => {
    requireCtx().openCreate()
  })
  await waitFor(() =>
    expect(requireCtx().activeEntity?.id, 'activeEntity never resolved from the entity list').toBe(entityId),
  )
}

// The one filing, plus the population floor every absence assertion below leans on: a body
// was captured at all, and it is the draft's own invoice, not some other request.
async function fileAndReadBody(): Promise<Record<string, unknown>> {
  await act(async () => {
    requireCtx().fileDraft()
  })
  await waitFor(() => expect(createBodies, 'control: the filing never reached POST /v1/invoices').toHaveLength(1))
  // The round trip completed, not just started: onCreated fired with the server's own id.
  await waitFor(() =>
    expect(requireCtx().importedInvoiceId, 'control: the filing never landed on the created invoice').toBe(CREATED_ID),
  )
  const body = createBodies[0]!
  expect(body.invoice_number, 'control: the captured request is not the draft filing').toBe('INV-2026-00482')
  return body
}

describe('EXTR-15-07: the hand-off id crosses the wire', () => {
  // Kills M-h: deleting fileDraft's `handOffDocumentId ?? undefined` 4th argument.
  it('handOffDocumentIdReachesTheCreateRequest', async () => {
    await renderApp()
    await openCreateAndSettle()

    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    // The landing is async (EXTR-27-03): a late reply must not overwrite typing, so the
    // form step is only reachable after the read settles.
    await waitFor(() => expect(requireCtx().createStep, 'enterByHand must land on the form step').toBe('form'))

    const body = await fileAndReadBody()
    expect(body.source_document_id, 'the hand-off document did not reach the create request').toBe(DOC_ID)
  })

  // The negative control for the spec above: a filing that emits the field unconditionally
  // would pass it, so this pins that a from-scratch draft omits the KEY (not sends null).
  it('aDraftTypedFromScratchSendsNoSourceDocumentIdKey', async () => {
    await renderApp()
    await openCreateAndSettle()

    const body = await fileAndReadBody()
    expect('source_document_id' in body, 'a draft with no hand-off must omit source_document_id entirely').toBe(false)
  })
})

describe('EXTR-15-07: a recorded hand-off does not outlive its create flow', () => {
  // Kills M-clear (App.tsx:582, openCreate).
  it('openCreateClearsARecordedHandOff', async () => {
    await renderApp()
    await openCreateAndSettle()

    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('form'))

    await act(async () => {
      requireCtx().openCreate()
    })
    expect(requireCtx().createStep, 'control: openCreate did not restart the flow').toBe('upload')

    const body = await fileAndReadBody()
    expect('source_document_id' in body, 'a stale hand-off survived openCreate onto an unrelated invoice').toBe(false)
  })

  // Kills M-clear (App.tsx:558, switchClient). The entity_id assertion is the control that
  // the switch itself happened, so the absence below is read off a real, different filing.
  it('switchClientClearsARecordedHandOff', async () => {
    await renderApp()
    await openCreateAndSettle()

    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('form'))

    await act(async () => {
      requireCtx().switchClient(ENTITY_B)
    })
    await waitFor(() =>
      expect(requireCtx().activeEntity?.id, 'control: switchClient never landed on the incoming company').toBe(
        ENTITY_B,
      ),
    )

    const body = await fileAndReadBody()
    expect(body.entity_id, 'control: the filing did not go under the incoming company').toBe(ENTITY_B)
    expect('source_document_id' in body, 'a stale hand-off survived a company switch').toBe(false)
  })
})

describe('EXTR-15-07: enterByHand leaves the run alone', () => {
  // Kills M-g: adding `setRun({files:[],cursor:0,status:'idle'})` to enterByHand would
  // empty the failure list the user backs out to.
  it('enterByHandKeepsTheFailureListIntact', async () => {
    await renderApp()
    await openCreateAndSettle()

    const file = new File(['%PDF-1.4 dummy'], 'invoice.pdf', { type: 'application/pdf' })
    await act(async () => {
      requireCtx().addPickedFiles([file])
    })
    expect(requireCtx().runKind, 'the picked file did not classify as a document run').toBe('document')

    // Fail the run deterministically through the upload transport's own onerror -- no
    // poll/import wire shape needed, since a rejected upload reaches neither.
    act(() => {
      requireCtx().startDocumentRun()
    })
    expect(FakeXhr.instances, 'control: the run never reached the upload transport').toHaveLength(1)
    act(() => {
      FakeXhr.last()!.fireError()
    })
    await waitFor(() =>
      expect(runFailures(requireCtx().run), 'the run must end with one failed file to back out to').toHaveLength(1),
    )
    const before = runFailures(requireCtx().run)
    expect(before[0]!.name).toBe('invoice.pdf')

    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('form'))

    const after = runFailures(requireCtx().run)
    expect(after, 'enterByHand emptied the failure list the user backs out to').toEqual(before)
    expect(requireCtx().run.files, 'enterByHand discarded the run files').toHaveLength(1)
  })
})

describe('EXTR-27-03: the hand-off carries the reading', () => {
  // While the read is in flight the flow must stay on 'upload' -- today's enterByHand sets
  // 'form' synchronously, so this reds on the very first check.
  it('EXTR27-A1: a carried reading lands the form in carried mode, after the read, with a blank number', async () => {
    await renderApp()
    await openCreateAndSettle()

    const held = deferred<unknown>()
    readingReplies[DOC_ID] = () => held.promise

    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })

    expect(requireCtx().createStep, 'the landing must wait for the read before leaving upload').toBe('upload')
    expect(requireCtx().handOffReading).toBeNull()
    expect(readingRequests, 'control: enterByHand must request the reading').toContain(DOC_ID)

    await act(async () => {
      held.resolve({ ok: true, status: 200, json: () => Promise.resolve({ reading: READING }) })
      await new Promise((r) => setTimeout(r, 0))
    })

    await waitFor(() => expect(requireCtx().createStep).toBe('form'))
    expect(requireCtx().handOffReading).toEqual(READING)
    expect(requireCtx().draft.number).toBe('')
  })

  it('EXTR27-A2: nothing to carry lands today\'s blank form', async () => {
    await renderApp()
    await openCreateAndSettle()

    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('form'))

    expect(readingRequests, 'control: enterByHand must request the reading').toContain(DOC_ID)
    expect(requireCtx().handOffReading).toBeNull()
    expect(requireCtx().draft.number).toBe('INV-2026-00482')
  })

  it('EXTR27-A3: a late reading for an earlier document is dropped', async () => {
    await renderApp()
    await openCreateAndSettle()

    const heldA = deferred<unknown>()
    readingReplies[DOC_ID] = () => heldA.promise

    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    await act(async () => {
      requireCtx().enterByHand(DOC_ID_2)
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('form'))

    expect(readingRequests, 'control: both hand-offs must request a reading').toEqual([DOC_ID, DOC_ID_2])

    await act(async () => {
      heldA.resolve({ ok: true, status: 200, json: () => Promise.resolve({ reading: READING }) })
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(requireCtx().handOffReading, 'a late reply for an earlier document must be dropped').toBeNull()
    expect(requireCtx().draft.number).toBe('INV-2026-00482')

    const body = await fileAndReadBody()
    expect(body.source_document_id, 'the filing must carry the later document').toBe(DOC_ID_2)
    expect(supplyBodies, 'a dropped read must never reach the supply route').toHaveLength(0)
  })

  it('EXTR27-A4: filing a carried draft posts the supply, not a create, and lands on the invoice', async () => {
    await renderApp()
    await openCreateAndSettle()

    readingReplies[DOC_ID] = () => ok({ reading: READING })
    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('form'))

    expect(readingRequests, 'control: enterByHand must request the reading').toContain(DOC_ID)

    act(() => {
      requireCtx().updateDraft('number', 'N-1')
    })
    act(() => {
      requireCtx().fileDraft()
    })

    await waitFor(() => expect(supplyBodies, 'the filing must post the supply route, not create').toHaveLength(1))
    expect(supplyBodies[0]).toEqual({ entity_id: ENTITY_A, document_id: DOC_ID, invoice_number: 'N-1' })
    expect(createBodies).toHaveLength(0)
    await waitFor(() => expect(requireCtx().importedInvoiceId).toBe(CREATED_ID))
    expect(window.location.pathname).toBe('/invoices/' + CREATED_ID)
  })

  describe('a clearer drops a landed reading', () => {
    async function landCarried() {
      await openCreateAndSettle()
      readingReplies[DOC_ID] = () => ok({ reading: READING })
      await act(async () => {
        requireCtx().enterByHand(DOC_ID)
      })
      await waitFor(() => expect(requireCtx().createStep).toBe('form'))
      expect(readingRequests, 'control: enterByHand must request the reading').toContain(DOC_ID)
    }

    it('EXTR27-A5a: switchClient drops the reading', async () => {
      await renderApp()
      await landCarried()

      await act(async () => {
        requireCtx().switchClient(ENTITY_B)
      })
      await waitFor(() => expect(requireCtx().activeEntity?.id).toBe(ENTITY_B))

      expect(requireCtx().handOffReading).toBeNull()
    })

    it('EXTR27-A5b: openCreate drops the reading', async () => {
      await renderApp()
      await landCarried()

      await act(async () => {
        requireCtx().openCreate()
      })

      expect(requireCtx().handOffReading).toBeNull()
    })

    it('EXTR27-A5c: skipUpload drops the reading', async () => {
      await renderApp()
      await landCarried()

      act(() => {
        requireCtx().skipUpload()
      })

      expect(requireCtx().handOffReading).toBeNull()
    })
  })

  it('EXTR27-A6: a failed read lands today\'s blank form with the document still recorded', async () => {
    await renderApp()
    await openCreateAndSettle()

    readingReplies[DOC_ID] = () =>
      Promise.resolve({ ok: false, status: 500, statusText: 'boom', json: () => Promise.resolve({ error: 'boom' }) })
    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('form'))

    expect(readingRequests, 'control: enterByHand must attempt the reading even when it will fail').toContain(DOC_ID)
    expect(requireCtx().handOffReading).toBeNull()
    expect(requireCtx().draft.number).toBe('INV-2026-00482')

    const body = await fileAndReadBody()
    expect(body.source_document_id, 'the document must still be recorded after a failed read').toBe(DOC_ID)
  })

  it('EXTR27-A7: a read still pending across a company switch lands nothing', async () => {
    await renderApp()
    await openCreateAndSettle()

    const held = deferred<unknown>()
    readingReplies[DOC_ID] = () => held.promise
    await act(async () => {
      requireCtx().enterByHand(DOC_ID)
    })

    expect(readingRequests, 'control: enterByHand must request the reading before the switch').toContain(DOC_ID)

    await act(async () => {
      requireCtx().switchClient(ENTITY_B)
    })
    await waitFor(() => expect(requireCtx().activeEntity?.id).toBe(ENTITY_B))

    await act(async () => {
      held.resolve({ ok: true, status: 200, json: () => Promise.resolve({ reading: READING }) })
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(requireCtx().handOffReading, 'a read pending across a company switch must land nothing').toBeNull()

    const body = await fileAndReadBody()
    expect(body.entity_id).toBe(ENTITY_B)
    expect('source_document_id' in body, 'a dropped read must not carry its document onto the new company').toBe(false)
    expect(supplyBodies).toHaveLength(0)
  })
})
