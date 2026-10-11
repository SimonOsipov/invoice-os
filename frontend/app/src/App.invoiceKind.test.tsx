// @vitest-environment jsdom
// The buyer-type default through the real App path: select -> group -> createImport multipart.

import { act, cleanup, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import type { SavedMapping } from './lib/importApi'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
const DEFAULT_ENTITIES = [{ id: ENTITY_A, name: 'Mirror Co', tin: '12345678-0001' }]

const LAYOUT_A = ['Invoice No', 'Subtotal', 'Total']
const LAYOUT_B = ['Ref', 'Total', 'Kind']
const SAVE_A: SavedMapping = { mapping: { invoice_number: 'Invoice No', total: 'Total' }, saved_at: '2026-09-01T10:15:00Z' }

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


beforeEach(() => {
  capturedCtx = undefined
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

// Records createImport's multipart body so remember_mapping can be asserted.
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

function entityRow(id: string, name: string, tin: string) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
}

function routeFetch(savedMappingByDoc: Record<string, SavedMappingAnswer>, entities: { id: string; name: string; tin: string }[]) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
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
      if (url.includes('/api/invoice/v1/imports/saved-mapping')) {
        const m = /document_id=([^&]+)/.exec(url)
        const documentId = m ? decodeURIComponent(m[1]) : ''
        const answer = savedMappingByDoc[documentId]
        if (!answer) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ saved_mapping: null }) })
        return answer()
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0 }),
      })
    }),
  )
}

async function bootAtWithGateway(
  savedMappingByDoc: Record<string, SavedMappingAnswer>,
  entities: { id: string; name: string; tin: string }[] = DEFAULT_ENTITIES,
) {
  routeFetch(savedMappingByDoc, entities)
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

function importReport(id: string, readyInvoices: number) {
  return {
    id,
    status: 'completed',
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    rows_total: 1,
    rows_valid: 1,
    rows_invalid: 0,
    ready_invoices: readyInvoices,
    quarantined_invoices: 1 - readyInvoices,
    errors: [],
    rule_set_version: null,
    invoices_clean: readyInvoices,
    invoices_with_violations: 0,
    invoice_violations: [],
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}


const kindSelect = () => document.querySelector<HTMLSelectElement>('[data-testid="map-invoice-kind"]')

function chooseKind(value: string) {
  fireEvent.change(kindSelect()!, { target: { value } })
}

async function reachMapping(files: [string, string[]][], docs: string[]) {
  FakeXhr.instances = []
  await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A) })
  await openCreateAndWaitForEntity()
  act(() => {
    requireCtx().addPickedFiles(files.map(([n, cols]) => csvFile(n, cols)))
  })
  act(() => {
    requireCtx().readAllColumns()
  })
  for (let i = 0; i < files.length; i++) {
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(i + 1))
    act(() => {
      FakeXhr.instances[i]!.respond(200, previewReply(docs[i]!, files[i]![1]))
    })
  }
  await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))
}

function mapRef() {
  act(() => {
    requireCtx().armField('invoice_number')
  })
  act(() => {
    requireCtx().clickCol('Ref')
  })
}

describe('the buyer-type default reaches createImport per group', () => {
  it('ENGI07-APP-01: each createImport carries only its own group kind; Not set sends no part', async () => {
    await reachMapping([['a.csv', LAYOUT_A], ['b.csv', LAYOUT_B]], ['doc-a', 'doc-b'])
    expect(requireCtx().groups, 'control: two layout groups').toHaveLength(2)

    chooseKind('B2G')
    act(() => {
      requireCtx().continueMapping()
    })
    expect(requireCtx().groupIndex, 'sanity: advanced to group 1').toBe(1)
    mapRef()
    expect(kindSelect()!.value, "group 1 must not inherit group 0's choice").toBe('')
    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(3))
    const body0 = FakeXhr.instances[2]!.body!
    expect(body0.getAll('default_invoice_kind')).toEqual(['B2G'])
    act(() => {
      FakeXhr.instances[2]!.respond(200, importReport('batch-1', 1))
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(4))
    expect(Array.from(FakeXhr.instances[3]!.body!.keys())).not.toContain('default_invoice_kind')
  })

  it('ENGI07-APP-02: Not set after a choice sends no part, and a placed invoice_kind suppresses the stored choice', async () => {
    await reachMapping([['a.csv', LAYOUT_A], ['b.csv', LAYOUT_B]], ['doc-a', 'doc-b'])

    chooseKind('B2G')
    chooseKind('')
    act(() => {
      requireCtx().continueMapping()
    })
    mapRef()
    chooseKind('B2C')
    act(() => {
      requireCtx().armField('invoice_kind')
    })
    act(() => {
      requireCtx().clickCol('Kind')
    })
    expect(kindSelect(), 'placing invoice_kind removes the select').toBeNull()
    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(3))
    expect(Array.from(FakeXhr.instances[2]!.body!.keys()), 'Not set must send no part').not.toContain('default_invoice_kind')
    act(() => {
      FakeXhr.instances[2]!.respond(200, importReport('batch-1', 1))
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(4))
    expect(Array.from(FakeXhr.instances[3]!.body!.keys()), 'a mapped invoice_kind column must suppress the default').not.toContain('default_invoice_kind')
  })

  it('ENGI07-APP-03: a file split out of a group keeps the group kind and sends it', async () => {
    await reachMapping([['a.csv', LAYOUT_A], ['a2.csv', LAYOUT_A]], ['doc-a', 'doc-a2'])
    expect(requireCtx().groups, 'control: one group').toHaveLength(1)

    chooseKind('B2C')
    act(() => {
      requireCtx().splitOutFile(requireCtx().groups[0]!.fileIds[1]!)
    })
    expect(requireCtx().groups, 'control: split produced two groups').toHaveLength(2)
    expect(requireCtx().groups.map((g) => g.invoiceKind)).toEqual(['B2C', 'B2C'])
    act(() => {
      requireCtx().continueMapping()
    })
    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(3))
    expect(FakeXhr.instances[2]!.body!.getAll('default_invoice_kind')).toEqual(['B2C'])
    act(() => {
      FakeXhr.instances[2]!.respond(200, importReport('batch-1', 1))
    })
    await waitFor(() => expect(FakeXhr.instances).toHaveLength(4))
    expect(FakeXhr.instances[3]!.body!.getAll('default_invoice_kind'), 'the split-out group sends it too').toEqual(['B2C'])
  })

  it('ENGI07-APP-04: Use automatic suggestions keeps the kind', async () => {
    await reachMapping([['a.csv', LAYOUT_A]], ['doc-a'])
    chooseKind('B2B')
    const notice = document.querySelector('[data-testid="map-restored-notice"]')
    expect(notice, 'control: restored notice offers the return control').not.toBeNull()
    fireEvent.click(notice!.querySelector('button')!)
    expect(requireCtx().groups[0]?.restored, 'control: reset happened').toBeNull()
    expect(kindSelect()!.value).toBe('B2B')
  })
})
