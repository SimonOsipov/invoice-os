// @vitest-environment jsdom
// The sole-invoice lookup waits on a promise each row resolves itself, so a row acts inside
// the landing window by construction, not by timing.
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
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
const FILED = 'ffffffff-2222-4222-8222-222222222222'

// Copied from STILL_WORKING in App.tsx.
const STILL_WORKING_COPY = 'An import or filing is still in progress. Try again when it finishes.'

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
let posts = 0
let heldPosts: ((status: 201 | 409) => void)[] = []

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string }): Promise<Reply> => {
      if (init?.method === 'POST' && url.endsWith('/api/invoice/v1/invoices')) {
        posts += 1
        return new Promise<Reply>((resolve) => {
          heldPosts.push((status) =>
            resolve(
              status === 201
                ? reply(201, { id: FILED, invoice_number: 'INV-2026-00482', status: 'draft' })
                : reply(409, { error: 'duplicate invoice number' }),
            ),
          )
        })
      }
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
  posts = 0
  heldPosts = []
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

async function documentRunHeld() {
  await boot()
  await act(async () => c().addPickedFiles([pdf('a.pdf')]))
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'the upload was not issued').toHaveLength(1)
  expect(c().run.status).toBe('running')
}

async function previewHeld() {
  await boot()
  await act(async () => c().addPickedFiles([csv('a.csv')]))
  act(() => c().readAllColumns())
  expect(FakeXhr.instances, 'the preview was not issued').toHaveLength(1)
}

async function filingHeld() {
  await boot()
  await waitFor(() => expect(c().activeEntity?.id, 'activeEntity never resolved').toBe(ENTITY))
  act(() => c().skipUpload())
  expect(c().createStep).toBe('form')
  await act(async () => c().fileDraft())
  await waitFor(() => expect(posts, 'the filing POST was not issued').toBe(1))
  expect(c().filing, 'the filing is not pending').toBe(true)
}

async function releaseFiling(status: 201 | 409) {
  expect(heldPosts, 'no filing POST is held').toHaveLength(1)
  await act(async () => heldPosts.shift()!(status))
  await flush()
}

function refusalsIn(el: HTMLElement | null): HTMLElement[] {
  expect(el, 'the surface that carries the refusal is not mounted').not.toBeNull()
  return within(el!).queryAllByText(STILL_WORKING_COPY)
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

it('BUG20-QA1 (handler contract): restartImport inside the window drops the spreadsheet landing', async () => {
  await spreadsheetRunInWindow()
  await act(async () => c().restartImport())
  await releaseLookup()
  expect({
    view: c().view,
    step: c().createStep,
    path: window.location.pathname,
    importedInvoiceId: c().importedInvoiceId,
  }).toEqual({ view: 'create', step: 'upload', path: '/create', importedInvoiceId: null })
})

it('BUG20-QA2 (handler contract): restartImport inside the window drops the document landing', async () => {
  await documentRunInWindow()
  await act(async () => c().restartImport())
  await releaseLookup()
  expect({
    view: c().view,
    step: c().createStep,
    path: window.location.pathname,
    extractionJobId: c().extractionJobId,
  }).toEqual({ view: 'create', step: 'upload', path: '/create', extractionJobId: null })
})

it('BUG20-QA3 (handler contract): a document start refused by its entry guard keeps the spreadsheet landing', async () => {
  await spreadsheetRunInWindow()
  expect(pickedNames(), 'the refusal needs a spreadsheet selection').toEqual(['a.csv'])
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'a spreadsheet selection started a document upload').toHaveLength(2)
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view }).toEqual({ path: `/invoices/${INV}`, view: 'detail' })
})

it('BUG20-QA4 (handler contract): a spreadsheet start refused by its entry guard keeps its landing', async () => {
  await spreadsheetRunInWindow()
  // base == null is the only entry-guard clause a mapped window can fail.
  vi.stubEnv('VITE_GATEWAY_URL', '')
  act(() => c().continueMapping())
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  expect(FakeXhr.instances, 'a gateway-less start issued a createImport').toHaveLength(2)
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view }).toEqual({ path: `/invoices/${INV}`, view: 'detail' })
})

it('BUG20-QA5: a company switch mid-run drops the unread-file row of a left spreadsheet run', async () => {
  await boot()
  await act(async () => c().addPickedFiles([csv('a.csv')]))
  act(() => c().readAllColumns())
  expect(FakeXhr.instances, 'the preview was not issued').toHaveLength(1)
  // Added after the preview started, so it reaches the run with no document id.
  await act(async () => c().addPickedFiles([csv('b.csv')]))
  act(() => FakeXhr.instances[0]!.respond(200, PREVIEW))
  await waitFor(() => expect(c().createStep).toBe('mapping'))
  expect(c().pickedFiles.map((p) => [p.file.name, p.documentId ?? null])).toEqual([
    ['a.csv', DOC],
    ['b.csv', null],
  ])
  act(() => c().armField('invoice_number'))
  act(() => c().clickCol('invoice_number'))
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'createImport was not issued').toHaveLength(2)
  expect(runNames()).toEqual(['a.csv', 'b.csv'])
  await switchTo(ENTITY_B)
  act(() => FakeXhr.instances[1]!.respond(200, report('csv')))
  await flush()
  expect({
    status: c().run.status,
    path: window.location.pathname,
    view: c().view,
    reviewBatchIds: c().reviewBatchIds,
    lookups,
  }).toEqual({ status: 'idle', path: '/', view: 'dashboard', reviewBatchIds: [], lookups: 0 })
})

it('BUG20-QA6: an undisturbed spreadsheet run whose only file fails lands back on the map step', async () => {
  await spreadsheetMapped()
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'createImport was not issued').toHaveLength(2)
  act(() => FakeXhr.instances[1]!.respond(500, { error: 'boom' }))
  await flush()
  expect({
    status: c().run.status,
    outcomes: c().run.files.map((f) => f.outcome.kind),
    step: c().createStep,
    path: window.location.pathname,
    lookups,
  }).toEqual({ status: 'failed', outcomes: ['failed'], step: 'mapping', path: '/create', lookups: 0 })
})

it('BUG20-QA7: an undisturbed document run whose only file fails lands back on the documents step', async () => {
  await boot()
  await act(async () => c().addPickedFiles([pdf('a.pdf')]))
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'the upload was not issued').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(500, { error: 'boom' }))
  await flush()
  expect({
    status: c().run.status,
    outcomes: c().run.files.map((f) => f.outcome.kind),
    step: c().createStep,
    path: window.location.pathname,
    lookups,
  }).toEqual({ status: 'failed', outcomes: ['failed'], step: 'documents', path: '/create', lookups: 0 })
})

it('BUG20-QA8: an undisturbed spreadsheet run with two ready invoices lands on its batch review', async () => {
  await spreadsheetMapped()
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'createImport was not issued').toHaveLength(2)
  act(() => FakeXhr.instances[1]!.respond(200, { ...report('csv'), ready_invoices: 2, invoices_clean: 2 }))
  await flush()
  expect({
    step: c().createStep,
    reviewBatchIds: c().reviewBatchIds,
    path: window.location.pathname,
    lookups,
  }).toEqual({ step: 'review', reviewBatchIds: [BATCH], path: `/imports/${BATCH}/review`, lookups: 0 })
})

it('BUG20-QA9: an undisturbed two-document run lands on its batch review', async () => {
  await boot()
  await act(async () => c().addPickedFiles([pdf('a.pdf'), pdf('b.pdf')]))
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'both uploads were not issued').toHaveLength(2)
  act(() => {
    FakeXhr.instances[0]!.respond(201, uploadReply('a.pdf'))
    FakeXhr.instances[1]!.respond(201, uploadReply('b.pdf'))
  })
  await flush()
  expect({ step: c().createStep, reviewBatchIds: c().reviewBatchIds, status: c().run.status, lookups }).toEqual({
    step: 'review',
    reviewBatchIds: [BATCH, BATCH],
    status: 'idle',
    lookups: 0,
  })
})

it('BUG20-QA10: files picked inside the window are on the upload step after the old document landing is dropped', async () => {
  await documentRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  await releaseLookup()
  expect(progressCard(), 'a progress card covers the upload step').toBeNull()
  expect(screen.queryAllByText('b.pdf'), 'b.pdf is not on screen').not.toHaveLength(0)
})

it('BUG20-QA11: files picked inside the window are on the upload step after the old spreadsheet landing is dropped', async () => {
  await spreadsheetRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([csv('b.csv')]))
  await releaseLookup()
  expect(progressCard(), 'a progress card covers the upload step').toBeNull()
  expect(screen.queryAllByText('b.csv'), 'b.csv is not on screen').not.toHaveLength(0)
})

it('BUG20-QA12: a document run started inside the window keeps its lock and lands on its own', async () => {
  await documentRunInWindow()
  await act(async () => c().openCreate())
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'the second upload was not issued').toHaveLength(2)
  await releaseLookup()
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'the old landing freed the lock the second run holds').toHaveLength(2)
  act(() => FakeXhr.instances[1]!.respond(201, uploadReply('b.pdf')))
  await waitFor(() => expect(lookups, 'the second run never requested its lookup').toBe(2))
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view }).toEqual({ path: `/extraction/${JOB}`, view: 'extraction' })
})

it('BUG20-QA13: a spreadsheet run started inside the window keeps its lock and lands on its own', async () => {
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
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'the old landing freed the lock the second run holds').toHaveLength(4)
  act(() => FakeXhr.instances[3]!.respond(200, report('csv')))
  await waitFor(() => expect(lookups, 'the second run never requested its lookup').toBe(2))
  await releaseLookup()
  expect({ path: window.location.pathname, view: c().view }).toEqual({ path: `/invoices/${INV}`, view: 'detail' })
})

it('BUG20-D11: New invoice mid document run keeps the run, says why, and the run lands as before', async () => {
  await documentRunHeld()
  await act(async () => c().openCreate())
  expect({
    view: c().view,
    step: c().createStep,
    status: c().run.status,
    picked: pickedNames(),
    importError: c().importError?.message ?? null,
  }).toEqual({ view: 'create', step: 'documents', status: 'running', picked: ['a.pdf'], importError: STILL_WORKING_COPY })
  expect(refusalsIn(progressCard()), 'the refusal is not inside import-progress').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(201, uploadReply('a.pdf')))
  await waitFor(() => expect(lookups, 'the sole-invoice lookup was never requested').toBe(1))
  expect(c().importError, 'the lock release left the refusal up').toBeNull()
  await releaseLookup()
  expect(window.location.pathname).toBe(`/extraction/${JOB}`)
})

it('BUG20-S11: New invoice mid spreadsheet run keeps the run; its failure and review land as before', async () => {
  await boot()
  await act(async () => c().addPickedFiles([csv('a.csv'), csv('c.csv')]))
  act(() => c().readAllColumns())
  expect(FakeXhr.instances, 'the a.csv preview was not issued').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(200, PREVIEW))
  await waitFor(() => expect(FakeXhr.instances, 'the c.csv preview was not issued').toHaveLength(2))
  act(() => FakeXhr.instances[1]!.respond(200, PREVIEW))
  await waitFor(() => expect(c().createStep).toBe('mapping'))
  expect(c().groups, 'the two previews are not one group').toHaveLength(1)
  act(() => c().armField('invoice_number'))
  act(() => c().clickCol('invoice_number'))
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'the a.csv createImport was not issued').toHaveLength(3)
  expect(c().run.status).toBe('running')
  await act(async () => c().openCreate())
  expect({
    view: c().view,
    step: c().createStep,
    status: c().run.status,
    picked: pickedNames(),
    importError: c().importError?.message ?? null,
  }).toEqual({ view: 'create', step: 'mapping', status: 'running', picked: ['a.csv', 'c.csv'], importError: STILL_WORKING_COPY })
  expect(refusalsIn(progressCard()), 'the refusal is not inside import-progress').toHaveLength(1)
  act(() => FakeXhr.instances[2]!.respond(200, report('csv')))
  await waitFor(() => expect(FakeXhr.instances, 'the c.csv createImport was not issued').toHaveLength(4))
  act(() => FakeXhr.instances[3]!.respond(500, { error: 'boom c.csv' }))
  await flush()
  expect({
    step: c().createStep,
    files: c().run.files.map((f) => (f.outcome.kind === 'failed' ? [f.name, f.outcome.kind, f.outcome.message] : [f.name, f.outcome.kind])),
    reviewBatchIds: c().reviewBatchIds,
    path: window.location.pathname,
    importError: c().importError,
    lookups,
  }).toEqual({
    step: 'review',
    files: [
      ['a.csv', 'imported'],
      ['c.csv', 'failed', 'boom c.csv'],
    ],
    reviewBatchIds: [BATCH],
    path: `/imports/${BATCH}/review`,
    importError: null,
    lookups: 0,
  })
})

it('BUG20-P1: New invoice during a Read columns preview keeps it and says why', async () => {
  await previewHeld()
  await act(async () => c().openCreate())
  expect({ step: c().createStep, picked: pickedNames(), importError: c().importError?.message ?? null }).toEqual({
    step: 'upload',
    picked: ['a.csv'],
    importError: STILL_WORKING_COPY,
  })
  expect(refusalsIn(document.body), 'the refusal is not on screen').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(200, PREVIEW))
  await flush()
  expect({ step: c().createStep, importError: c().importError }).toEqual({ step: 'mapping', importError: null })
})

it('BUG20-F1: New invoice during a filing keeps the form and says why', async () => {
  await filingHeld()
  await act(async () => c().openCreate())
  expect({
    view: c().view,
    step: c().createStep,
    filing: c().filing,
    filingError: c().filingError?.message ?? null,
    importError: c().importError,
  }).toEqual({ view: 'create', step: 'form', filing: true, filingError: STILL_WORKING_COPY, importError: null })
  expect(refusalsIn(document.body), 'the refusal is not on screen').toHaveLength(1)
  await releaseFiling(201)
  expect({ path: window.location.pathname, filingError: c().filingError, filing: c().filing }).toEqual({
    path: `/invoices/${FILED}`,
    filingError: null,
    filing: false,
  })
})

it("BUG20-F3: a filing that fails after the refusal keeps the server's message", async () => {
  await filingHeld()
  await act(async () => c().openCreate())
  const refused = c().filingError?.message ?? null
  await releaseFiling(409)
  expect({ refused, step: c().createStep, filing: c().filing, filingError: c().filingError?.message ?? null }).toEqual({
    refused: STILL_WORKING_COPY,
    step: 'form',
    filing: false,
    filingError: 'duplicate invoice number',
  })
})

it("BUG20-P2: a preview that fails after the refusal keeps the file's message", async () => {
  await previewHeld()
  await act(async () => c().openCreate())
  const refused = c().importError?.message ?? null
  act(() => FakeXhr.instances[0]!.respond(500, { error: 'boom' }))
  await flush()
  expect({ refused, step: c().createStep, importError: c().importError?.message ?? null }).toEqual({
    refused: STILL_WORKING_COPY,
    step: 'upload',
    importError: 'a.csv: boom',
  })
})

it('BUG20-D12: after a company switch mid-run, Extract invoices says why, and works once the old run releases', async () => {
  await documentRunHeld()
  await switchTo(ENTITY_B)
  await act(async () => c().openCreate())
  expect({ step: c().createStep, importError: c().importError }).toEqual({ step: 'upload', importError: null })
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  expect({ picked: pickedNames(), entityId: c().entityId }).toEqual({ picked: ['b.pdf'], entityId: ENTITY_B })
  act(() => c().startDocumentRun())
  expect({ xhrs: FakeXhr.instances.length, importError: c().importError?.message ?? null }).toEqual({
    xhrs: 1,
    importError: STILL_WORKING_COPY,
  })
  expect(refusalsIn(document.body), 'the refusal is not on screen').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(201, uploadReply('a.pdf')))
  await waitFor(() => expect(lookups, 'the old run never requested its lookup').toBe(1))
  expect(c().importError, 'the lock release left the refusal up').toBeNull()
  act(() => c().startDocumentRun())
  expect(FakeXhr.instances, 'the released lock still refuses Extract invoices').toHaveLength(2)
})

it('BUG20-S12: after a company switch mid-run, Read columns says why, and works once the old run releases', async () => {
  await spreadsheetMapped()
  act(() => c().continueMapping())
  expect(FakeXhr.instances, 'createImport was not issued').toHaveLength(2)
  expect(c().run.status).toBe('running')
  await switchTo(ENTITY_B)
  await act(async () => c().openCreate())
  expect({ step: c().createStep, importError: c().importError }).toEqual({ step: 'upload', importError: null })
  await act(async () => c().addPickedFiles([csv('b.csv')]))
  expect(pickedNames()).toEqual(['b.csv'])
  act(() => c().readAllColumns())
  expect({ xhrs: FakeXhr.instances.length, importError: c().importError?.message ?? null }).toEqual({
    xhrs: 2,
    importError: STILL_WORKING_COPY,
  })
  act(() => FakeXhr.instances[1]!.respond(200, report('csv')))
  await waitFor(() => expect(lookups, 'the old run never requested its lookup').toBe(1))
  expect(c().importError, 'the lock release left the refusal up').toBeNull()
  act(() => c().readAllColumns())
  expect(FakeXhr.instances, 'the released lock still refuses Read columns').toHaveLength(3)
})

it('BUG20-P3: Extract invoices while a Read columns preview holds the lock says why', async () => {
  await previewHeld()
  expect(pickedNames()).toEqual(['a.csv'])
  await act(async () => c().removePickedFile(c().pickedFiles[0]!.id))
  await act(async () => c().addPickedFiles([pdf('b.pdf')]))
  expect(pickedNames(), 'the selection is not the one PDF').toEqual(['b.pdf'])
  act(() => c().startDocumentRun())
  expect({ xhrs: FakeXhr.instances.length, importError: c().importError?.message ?? null }).toEqual({
    xhrs: 1,
    importError: STILL_WORKING_COPY,
  })
  expect(refusalsIn(document.body), 'the refusal is not on screen').toHaveLength(1)
})

it('BUG20-F2: after a company switch mid-run, File on the form says why', async () => {
  await documentRunHeld()
  await switchTo(ENTITY_B)
  await act(async () => c().openCreate())
  act(() => c().skipUpload())
  expect({ step: c().createStep, status: c().run.status }).toEqual({ step: 'form', status: 'idle' })
  await act(async () => c().fileDraft())
  expect({ posts, filingError: c().filingError?.message ?? null, importError: c().importError }).toEqual({
    posts: 0,
    filingError: STILL_WORKING_COPY,
    importError: null,
  })
  expect(refusalsIn(document.body), 'the refusal is not on screen').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(201, uploadReply('a.pdf')))
  await waitFor(() => expect(lookups, 'the old run never requested its lookup').toBe(1))
  expect(c().filingError, 'the lock release left the refusal up').toBeNull()
})

it('BUG20-P4: New invoice during a preview, after Skip — enter manually, says why on the form', async () => {
  await previewHeld()
  act(() => c().skipUpload())
  expect(c().createStep).toBe('form')
  await act(async () => c().openCreate())
  expect({
    view: c().view,
    step: c().createStep,
    filingError: c().filingError?.message ?? null,
    importError: c().importError,
  }).toEqual({ view: 'create', step: 'form', filingError: STILL_WORKING_COPY, importError: null })
  expect(refusalsIn(document.body), 'the refusal is not on screen').toHaveLength(1)
  act(() => FakeXhr.instances[0]!.respond(200, PREVIEW))
  await flush()
  expect({ filingError: c().filingError, importError: c().importError }).toEqual({ filingError: null, importError: null })
})

it('BUG20-D13 (handler contract): the progress card outranks the form step', async () => {
  await documentRunHeld()
  act(() => c().skipUpload())
  expect({ step: c().createStep, status: c().run.status }).toEqual({ step: 'form', status: 'running' })
  await act(async () => c().openCreate())
  expect({
    step: c().createStep,
    status: c().run.status,
    importError: c().importError?.message ?? null,
    filingError: c().filingError,
  }).toEqual({ step: 'form', status: 'running', importError: STILL_WORKING_COPY, filingError: null })
  expect(refusalsIn(progressCard()), 'the refusal is not inside import-progress').toHaveLength(1)
})
