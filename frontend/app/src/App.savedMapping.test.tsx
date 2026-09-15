// @vitest-environment jsdom
// The lookup goes through fetch, so routeFetch answers it; FakeXhr serves only preview and createImport.

import { act, cleanup, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { fmtDateTime } from './lib/format'
import type { SavedMapping } from './lib/importApi'
import { initMappingFromHeaders, restoreMapping, toImportMapping } from './lib/mapping'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
const DEFAULT_ENTITIES = [{ id: ENTITY_A, name: 'Mirror Co', tin: '12345678-0001' }]

const LAYOUT_A = ['Invoice No', 'Subtotal', 'Total']
const LAYOUT_B = ['Ref', 'Total']
const SAVE_A: SavedMapping = { mapping: { invoice_number: 'Invoice No', total: 'Total' }, saved_at: '2026-09-01T10:15:00Z' }
const SAVE_B: SavedMapping = { mapping: { invoice_number: 'Ref' }, saved_at: '2026-09-02T08:00:00Z' }

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

const lookupUrls: string[] = []

beforeEach(() => {
  capturedCtx = undefined
  lookupUrls.length = 0
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
        lookupUrls.push(url)
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

describe('opening the Map step restores a saved mapping', () => {
  it("SMAPP-01: after preview the App looks up each group's saved mapping in group order with the click-time entity", async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A), 'doc-b': okAnswer(null) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A), csvFile('b.csv', LAYOUT_B)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview a never reached the transport').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: preview b never reached the transport').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-b', LAYOUT_B))
    })

    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))
    expect(lookupUrls, 'lookup URLs').toEqual([
      `${GATEWAY}/api/invoice/v1/imports/saved-mapping?entity_id=${ENTITY_A}&document_id=doc-a`,
      `${GATEWAY}/api/invoice/v1/imports/saved-mapping?entity_id=${ENTITY_A}&document_id=doc-b`,
    ])

    const ctx = requireCtx()
    const restoredMappingA = restoreMapping(LAYOUT_A, SAVE_A.mapping)
    expect(ctx.groups[0]?.restored, 'group 0 must be restored from SAVE_A').toEqual({
      savedAt: SAVE_A.saved_at,
      mapping: restoredMappingA,
    })
    expect(ctx.groups[0]?.mapping, 'group 0 mapping must equal the restored mapping').toEqual(restoredMappingA)
    expect(ctx.groups[1]?.restored, 'group 1 has nothing saved and must stay unrestored').toBeNull()
    expect(ctx.groups[1]?.mapping, 'group 1 mapping must stay on the automatic seed').toEqual(initMappingFromHeaders(LAYOUT_B))
  })

  it('SMAPP-02: the Map step waits for the lookup, which runs inside the request guard', async () => {
    FakeXhr.instances = []
    const pending = deferred<SavedMappingFetchResult>()
    await bootAtWithGateway({ 'doc-a': () => pending.promise })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: the preview never reached the transport').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })

    await waitFor(() => expect(lookupUrls, 'the lookup never started').toHaveLength(1))

    await act(async () => {})
    expect(requireCtx().createStep, 'the pending lookup must hold createStep on upload').toBe('upload')

    act(() => {
      requireCtx().readAllColumns()
    })
    expect(FakeXhr.instances, 'reqInFlight must still be held while the lookup is pending').toHaveLength(1)

    await act(async () => {
      pending.resolve({ ok: true, status: 200, json: () => Promise.resolve({ saved_mapping: SAVE_A }) })
    })

    await waitFor(() => expect(requireCtx().createStep, 'the resolved lookup must open the mapping step').toBe('mapping'))
    expect(requireCtx().groups[0]?.restored, 'the resolved lookup must restore group 0').not.toBeNull()
  })

  it("SMAPP-03: with no entity the App makes no lookup and opens today's seed", async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({}, [])

    act(() => {
      requireCtx().openCreate()
    })
    expect(requireCtx().entityId, 'control: with no entities entityId must be null').toBeNull()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: the preview never reached the transport').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })

    await waitFor(() => expect(requireCtx().createStep, 'a null entity must still open the mapping step').toBe('mapping'))
    expect(lookupUrls, 'a null entity must make no lookup').toEqual([])
    expect(requireCtx().groups[0]?.restored, 'with no lookup the group cannot be restored').toBeNull()
    expect(requireCtx().groups[0]?.mapping, 'the mapping must stay on the automatic seed').toEqual(initMappingFromHeaders(LAYOUT_A))
  })

  it('SMAPP-04: an untouched restored group posts remember_mapping false and a group with nothing saved posts true', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A), 'doc-b': okAnswer(null) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A), csvFile('b.csv', LAYOUT_B)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-b', LAYOUT_B))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    expect(requireCtx().groups[0]?.restored, 'the lookup never restored group 0').not.toBeNull()

    act(() => {
      requireCtx().continueMapping()
    })
    expect(requireCtx().groupIndex, 'sanity: an untouched complete group 0 must advance to group 1').toBe(1)

    act(() => {
      requireCtx().armField('invoice_number')
    })
    act(() => {
      requireCtx().clickCol('Ref')
    })
    expect(requireCtx().groups[1]?.mapping.invoice_number, 'sanity: the click must map invoice_number onto Ref').toBe('Ref')

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: group 0 never reached the createImport transport').toHaveLength(3))

    const body0 = FakeXhr.instances[2]!.body!
    expect(body0.getAll('remember_mapping'), 'remember_mapping must be sent exactly once').toHaveLength(1)
    expect(body0.get('remember_mapping'), 'an untouched restored group must skip the save').toBe('false')
    expect(JSON.parse(body0.get('mapping') as string), 'group 0 must send its restored mapping').toEqual(
      toImportMapping(restoreMapping(LAYOUT_A, SAVE_A.mapping)),
    )

    act(() => {
      FakeXhr.instances[2]!.respond(200, importReport('batch-1', 0))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: group 1 never reached the createImport transport').toHaveLength(4))

    const body1 = FakeXhr.instances[3]!.body!
    expect(body1.get('remember_mapping'), 'a group with nothing saved must be saved').toBe('true')
  })

  it('SMAPP-05: an edited restored group posts remember_mapping true', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    expect(requireCtx().groups[0]?.restored, 'the lookup never restored the group').not.toBeNull()

    act(() => {
      requireCtx().armField('total')
    })
    act(() => {
      requireCtx().clickCol('Subtotal')
    })
    expect(requireCtx().groups[0]?.mapping.total, 'sanity: the click must map total onto Subtotal').toBe('Subtotal')
    expect(requireCtx().groups[0]?.restored, 'editing a restored group must not clear its restored snapshot').not.toBeNull()

    act(() => {
      requireCtx().continueMapping()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control: the run never reached the createImport transport').toHaveLength(2))

    const body = FakeXhr.instances[1]!.body!
    expect(body.get('remember_mapping'), 'an edited restored group must be saved').toBe('true')
    expect(JSON.parse(body.get('mapping') as string).total, 'the edited placement must be sent').toBe('Subtotal')
  })

  it('SMAPP-06: a restored group renders the notice under the coverage sentence, RESTORED instead of AUTO, and an enabled import', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A), csvFile('c.csv', LAYOUT_A)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-c', LAYOUT_A))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))
    expect(lookupUrls, 'one lookup per group, not per file').toHaveLength(1)

    const notice = document.querySelector('[data-testid="map-restored-notice"]')
    expect(notice, 'no restored notice rendered').not.toBeNull()
    expect(notice!.tagName, 'the notice must be a DIV').toBe('DIV')
    expect(notice!.classList.contains('mono'), 'the notice itself must not be mono').toBe(false)
    const noticeText = `Mapping restored from this client's earlier import, saved ${fmtDateTime(SAVE_A.saved_at)}.`
    const noticeP = notice!.querySelector('p')
    expect(noticeP, 'the notice must hold a <p>').not.toBeNull()
    expect(noticeP!.textContent, 'the notice text must name the saved-at time').toBe(noticeText)
    expect(notice!.querySelector('.mono'), 'the notice must carry no mono descendant').toBeNull()

    const sentenceP = Array.from(document.querySelectorAll('main p')).find(
      (p) => p.textContent === 'This mapping applies to 2 files: a.csv and c.csv.',
    )
    expect(sentenceP, 'the coverage sentence must render for a two-file group').not.toBeUndefined()
    const coverageCard = sentenceP!.parentElement!.parentElement
    expect(notice!.parentElement, "the notice's parent must be the coverage card").toBe(coverageCard)

    const splitButton = Array.from(document.querySelectorAll('button')).find((b) => b.textContent === 'Map a.csv separately')
    expect(splitButton, 'the split control must render for a two-file group').not.toBeUndefined()
    expect(
      !!(sentenceP!.compareDocumentPosition(notice!) & Node.DOCUMENT_POSITION_FOLLOWING),
      'the notice must follow the coverage sentence',
    ).toBe(true)
    expect(
      !!(notice!.compareDocumentPosition(splitButton!) & Node.DOCUMENT_POSITION_FOLLOWING),
      'the notice must precede the split control',
    ).toBe(true)

    const returnButton = notice!.querySelector('button')
    expect(returnButton, 'the notice must hold the return-to-automatic control').not.toBeNull()
    expect(returnButton!.textContent, 'the control must read Use automatic suggestions').toBe('Use automatic suggestions')
    expect(returnButton!.disabled, 'the control must never be disabled').toBe(false)
    expect(returnButton!.className, "the control's class must match the split control's").toBe(splitButton!.className)
    expect(returnButton!.getAttribute('style'), "the control's style must match the split control's").toBe(
      splitButton!.getAttribute('style'),
    )

    const columns = Array.from(document.querySelectorAll('[data-testid="map-column"]'))
    expect(columns, 'the column grid must render one column per header').toHaveLength(LAYOUT_A.length)
    const badgesByHeader = new Map<string, Element[]>()
    columns.forEach((col) => {
      const header = col.querySelector('div.mono')?.textContent ?? ''
      badgesByHeader.set(header, Array.from(col.querySelectorAll('[data-testid="map-restored-badge"]')))
    })

    for (const header of ['Invoice No', 'Total']) {
      const badges = badgesByHeader.get(header) ?? []
      expect(badges, `${header} must hold exactly one RESTORED badge`).toHaveLength(1)
      expect(badges[0]!.tagName, `${header}'s badge must be a SPAN`).toBe('SPAN')
      expect(badges[0]!.classList.contains('mono'), `${header}'s badge must carry the mono class`).toBe(true)
      expect(badges[0]!.textContent, `${header}'s badge must read RESTORED`).toBe('RESTORED')
      expect((badges[0] as HTMLElement).style.color, `${header}'s badge must use the action colour`).toBe('var(--action)')
    }
    expect(badgesByHeader.get('Subtotal') ?? [], 'Subtotal must carry no badge').toHaveLength(0)

    const tagTexts = Array.from(document.querySelectorAll('[data-testid="map-column"] .mono')).map((n) => n.textContent ?? '')
    expect(tagTexts.some((t) => t.includes('AUTO')), 'no tag may still read AUTO once restored').toBe(false)

    const paletteChip = Array.from(document.querySelectorAll('button[draggable]')).find((b) => b.textContent?.includes('invoice_number'))
    expect(paletteChip, 'a restored invoice_number must not sit in the palette').toBeUndefined()

    const importButton = Array.from(document.querySelectorAll('button')).find((b) => b.textContent === 'Import 1 rows')
    expect(importButton, 'the continue control must read Import 1 rows').not.toBeUndefined()
    expect(importButton!.disabled, 'a fully restored group must leave Continue enabled').toBe(false)

    expect(document.querySelectorAll('main div.mono'), 'the column header cell is the only div.mono under main').toHaveLength(
      LAYOUT_A.length,
    )
  })

  it('SMAPP-07: moving a restored placement drops its RESTORED badge and keeps the notice', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))
    expect(requireCtx().groups[0]?.restored, 'precondition: group 0 must be restored').not.toBeNull()

    const badgesBefore = document.querySelectorAll('[data-testid="map-restored-badge"]')
    expect(badgesBefore, 'control: two RESTORED badges before the move').toHaveLength(2)

    act(() => {
      requireCtx().armField('total')
    })
    act(() => {
      requireCtx().clickCol('Subtotal')
    })

    const columns = Array.from(document.querySelectorAll('[data-testid="map-column"]'))
    const byHeader = new Map<string, Element>()
    columns.forEach((col) => {
      byHeader.set(col.querySelector('div.mono')?.textContent ?? '', col)
    })

    const subtotalCol = byHeader.get('Subtotal')!
    expect(
      subtotalCol.querySelectorAll('[data-testid="map-restored-badge"]'),
      'the moved placement must carry no RESTORED badge',
    ).toHaveLength(0)
    const subtotalTagTexts = Array.from(subtotalCol.querySelectorAll('.mono')).map((n) => n.textContent ?? '')
    expect(subtotalTagTexts.some((t) => t.includes('AUTO')), 'a moved placement must not read AUTO either').toBe(false)
    expect(subtotalTagTexts.some((t) => t === 'total'), 'Subtotal must now hold the total tag').toBe(true)

    const totalCol = byHeader.get('Total')!
    expect(totalCol.querySelector('span[draggable]'), 'Total must hold no tag once its placement moves').toBeNull()

    expect(
      document.querySelectorAll('[data-testid="map-restored-badge"]'),
      'only the untouched invoice_number placement keeps its badge',
    ).toHaveLength(1)
    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'the notice must survive the edit').not.toBeNull()
  })

  it('SMAPP-08: Use automatic suggestions reseeds only the active group and clears the notice and every badge', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A), 'doc-b': okAnswer(SAVE_B) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A), csvFile('b.csv', LAYOUT_B)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-b', LAYOUT_B))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    expect(requireCtx().groups[0]?.restored, 'precondition: group 0 restored').not.toBeNull()
    const group1Snapshot = requireCtx().groups[1]
    expect(group1Snapshot?.restored, 'precondition: group 1 restored').not.toBeNull()

    const notice = document.querySelector('[data-testid="map-restored-notice"]')
    expect(notice, 'sanity: the notice must render before the reset').not.toBeNull()
    const returnButton = notice!.querySelector('button')
    expect(returnButton, 'sanity: the return control must render before the reset').not.toBeNull()

    fireEvent.click(returnButton!)

    const after0 = requireCtx().groups[0]
    expect(after0?.restored, "the return control must drop group 0's restored snapshot").toBeNull()
    expect(after0?.mapping, "group 0 must reseed from today's automatic suggestions").toEqual(initMappingFromHeaders(LAYOUT_A))

    const after1 = requireCtx().groups[1]
    expect(after1?.restored, "group 0's reset must not touch group 1").toEqual(group1Snapshot?.restored)
    expect(after1?.mapping, "group 1's mapping must be untouched too").toEqual(group1Snapshot?.mapping)

    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'the notice must be gone once reset').toBeNull()
    expect(document.querySelectorAll('[data-testid="map-restored-badge"]'), 'every badge must be gone once reset').toHaveLength(0)

    const totalCol = Array.from(document.querySelectorAll('[data-testid="map-column"]')).find(
      (col) => col.querySelector('div.mono')?.textContent === 'Total',
    )!
    const totalTagTexts = Array.from(totalCol.querySelectorAll('.mono')).map((n) => n.textContent ?? '')
    expect(totalTagTexts.some((t) => t.includes('AUTO')), 'Total must read AUTO again once reset to automatic').toBe(true)

    const paletteChip = Array.from(document.querySelectorAll('button[draggable]')).find((b) => b.textContent?.includes('invoice_number'))
    expect(paletteChip, 'invoice_number must return to the palette once reset').not.toBeUndefined()

    const continueButton = Array.from(document.querySelectorAll('button')).find((b) => b.textContent === 'Map invoice number to continue')
    expect(continueButton, 'the continue control must read Map invoice number to continue once unrestored').not.toBeUndefined()
  })

  it.each<[string, SavedMappingAnswer]>([
    ['a 500', () => Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })],
    ['a network error', () => Promise.reject(new TypeError('Failed to fetch'))],
  ])(
    "SMAPP-09: a lookup that fails with %s shows no error, opens today's seed and does not block the import",
    async (_label, failing) => {
      FakeXhr.instances = []
      await bootAtWithGateway({ 'doc-a': failing })
      await openCreateAndWaitForEntity()

      act(() => {
        requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A)])
      })
      act(() => {
        requireCtx().readAllColumns()
      })
      await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
      act(() => {
        FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
      })

      await waitFor(() => expect(lookupUrls, 'control: the lookup was never attempted').toHaveLength(1))
      await waitFor(() => expect(requireCtx().createStep, 'a failed lookup must still open the mapping step').toBe('mapping'))

      const ctx = requireCtx()
      expect(ctx.importError, 'a failed lookup must not surface an import error').toBeNull()
      expect(ctx.groups[0]?.restored, 'a failed lookup must leave the group unrestored').toBeNull()
      expect(ctx.groups[0]?.mapping, 'a failed lookup must fall back to the automatic seed').toEqual(initMappingFromHeaders(LAYOUT_A))
      expect(document.querySelector('[data-testid="map-restored-notice"]'), 'no notice may render for a failed lookup').toBeNull()

      act(() => {
        requireCtx().armField('invoice_number')
      })
      act(() => {
        requireCtx().clickCol('Invoice No')
      })
      act(() => {
        requireCtx().continueMapping()
      })

      await waitFor(() => expect(FakeXhr.instances, 'the import must still proceed after a failed lookup').toHaveLength(2))
      const body = FakeXhr.instances[1]!.body!
      expect(body.get('remember_mapping'), 'a group that was never restored must be saved').toBe('true')
    },
  )

  it('SMAPP-10: the notice and badges follow the active group, so an unrestored second group shows neither', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({ 'doc-a': okAnswer(SAVE_A), 'doc-b': okAnswer(null) })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A), csvFile('b.csv', LAYOUT_B)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-b', LAYOUT_B))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'control: restored group 0 renders the notice').not.toBeNull()
    const nextButton = Array.from(document.querySelectorAll('button')).find((b) => b.textContent === 'Continue to next file')
    expect(nextButton, 'a restored group that is not the last must read Continue to next file').not.toBeUndefined()
    expect(nextButton!.disabled, 'a restored group that is not the last must leave Continue enabled').toBe(false)

    act(() => {
      requireCtx().continueMapping()
    })
    expect(requireCtx().groupIndex, 'sanity: an untouched restored group 0 must advance to group 1').toBe(1)

    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'an unrestored active group must render no notice').toBeNull()
    expect(document.querySelectorAll('[data-testid="map-restored-badge"]'), 'an unrestored active group must render no badge').toHaveLength(0)
    const totalCol = Array.from(document.querySelectorAll('[data-testid="map-column"]')).find(
      (col) => col.querySelector('div.mono')?.textContent === 'Total',
    )
    expect(totalCol, "group 1's Total column must render").not.toBeUndefined()
    const totalTagTexts = Array.from(totalCol!.querySelectorAll('.mono')).map((n) => n.textContent ?? '')
    expect(totalTagTexts.some((t) => t.includes('AUTO')), "group 1's recognised Total must read AUTO").toBe(true)
  })

  it('SMAPP-11: a failed first lookup still restores the second group, and Use automatic suggestions there resets only that group', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway({
      'doc-a': () => Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) }),
      'doc-b': okAnswer(SAVE_B),
    })
    await openCreateAndWaitForEntity()

    act(() => {
      requireCtx().addPickedFiles([csvFile('a.csv', LAYOUT_A), csvFile('b.csv', LAYOUT_B)])
    })
    act(() => {
      requireCtx().readAllColumns()
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(1))
    act(() => {
      FakeXhr.instances[0]!.respond(200, previewReply('doc-a', LAYOUT_A))
    })
    await waitFor(() => expect(FakeXhr.instances, 'control').toHaveLength(2))
    act(() => {
      FakeXhr.instances[1]!.respond(200, previewReply('doc-b', LAYOUT_B))
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    expect(lookupUrls, 'a failed first lookup must not stop the second').toHaveLength(2)
    expect(requireCtx().importError, 'a failed lookup must not surface an import error').toBeNull()
    expect(requireCtx().groups[0]?.restored, 'the failed lookup must leave group 0 unrestored').toBeNull()
    expect(requireCtx().groups[1]?.restored, 'group 1 must be restored from SAVE_B').toEqual({
      savedAt: SAVE_B.saved_at,
      mapping: restoreMapping(LAYOUT_B, SAVE_B.mapping),
    })
    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'unrestored group 0 must render no notice').toBeNull()

    act(() => {
      requireCtx().armField('invoice_number')
    })
    act(() => {
      requireCtx().clickCol('Invoice No')
    })
    act(() => {
      requireCtx().continueMapping()
    })
    expect(requireCtx().groupIndex, 'sanity: group 0 must advance once invoice_number is placed').toBe(1)
    const group0Snapshot = requireCtx().groups[0]
    expect(group0Snapshot?.mapping.invoice_number, 'sanity: group 0 keeps its hand placement').toBe('Invoice No')

    const notice = document.querySelector('[data-testid="map-restored-notice"]')
    expect(notice, "restored group 1 must render the notice").not.toBeNull()
    expect(notice!.querySelector('p')?.textContent, "the notice must name group 1's saved-at time").toBe(
      `Mapping restored from this client's earlier import, saved ${fmtDateTime(SAVE_B.saved_at)}.`,
    )
    const refCol = Array.from(document.querySelectorAll('[data-testid="map-column"]')).find(
      (col) => col.querySelector('div.mono')?.textContent === 'Ref',
    )
    expect(refCol, "group 1's Ref column must render").not.toBeUndefined()
    expect(refCol!.querySelectorAll('[data-testid="map-restored-badge"]'), 'Ref must hold the restored invoice_number badge').toHaveLength(1)

    fireEvent.click(notice!.querySelector('button')!)

    expect(requireCtx().groups[1]?.restored, 'the control must drop the active group 1 snapshot').toBeNull()
    expect(requireCtx().groups[1]?.mapping, 'group 1 must reseed from automatic suggestions').toEqual(initMappingFromHeaders(LAYOUT_B))
    expect(requireCtx().groups[0], 'the control must leave group 0 exactly as it was').toEqual(group0Snapshot)
    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'the notice must be gone once reset').toBeNull()
  })
})
