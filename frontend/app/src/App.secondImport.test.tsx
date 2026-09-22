// @vitest-environment jsdom
// The sole-invoice lookup waits on a promise each row resolves itself, so a row acts inside
// the landing window by construction, not by timing.
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

let captured: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    captured = p.ctx
    return null
  },
}))
// ReviewBatch reads import_batch_id= lists of its own, which would inflate the lookup count.
vi.mock('./components/ReviewBatch', () => ({ ReviewBatch: () => null }))
vi.mock('./components/InvoiceDetail', () => ({ InvoiceDetail: () => null }))
vi.mock('./components/ExtractionReview', () => ({ ExtractionReview: () => null }))
vi.mock('./components/DashboardActive', () => ({ DashboardActive: () => null }))

class FakeXhr {
  static instances: FakeXhr[] = []

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

  respond(status: number, body: unknown): void {
    this.status = status
    this.responseText = JSON.stringify(body)
    this.onload?.()
  }
}

// Node 25's native localStorage collides with jsdom's.
function memoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, value)
    },
    removeItem: (key: string) => {
      store.delete(key)
    },
    clear: () => {
      store.clear()
    },
  }
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY = 'aaaaaaaa-0000-4000-8000-000000000001'
const ENTITY_B = 'aaaaaaaa-0000-4000-8000-000000000002'
const DOC = 'dddddddd-1111-4111-8111-111111111111'
const JOB = 'eeeeeeee-1111-4111-8111-111111111111'
const BATCH = 'bbbbbbbb-1111-4111-8111-111111111111'
const INV = 'ffffffff-1111-4111-8111-111111111111'

function report(format: string) {
  return {
    id: BATCH,
    status: 'completed',
    format,
    delimiter: '',
    encoding: '',
    rows_total: 1,
    rows_valid: 1,
    rows_invalid: 0,
    ready_invoices: 1,
    quarantined_invoices: 0,
    errors: [],
    rule_set_version: null,
    invoices_clean: 1,
    invoices_with_violations: 0,
    invoice_violations: [],
  }
}

function uploadReply(filename: string) {
  return { document_id: DOC, filename, content_type: 'application/pdf', size_bytes: 14, reused: false }
}

const PREVIEW = {
  document_id: DOC,
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  columns: ['invoice_number', 'total'],
  sample_rows: [['INV-1', '100']],
  rows_total: 1,
}

function entity(id: string, name: string, tin: string) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
}

interface Reply {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function reply(status: number, body: unknown): Reply {
  return { ok: status < 400, status, json: () => Promise.resolve(body) }
}

let lookups = 0
let heldLookups: ((status: 200 | 500) => void)[] = []

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string): Promise<Reply> => {
      if (url.includes('import_batch_id=')) {
        lookups += 1
        return new Promise<Reply>((resolve) => {
          heldLookups.push((status) =>
            resolve(
              status === 200
                ? reply(200, { invoices: [{ id: INV }], pagination: { limit: 1, offset: 0, total: 1 } })
                : reply(500, { error: 'boom' }),
            ),
          )
        })
      }
      if (url.includes('/api/submission/v1/extractions?document_id=')) {
        return Promise.resolve(
          reply(200, {
            jobs: [{ id: JOB, document_id: DOC, state: 'succeeded', created_at: '2026-01-01T00:00:00Z', last_error: null, failure_kind: null }],
          }),
        )
      }
      if (url.endsWith('/api/invoice/v1/imports/document')) return Promise.resolve(reply(201, report('pdf')))
      if (url.includes('/api/invoice/v1/imports/saved-mapping?')) return Promise.resolve(reply(200, { saved_mapping: null }))
      if (url.endsWith('/api/invoice/v1/imports/suggest-mapping')) return Promise.resolve(reply(404, { error: 'not found' }))
      if (/\/api\/invoice\/v1\/invoices\/[^/?]+\/approval$/.test(url)) return Promise.resolve(reply(404, { error: 'not found' }))
      if (url.includes('/portfolio/v1/entities')) {
        return Promise.resolve(
          reply(200, {
            entities: [entity(ENTITY, 'Window Test Co', '12345678-0001'), entity(ENTITY_B, 'Second Entity Co', '87654321-0001')],
            pagination: { limit: 200, offset: 0, total: 2 },
          }),
        )
      }
      return Promise.resolve(reply(200, { entities: [], policies: [], members: [], roles: [], invoices: [], total: 0, jobs: [] }))
    }),
  )
}

beforeEach(() => {
  captured = undefined
  lookups = 0
  heldLookups = []
  FakeXhr.instances = []
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', memoryStorage())
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
  stubFetch()
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

function c(): PlatformCtx {
  expect(captured, 'Sidebar never rendered, so ctx was not captured').toBeDefined()
  return captured!
}

const pdf = (name: string) => new File([`%PDF-1.4 ${name}`], name, { type: 'application/pdf' })
const csv = (name: string) => new File(['invoice_number,total\nINV-1,100'], name, { type: 'text/csv' })
const pickedNames = () => c().pickedFiles.map((p) => p.file.name)
const runNames = () => c().run.files.map((f) => f.name)
const progressCard = () => screen.queryByTestId('import-progress')

// One macrotask boundary: the lookup-to-applyRoute chain is microtask-only.
const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })

async function releaseLookup(status: 200 | 500 = 200) {
  expect(heldLookups, 'no sole-invoice lookup is held').toHaveLength(1)
  await act(async () => heldLookups.shift()!(status))
  await flush()
}

async function boot() {
  vi.resetModules()
  const { default: App } = await import('./App')
  render(<App />)
  await act(async () => c().openCreate())
  await waitFor(() => expect(c().entityId, 'active.entityId never backfilled entityId').toBe(ENTITY))
}

async function switchTo(id: string) {
  await act(async () => c().switchClient(id))
  await waitFor(() => expect(c().activeEntity?.id).toBe(id))
}

async function assertWindowFloor() {
  await waitFor(() => expect(lookups, 'the sole-invoice lookup was never requested').toBe(1))
  expect(c().run.status).toBe('finished')
  expect(progressCard(), 'the progress card is not mounted in the window').not.toBeNull()
}

async function documentRunInWindow() {
  await boot()
  await act(async () => c().addPickedFiles([pdf('a.pdf')]))
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'the first upload was not issued').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(201, uploadReply('a.pdf')))
  await assertWindowFloor()
}

async function spreadsheetMapped() {
  await boot()
  await act(async () => c().addPickedFiles([csv('a.csv')]))
  act(() => c().readAllColumns())
  expect(FakeXhr.instances, 'the first preview was not issued').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(200, PREVIEW))
  await waitFor(() => expect(c().createStep).toBe('mapping'))
  act(() => c().armField('invoice_number'))
  act(() => c().clickCol('invoice_number'))
}

async function spreadsheetRunInWindow() {
  await spreadsheetMapped()
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'the first createImport was not issued').toHaveLength(2)
  act(() => FakeXhr.instances[1]!.respond(200, report('csv')))
  await assertWindowFloor()
}

it('BUG20-D1: inside the landing window, New invoice then Extract invoices issues the upload', async () => {
  await documentRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  act(() => c().startDocumentRun())
  expect({ xhrs: FakeXhr.instances.length, importError: c().importError }).toEqual({ xhrs: 2, importError: null })
})

it('BUG20-S1: inside the landing window, New invoice then Read columns issues the preview', async () => {
  await spreadsheetRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([csv('b.csv')]))
  act(() => c().readAllColumns())
  expect({ xhrs: FakeXhr.instances.length, importError: c().importError }).toEqual({ xhrs: 3, importError: null })
})

it('BUG20-D2: files picked inside the window survive the old document landing', async () => {
  await documentRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  await releaseLookup()
  expect({
    view: c().view,
    step: c().createStep,
    path: window.location.pathname,
    picked: pickedNames(),
    extractionJobId: c().extractionJobId,
  }).toEqual({ view: 'create', step: 'upload', path: '/create', picked: ['b.pdf'], extractionJobId: null })
})

it('BUG20-S2: files picked inside the window survive the old spreadsheet landing', async () => {
  await spreadsheetRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([csv('b.csv')]))
  await releaseLookup()
  expect({
    view: c().view,
    step: c().createStep,
    path: window.location.pathname,
    picked: pickedNames(),
    importedInvoiceId: c().importedInvoiceId,
  }).toEqual({ view: 'create', step: 'upload', path: '/create', picked: ['b.csv'], importedInvoiceId: null })
})

it('BUG20-D3: a document run started inside the window is not taken over by the old landing', async () => {
  await documentRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  act(() => c().startDocumentRun())
  await releaseLookup()
  expect({
    view: c().view,
    step: c().createStep,
    status: c().run.status,
    runFiles: runNames(),
    path: window.location.pathname,
  }).toEqual({ view: 'create', step: 'documents', status: 'running', runFiles: ['b.pdf'], path: '/create' })
  expect(progressCard(), 'the second run lost its progress card').not.toBeNull()
})

it('BUG20-S3: a spreadsheet run started inside the window is not taken over by the old landing', async () => {
  await spreadsheetRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([csv('b.csv')]))
  act(() => c().readAllColumns())
  expect(FakeXhr.instances, 'the second preview was not issued').toHaveLength(3)
  act(() => FakeXhr.instances[2]!.respond(200, PREVIEW))
  await waitFor(() => expect(c().createStep).toBe('mapping'))
  act(() => c().armField('invoice_number'))
  act(() => c().clickCol('invoice_number'))
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'the second createImport was not issued').toHaveLength(4)
  await releaseLookup()
  expect({
    view: c().view,
    status: c().run.status,
    runFiles: runNames(),
    path: window.location.pathname,
  }).toEqual({ view: 'create', status: 'running', runFiles: ['b.csv'], path: '/create' })
  expect(progressCard(), 'the second run lost its progress card').not.toBeNull()
})

it('BUG20-D5 (handler contract): a document run started without a reset supersedes the pending landing', async () => {
  await documentRunInWindow()
  act(() => c().startDocumentRun())
  await releaseLookup()
  expect({ xhrs: FakeXhr.instances.length, view: c().view, status: c().run.status }).toEqual({
    xhrs: 2,
    view: 'create',
    status: 'running',
  })
})

it('BUG20-S5 (handler contract): a spreadsheet run started without a reset supersedes the pending landing', async () => {
  await spreadsheetRunInWindow()
  act(() => c().continueMapping())
  await releaseLookup()
  expect({ xhrs: FakeXhr.instances.length, view: c().view, status: c().run.status }).toEqual({
    xhrs: 3,
    view: 'create',
    status: 'running',
  })
})

it('BUG20-D6: a double-click stays silent and does not leave the document run it started', async () => {
  await boot()
  await act(async () => c().addPickedFiles([pdf('a.pdf')]))
  const ctx = c()
  act(() => {
    ctx.startDocumentRun()
    ctx.startDocumentRun()
  })
  expect({ xhrs: FakeXhr.instances.length, importError: c().importError }).toEqual({ xhrs: 1, importError: null })
  act(() => FakeXhr.instances[0]!.respond(201, uploadReply('a.pdf')))
  await waitFor(() => expect(lookups).toBe(1))
  await releaseLookup()
  expect(window.location.pathname).toBe(`/extraction/${JOB}`)
})

it('BUG20-S6: a double-click stays silent and does not leave the spreadsheet run it started', async () => {
  await spreadsheetMapped()
  const ctx = c()
  act(() => {
    ctx.continueMapping()
    ctx.continueMapping()
  })
  expect({ xhrs: FakeXhr.instances.length, importError: c().importError }).toEqual({ xhrs: 2, importError: null })
  act(() => FakeXhr.instances[1]!.respond(200, report('csv')))
  await waitFor(() => expect(lookups).toBe(1))
  await releaseLookup()
  expect(window.location.pathname).toBe(`/invoices/${INV}`)
})

it('BUG20-D7: an undisturbed document run still lands on its extraction review', async () => {
  await documentRunInWindow()
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view }).toEqual({ path: `/extraction/${JOB}`, view: 'extraction' })
})

it('BUG20-S7: an undisturbed spreadsheet run still lands on its invoice', async () => {
  await spreadsheetRunInWindow()
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view }).toEqual({ path: `/invoices/${INV}`, view: 'detail' })
})

it('BUG20-D8: a left document run whose lookup fails still lands nothing', async () => {
  await documentRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  await releaseLookup(500)
  expect({ step: c().createStep, reviewBatchIds: c().reviewBatchIds, path: window.location.pathname }).toEqual({
    step: 'upload',
    reviewBatchIds: [],
    path: '/create',
  })
})

it('BUG20-S8: a left spreadsheet run whose lookup fails still lands nothing', async () => {
  await spreadsheetRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([csv('b.csv')]))
  await releaseLookup(500)
  expect({ step: c().createStep, reviewBatchIds: c().reviewBatchIds, path: window.location.pathname }).toEqual({
    step: 'upload',
    reviewBatchIds: [],
    path: '/create',
  })
})

it('BUG20-D8c: an undisturbed document run whose lookup fails lands on its batch review', async () => {
  await documentRunInWindow()
  await releaseLookup(500)
  expect({ step: c().createStep, path: window.location.pathname }).toEqual({ step: 'review', path: `/imports/${BATCH}/review` })
})

it('BUG20-S8c: an undisturbed spreadsheet run whose lookup fails lands on its batch review', async () => {
  await spreadsheetRunInWindow()
  await releaseLookup(500)
  expect({ step: c().createStep, path: window.location.pathname }).toEqual({ step: 'review', path: `/imports/${BATCH}/review` })
})

it('BUG20-D9: a company switch inside the window drops the document landing', async () => {
  await documentRunInWindow()
  await switchTo(ENTITY_B)
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view, extractionJobId: c().extractionJobId }).toEqual({
    path: '/',
    view: 'dashboard',
    extractionJobId: null,
  })
})

it('BUG20-S9: a company switch inside the window drops the spreadsheet landing', async () => {
  await spreadsheetRunInWindow()
  await switchTo(ENTITY_B)
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view, importedInvoiceId: c().importedInvoiceId }).toEqual({
    path: '/',
    view: 'dashboard',
    importedInvoiceId: null,
  })
})

it('BUG20-D10: a company switch mid-run leaves the document run: no run state, no landing', async () => {
  await boot()
  await act(async () => c().addPickedFiles([pdf('a.pdf')]))
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'the upload was not issued').toHaveLength(1)
  expect(c().run.status).toBe('running')
  await switchTo(ENTITY_B)
  act(() => FakeXhr.instances[0]!.respond(201, uploadReply('a.pdf')))
  await waitFor(() => expect(lookups, 'the sole-invoice lookup was never requested').toBe(1))
  expect(c().run.status, 'the left run still writes run state').toBe('idle')
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view, extractionJobId: c().extractionJobId }).toEqual({
    path: '/',
    view: 'dashboard',
    extractionJobId: null,
  })
})

it('BUG20-S10: a company switch mid-run leaves the spreadsheet run: no run state, no landing', async () => {
  await spreadsheetMapped()
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'createImport was not issued').toHaveLength(2)
  expect(c().run.status).toBe('running')
  await switchTo(ENTITY_B)
  act(() => FakeXhr.instances[1]!.respond(200, report('csv')))
  await waitFor(() => expect(lookups, 'the sole-invoice lookup was never requested').toBe(1))
  expect(c().run.status, 'the left run still writes run state').toBe('idle')
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view, importedInvoiceId: c().importedInvoiceId }).toEqual({
    path: '/',
    view: 'dashboard',
    importedInvoiceId: null,
  })
})
