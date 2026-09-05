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
const CREATED_ID = 'cccccccc-0000-4000-8000-00000000000c'

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

beforeEach(() => {
  capturedCtx = undefined
  createBodies = []
  FakeXhr.instances = []
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
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
    expect(requireCtx().createStep, 'enterByHand must land on the form step').toBe('form')

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

    const after = runFailures(requireCtx().run)
    expect(after, 'enterByHand emptied the failure list the user backs out to').toEqual(before)
    expect(requireCtx().run.files, 'enterByHand discarded the run files').toHaveLength(1)
  })
})
