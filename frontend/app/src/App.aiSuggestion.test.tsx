// @vitest-environment jsdom
// Suggest-after-restore wiring (step 4 of readAllColumns, after restoreGroups). routeFetch
// answers both the saved-mapping lookup and the suggest POST; FakeXhr serves preview and
// createImport, same split as App.savedMapping.test.tsx.

import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import type { SavedMapping, SuggestMapping } from './lib/importApi'
import { initMappingFromHeaders, restoreMapping, toImportMapping } from './lib/mapping'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
const ENTITY_B = 'bbbbbbbb-0000-4000-8000-000000000002'
const DEFAULT_ENTITIES = [{ id: ENTITY_A, name: 'Mirror Co', tin: '12345678-0001' }]

// Column names deliberately avoid every ALIAS entry (lib/mapping.ts) so a placement can
// only appear via a genuine restore/suggestion, never recognize()'s automatic seed --
// except NONE_COLS below, which deliberately DOES alias-match for the one spec that needs it.
const TWO_COL = ['Invoice No', 'Client Ref']
const ALT_COL = ['Ref Number', 'Total Due']
const THREE_COL = ['Invoice No', 'Client Ref', 'Amount Due']
const FOUR_COL = ['Invoice No', 'Client Ref', 'Amount Due', 'Backup Amount']
const ROW1_COL = ['Invoice No', 'Junk']
const ROW3_COL = ['Invoice No', 'Amount Due']
const ROW3_COL_ALT = ['Invoice No', 'Backup Amount']
const NONE_COLS = ['Invoice No', 'Total']

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

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

const suggestBodies: { entity_id: string; document_id: string }[] = []

beforeEach(() => {
  capturedCtx = undefined
  suggestBodies.length = 0
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt() {
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

async function openCreateAndWaitForEntity() {
  act(() => {
    requireCtx().openCreate()
  })
  await waitFor(() => expect(requireCtx().entityId, 'the click-time entity never resolved').toBe(ENTITY_A))
}

// Records createImport's multipart body so mapping/header_row can be asserted.
class FakeXhr {
  static instances: FakeXhr[] = []
  status = 0
  responseText = ''
  method = ''
  url = ''
  body: FormData | null = null
  upload: { onprogress: (() => void) | null; onload: (() => void) | null } = { onprogress: null, onload: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null

  constructor() {
    FakeXhr.instances.push(this)
  }

  open(method: string, url: string): void {
    this.method = method
    this.url = url
  }
  setRequestHeader(): void {}
  send(body: FormData): void {
    this.body = body
  }

  respond(status: number, body: unknown): void {
    this.status = status
    this.responseText = JSON.stringify(body)
    this.onload?.()
  }
}

type SavedMappingFetchResult = { ok: boolean; status: number; json: () => Promise<unknown> }
type SavedMappingAnswer = () => Promise<SavedMappingFetchResult>

function okAnswer(saved: SavedMapping | null): SavedMappingAnswer {
  return () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ saved_mapping: saved }) })
}

type SuggestFetchResult = { ok: boolean; status: number; json: () => Promise<unknown> }
type SuggestAnswer = () => Promise<SuggestFetchResult>

function suggestOk(res: SuggestMapping): SuggestAnswer {
  return () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(res) })
}

function suggestFail(status: number): SuggestAnswer {
  return () => Promise.resolve({ ok: false, status, json: () => Promise.resolve({ error: 'boom' }) })
}

// A transport rejection, not a non-2xx answer -- a different input class from suggestFail.
function suggestThrow(): SuggestAnswer {
  return () => Promise.reject(new TypeError('Failed to fetch'))
}

const DEFAULT_SUGGEST_NONE: SuggestMapping = {
  source: 'none',
  header_row: 1,
  columns: [],
  sample_rows: [],
  rows_total: 0,
  mapping: {},
  saved_at: null,
}

function entityRow(id: string, name: string, tin: string) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
}

function routeFetch(
  savedMappingByDoc: Record<string, SavedMappingAnswer>,
  entities: { id: string; name: string; tin: string }[],
  suggestByDoc: Record<string, SuggestAnswer>,
) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; body?: string }) => {
      if (url.includes('/portfolio/v1/entities')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              entities: entities.map((e) => entityRow(e.id, e.name, e.tin)),
              pagination: { limit: 200, offset: 0, total: entities.length },
            }),
        })
      }
      if (url.includes('/api/invoice/v1/imports/suggest-mapping')) {
        const body = JSON.parse(init!.body!) as { entity_id: string; document_id: string }
        suggestBodies.push(body)
        const answer = suggestByDoc[body.document_id]
        if (!answer) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(DEFAULT_SUGGEST_NONE) })
        return answer()
      }
      if (url.includes('/api/invoice/v1/imports/saved-mapping')) {
        const m = /document_id=([^&]+)/.exec(url)
        const documentId = m ? decodeURIComponent(m[1]) : ''
        const answer = savedMappingByDoc[documentId]
        if (!answer) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ saved_mapping: null }) })
        return answer()
      }
      // Wide enough that a switchClient-driven dashboard render (AIRA-10) never throws --
      // scopedBucket (lib/dashboard.ts) reads rollup.clients/.totals off this same body.
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
            clients: [],
            totals: EMPTY_BUCKET,
            total: 0,
          }),
      })
    }),
  )
}

async function bootAtWithGateway(
  savedMappingByDoc: Record<string, SavedMappingAnswer>,
  entities: { id: string; name: string; tin: string }[] = DEFAULT_ENTITIES,
  suggestByDoc: Record<string, SuggestAnswer> = {},
) {
  routeFetch(savedMappingByDoc, entities, suggestByDoc)
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  return bootAt()
}

function csvFile(name: string, headers: string[]): File {
  return new File([headers.join(',') + '\n'], name, { type: 'text/csv' })
}

function previewReply(documentId: string, columns: string[]) {
  return {
    document_id: documentId,
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    columns,
    sample_rows: [columns.map(() => 'x')],
    rows_total: 1,
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

describe('the AI suggests a mapping after a saved lookup restores nothing', () => {
  it('AIRA-01: a group whose saved lookup hits makes no suggest call', async () => {
    FakeXhr.instances = []
    const SAVE_HIT: SavedMapping = { mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-09-01T10:15:00Z' }
    const suggestForB: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: ALT_COL,
      sample_rows: [['x', 'y']],
      rows_total: 1,
      mapping: { invoice_number: 'Ref Number' },
      saved_at: null,
    }
    await bootAtWithGateway({ 'doc-a1': okAnswer(SAVE_HIT) }, DEFAULT_ENTITIES, { 'doc-b1': suggestOk(suggestForB) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL), csvFile('b.csv', ALT_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview a never reached the transport').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a1', TWO_COL))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview b never reached the transport').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-b1', ALT_COL))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    // Positive control lives in the same length: a broken routeFetch arm that suggests
    // NOTHING would also read as "the restored group made no call" -- length 1 for doc-b1
    // proves the mechanism fires, not just that it stayed silent for doc-a1.
    expect(suggestBodies, 'only the unsaved group may record a suggest call').toHaveLength(1)
    expect(suggestBodies[0]!.document_id, 'the one call must be for the unsaved group, not the restored one').toBe('doc-b1')
    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'the restored group must still render its notice').not.toBeNull()
  })

  it('AIRA-02: two files of one layout make one suggest call; two layouts make two', async () => {
    FakeXhr.instances = []
    const resP: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: TWO_COL,
      sample_rows: [['x', 'y']],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: null,
    }
    const resQ: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: ALT_COL,
      sample_rows: [['x', 'y']],
      rows_total: 1,
      mapping: { invoice_number: 'Ref Number' },
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a1': suggestOk(resP), 'doc-b1': suggestOk(resQ) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a1.csv', TWO_COL), csvFile('a2.csv', TWO_COL), csvFile('b1.csv', ALT_COL), csvFile('b2.csv', ALT_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview a1 never reached the transport').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a1', TWO_COL))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview a2 never reached the transport').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-a2', TWO_COL))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview b1 never reached the transport').toHaveLength(3))
    act(() => {
      FakeXhr.instances[2]!.respond(200, previewReply('doc-b1', ALT_COL))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview b2 never reached the transport').toHaveLength(4))
    act(() => {
      FakeXhr.instances[3]!.respond(200, previewReply('doc-b2', ALT_COL))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    expect(suggestBodies, 'exactly one suggest call per group, not per file').toHaveLength(2)
    expect(suggestBodies.map((b) => b.document_id), "the two calls must be for each group's first file, in group order").toEqual([
      'doc-a1',
      'doc-b1',
    ])
  })

  it('AIRA-03: the Map step opens with the suggestion placed', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: TWO_COL,
      sample_rows: [['INV-1', 'C-1']],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a3': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a3', TWO_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    const columns = Array.from(document.querySelectorAll('[data-testid="map-column"]'))
    expect(columns, 'control: the column grid must render one column per header').toHaveLength(TWO_COL.length)
    const invoiceCol = columns.find((c) => c.querySelector('div.mono')?.textContent === 'Invoice No')
    expect(invoiceCol, 'the suggested column must render').not.toBeUndefined()
    const chip = invoiceCol!.querySelector('span[draggable]')
    expect(chip, 'the suggestion must place a chip on Invoice No').not.toBeNull()
    expect(chip!.querySelector('.mono')?.textContent, 'the placed field must read invoice_number').toBe('invoice_number')

    const paletteChip = Array.from(document.querySelectorAll('button[draggable]')).find((b) => b.textContent?.includes('invoice_number'))
    expect(paletteChip, 'a suggested invoice_number must not sit in the palette').toBeUndefined()

    const importButton = Array.from(document.querySelectorAll('button')).find((b) => b.textContent === 'Import 1 rows')
    expect(importButton, 'the continue control must read Import 1 rows once the suggestion places invoice_number').not.toBeUndefined()
  })

  it('AIRA-04: Continue confirms the suggestion and imports it verbatim', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: THREE_COL,
      sample_rows: [['INV-1', 'C-1', '900']],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No', subtotal: 'Amount Due' },
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a4': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', THREE_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a4', THREE_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    const expectedMapping = restoreMapping(THREE_COL, res.mapping)
    expect(
      requireCtx().groups[0]?.mapping,
      'the suggestion must be applied to the group mapping before Continue can import it',
    ).toEqual(expectedMapping)

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'the import must reach the transport').toHaveLength(2))
    const body = FakeXhr.instances[1]!.body!
    expect(JSON.parse(body.get('mapping') as string), 'the wire mapping must equal the suggestion verbatim').toEqual(
      toImportMapping(expectedMapping),
    )
  })

  it('AIRA-05: an edit to a suggested placement reaches the wire', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: FOUR_COL,
      sample_rows: [['INV-1', 'C-1', '900', '950']],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No', buyer_name: 'Client Ref', subtotal: 'Amount Due' },
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a5': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', FOUR_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a5', FOUR_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    const before = requireCtx().groups[0]?.mapping
    expect(before?.invoice_number, 'the suggestion must place invoice_number').toBe('Invoice No')
    expect(before?.buyer_name, 'the suggestion must place buyer_name').toBe('Client Ref')
    expect(before?.subtotal, 'the suggestion must place subtotal, so moving it below is meaningful').toBe('Amount Due')

    act(() => {
      requireCtx().armField('subtotal')
    })
    act(() => {
      requireCtx().clickCol('Backup Amount')
    })

    const columns = Array.from(document.querySelectorAll('[data-testid="map-column"]'))
    const byHeader = new Map<string, Element>()
    columns.forEach((col) => byHeader.set(col.querySelector('div.mono')?.textContent ?? '', col))

    expect(
      byHeader.get('Backup Amount')!.querySelector('span[draggable] .mono')?.textContent,
      'the moved field must land on Backup Amount',
    ).toBe('subtotal')
    expect(byHeader.get('Amount Due')!.querySelector('span[draggable]'), 'the vacated column must hold no chip').toBeNull()
    expect(
      byHeader.get('Client Ref')!.querySelector('span[draggable] .mono')?.textContent,
      "buyer_name's chip must be untouched by the move",
    ).toBe('buyer_name')

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'the import must reach the transport').toHaveLength(2))
    const body = FakeXhr.instances[1]!.body!
    expect(JSON.parse(body.get('mapping') as string), 'the wire must carry the edited placement, not the suggested one').toEqual({
      invoice_number: 'Invoice No',
      buyer_name: 'Client Ref',
      subtotal: 'Backup Amount',
    })
  })

  it('AIRA-06: a header_row above 1 re-renders the grid from that row and posts it', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 3,
      columns: ROW3_COL,
      sample_rows: [['INV-3', '900']],
      rows_total: 5,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a6': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', ROW1_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a6', ROW1_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    expect(requireCtx().groups[0]?.preview.columns, 'the suggested row must replace the row-1 preview').toEqual(ROW3_COL)

    // CreateMapping.tsx renders exactly one main div.mono per column (the header cell).
    const headerCells = document.querySelectorAll('main div.mono')
    expect(headerCells.length, 'control: the header cells must render').toBeGreaterThan(0)
    expect(headerCells).toHaveLength(ROW3_COL.length)
    expect(Array.from(headerCells).map((c) => c.textContent)).toEqual(ROW3_COL)

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'the import must reach the transport').toHaveLength(2))
    expect(FakeXhr.instances[1]!.body!.get('header_row'), 'the resolved header row must be sent').toBe('3')
  })

  it('AIRA-07: a header_row of 1 posts no header_row part', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: TWO_COL,
      sample_rows: [['INV-1', 'C-1']],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a7': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a7', TWO_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    expect(
      requireCtx().groups[0]?.mapping.invoice_number,
      'the suggestion must place invoice_number before Continue can import',
    ).toBe('Invoice No')

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'the import must reach the transport').toHaveLength(2))
    const body = FakeXhr.instances[1]!.body!
    expect(body.get('mapping'), 'control: the request must carry a real mapping part').not.toBeNull()
    expect(body.get('header_row'), 'row 1 must send no header_row part').toBeNull()
  })

  it('AIRA-08: source "none" opens exactly today\'s Map step', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'none',
      header_row: 1,
      columns: NONE_COLS,
      sample_rows: [],
      rows_total: 1,
      mapping: {},
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a8': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', NONE_COLS)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a8', NONE_COLS))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    expect(suggestBodies, 'a suggest call must still be made even when the response is none').toHaveLength(1)
    expect(suggestBodies[0]!.document_id, "the recorded call must be for this group's document").toBe('doc-a8')

    expect(requireCtx().groups[0]?.mapping.invoice_number, 'a none response must leave invoice_number unplaced').toBeNull()
    const tagTexts = Array.from(document.querySelectorAll('[data-testid="map-column"] .mono')).map((n) => n.textContent ?? '')
    expect(tagTexts.length, 'control: the grid must render header/tag cells').toBeGreaterThan(0)
    expect(tagTexts.some((t) => t.includes('AUTO')), 'a none response must leave automatic recognition untouched').toBe(true)
    const continueButton = Array.from(document.querySelectorAll('button')).find((b) => b.textContent === 'Map invoice number to continue')
    expect(continueButton, 'the continue control must read the unmapped label').not.toBeUndefined()
    expect(requireCtx().importError, 'a none response must not surface an import error').toBeNull()
  })

  it("AIRA-09: a rejected suggest call opens today's Map step and does not strand it", async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a9': suggestFail(500) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a9', TWO_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    expect(suggestBodies, 'a suggest call must still be attempted for an unrestored group').toHaveLength(1)
    expect(requireCtx().importError, 'a rejected suggestion must not surface an import error').toBeNull()
    expect(requireCtx().groups[0]?.mapping, 'a rejected suggestion must fall back to the automatic seed').toEqual(
      initMappingFromHeaders(TWO_COL),
    )
  })

  it('AIRA-10: the suggest call carries the click-time entity', async () => {
    FakeXhr.instances = []
    const pending = deferred<SuggestFetchResult>()
    const entities = [
      { id: ENTITY_A, name: 'Mirror Co', tin: '12345678-0001' },
      { id: ENTITY_B, name: 'Second Co', tin: '87654321-0002' },
    ]
    await bootAtWithGateway({}, entities, { 'doc-a10': () => pending.promise })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a10', TWO_COL))
    })

    await act(async () => {})
    act(() => {
      requireCtx().switchClient(ENTITY_B)
    })
    await act(async () => {
      pending.resolve({ ok: true, status: 200, json: () => Promise.resolve(DEFAULT_SUGGEST_NONE) })
    })

    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))
    expect(requireCtx().entityId, 'control: switchClient must actually change the live entity').toBe(ENTITY_B)
    expect(suggestBodies, 'a suggest call must have been recorded').toHaveLength(1)
    expect(suggestBodies[0]!.entity_id, 'the suggest call must carry the click-time entity, not the live one').toBe(ENTITY_A)
  })

  it('AIRA-11: the Map step waits for the suggestion, inside the request guard', async () => {
    FakeXhr.instances = []
    const pending = deferred<SuggestFetchResult>()
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a11': () => pending.promise })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a11', TWO_COL))
    })

    await act(async () => {})
    expect(requireCtx().createStep, 'a pending suggestion must hold createStep on upload').toBe('upload')

    act(() => {
      requireCtx().readAllColumns()
    })
    expect(FakeXhr.instances, 'reqInFlight must still be held while the suggestion is pending').toHaveLength(1)

    await act(async () => {
      pending.resolve({ ok: true, status: 200, json: () => Promise.resolve(DEFAULT_SUGGEST_NONE) })
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))
  })

  it('AIRA-12: a "saved" answer at row 3 still posts header_row=3', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'saved',
      header_row: 3,
      columns: ROW3_COL,
      sample_rows: [['INV-9', '450']],
      rows_total: 2,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: '2026-09-05T00:00:00Z',
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a12': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', ROW1_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a12', ROW1_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    expect(requireCtx().groups[0]?.restored?.mapping.invoice_number, 'a saved answer must restore the group').toBe('Invoice No')
    expect(requireCtx().groups[0]?.headerRow, 'the resolved header row must survive the saved branch too').toBe(3)
    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'a saved answer must render the restored notice').not.toBeNull()

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'the import must reach the transport').toHaveLength(2))
    expect(FakeXhr.instances[1]!.body!.get('header_row'), 'a saved answer at row 3 must still post header_row=3').toBe('3')
  })

  it('AIRA-13: Use automatic suggestions keeps the resolved header row', async () => {
    FakeXhr.instances = []
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 3,
      columns: ROW3_COL_ALT,
      sample_rows: [['INV-3', '700']],
      rows_total: 3,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: null,
    }
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a13': suggestOk(res) })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', ROW1_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a13', ROW1_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    expect(
      requireCtx().groups[0]?.headerRow,
      'the suggestion must set the resolved header row before the reset can be tested',
    ).toBe(3)

    act(() => {
      requireCtx().resetGroupToAutomatic()
    })
    expect(requireCtx().groups[0]?.headerRow, 'resetting to automatic must not lose the resolved header row').toBe(3)

    act(() => {
      requireCtx().armField('invoice_number')
    })
    act(() => {
      requireCtx().clickCol('Invoice No')
    })

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'the import must reach the transport').toHaveLength(2))
    expect(FakeXhr.instances[1]!.body!.get('header_row'), 'the header row must survive the reset onto the wire').toBe('3')
  })

  it('AIRA-14: dropping a restored group back to automatic never sends it to the AI', async () => {
    FakeXhr.instances = []
    const SAVE_HIT: SavedMapping = { mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-09-01T10:15:00Z' }
    const suggestForB: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: ALT_COL,
      sample_rows: [['x', 'y']],
      rows_total: 1,
      mapping: { invoice_number: 'Ref Number' },
      saved_at: null,
    }
    await bootAtWithGateway({ 'doc-a14': okAnswer(SAVE_HIT) }, DEFAULT_ENTITIES, { 'doc-b14': suggestOk(suggestForB) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL), csvFile('b.csv', ALT_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview a never reached the transport').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a14', TWO_COL))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview b never reached the transport').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-b14', ALT_COL))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    // Positive control: the mechanism fired once for the unsaved sibling, so the count
    // below cannot read as "no suggest call is ever recorded in this fixture".
    expect(suggestBodies, 'only the unsaved group may record a suggest call').toHaveLength(1)
    expect(requireCtx().groups[0]?.restored, 'the active group must be the restored one').not.toBeNull()

    act(() => {
      requireCtx().resetGroupToAutomatic()
    })
    await act(async () => {})

    expect(requireCtx().groups[0]?.restored, 'control: the reset must actually drop the restored snapshot').toBeNull()
    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'control: the notice must be gone').toBeNull()
    expect(suggestBodies, 'dropping a restored group to automatic must not spend an AI call').toHaveLength(1)
  })

  it("AIRA-15: a suggest call whose transport rejects opens today's Map step too", async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({}, DEFAULT_ENTITIES, { 'doc-a15': suggestThrow() })
    await openCreateAndWaitForEntity()
    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', TWO_COL)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a15', TWO_COL))
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))

    expect(suggestBodies, 'control: the call must have been attempted').toHaveLength(1)
    expect(requireCtx().importError, 'a transport rejection must not surface an import error').toBeNull()
    expect(requireCtx().groups[0]?.mapping, 'a rejected suggestion must fall back to the automatic seed').toEqual(
      initMappingFromHeaders(TWO_COL),
    )
  })
})
