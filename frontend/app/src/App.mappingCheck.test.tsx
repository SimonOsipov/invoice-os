// @vitest-environment jsdom
// The placement check between the suggestion and the Map step. routeFetch answers the saved
// lookup, the suggestion and the check; FakeXhr serves preview and createImport.

import { act, cleanup, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import type { CheckMapping, SavedMapping, SuggestMapping } from './lib/importApi'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
const DEFAULT_ENTITIES = [{ id: ENTITY_A, name: 'Mirror Co', tin: '12345678-0001' }]

// TWO_COL and ALT_COL alias nothing, so a placement there comes from a suggestion or a restore.
const TWO_COL = ['Invoice No', 'Client Ref']
const ALT_COL = ['Ref Number', 'Total Due']
// Aliases seed issue_date, vat and total.
const AUTO_COLS = ['Invoice No', 'Issue Date', 'VAT %', 'Total']
// Aliases seed total only.
const TOTAL_COLS = ['Invoice No', 'Total']

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
const checkBodies: { document_id: string; mapping: Record<string, string> }[] = []

beforeEach(() => {
  capturedCtx = undefined
  suggestBodies.length = 0
  checkBodies.length = 0
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

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

type FetchResult = { ok: boolean; status: number; json: () => Promise<unknown> }
type Answer = () => Promise<FetchResult>

const ok = (body: unknown): Answer => () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
const fail = (status: number): Answer => () => Promise.resolve({ ok: false, status, json: () => Promise.resolve({ error: 'boom' }) })
const transportFails = (): Answer => () => Promise.reject(new TypeError('Failed to fetch'))
const doubts = (doubted: string[]): Answer => ok({ doubted } satisfies CheckMapping)

const SUGGEST_NONE: SuggestMapping = { source: 'none', header_row: 1, columns: [], sample_rows: [], rows_total: 0, mapping: {}, saved_at: null }

function suggestAi(columns: string[], mapping: Record<string, string>): Answer {
  return ok({ source: 'ai', header_row: 1, columns, sample_rows: [columns.map(() => 'x')], rows_total: 1, mapping, saved_at: null } satisfies SuggestMapping)
}

function entityRow(id: string, name: string, tin: string) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
}

interface Routes {
  saved?: Record<string, Answer>
  suggest?: Record<string, Answer>
  check?: Record<string, Answer>
  entities?: { id: string; name: string; tin: string }[]
}

function routeFetch({ saved = {}, suggest = {}, check = {}, entities = DEFAULT_ENTITIES }: Routes) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; body?: string }) => {
      if (url.includes('/portfolio/v1/entities')) {
        return ok({ entities: entities.map((e) => entityRow(e.id, e.name, e.tin)), pagination: { limit: 200, offset: 0, total: entities.length } })()
      }
      if (url.includes('/api/invoice/v1/imports/check-mapping')) {
        const body = JSON.parse(init!.body!) as { document_id: string; mapping: Record<string, string> }
        checkBodies.push(body)
        return (check[body.document_id] ?? doubts([]))()
      }
      if (url.includes('/api/invoice/v1/imports/suggest-mapping')) {
        const body = JSON.parse(init!.body!) as { entity_id: string; document_id: string }
        suggestBodies.push(body)
        return (suggest[body.document_id] ?? ok(SUGGEST_NONE))()
      }
      if (url.includes('/api/invoice/v1/imports/saved-mapping')) {
        const m = /document_id=([^&]+)/.exec(url)
        const documentId = m ? decodeURIComponent(m[1]) : ''
        return (saved[documentId] ?? ok({ saved_mapping: null }))()
      }
      return ok({ entities: [], policies: [], members: [], roles: [], invoices: [], clients: [], totals: EMPTY_BUCKET, total: 0 })()
    }),
  )
}

async function boot(routes: Routes) {
  FakeXhr.instances = []
  routeFetch(routes)
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  render(<App />)
  act(() => {
    requireCtx().openCreate()
  })
  const want = (routes.entities ?? DEFAULT_ENTITIES)[0]?.id ?? null
  if (want) await waitFor(() => expect(requireCtx().entityId, 'the click-time entity never resolved').toBe(want))
  else await act(async () => {})
}

// A second boot in the same test.
async function reboot(routes: Routes) {
  cleanup()
  window.history.replaceState(null, '', '/')
  await boot(routes)
}

interface Pick {
  name: string
  cols: string[]
  doc: string
}

function csvFile(name: string, headers: string[]): File {
  return new File([headers.join(',') + '\n'], name, { type: 'text/csv' })
}

function previewReply(documentId: string, columns: string[]) {
  return { document_id: documentId, format: 'csv', delimiter: ',', encoding: 'utf-8', columns, sample_rows: [columns.map(() => 'x')], rows_total: 1 }
}

// Picks the files and answers every preview; does not wait for the Map step.
async function readColumns(picks: Pick[]) {
  act(() => {
    requireCtx().addPickedFiles(picks.map((p) => csvFile(p.name, p.cols)))
  })
  act(() => {
    requireCtx().readAllColumns()
  })
  for (let i = 0; i < picks.length; i++) {
    await waitFor(() => expect(FakeXhr.instances, `preview ${picks[i].name} never reached the transport`).toHaveLength(i + 1))
    act(() => {
      FakeXhr.instances[i]!.respond(200, previewReply(picks[i].doc, picks[i].cols))
    })
  }
}

async function readToMapStep(picks: Pick[]) {
  await readColumns(picks)
  await waitFor(() => expect(requireCtx().createStep, 'the Map step never opened').toBe('mapping'))
}

function columnByHeader(header: string): Element {
  const col = Array.from(document.querySelectorAll('[data-testid="map-column"]')).find(
    (c) => c.querySelector('div.mono')?.textContent === header,
  )
  expect(col, `no ${header} column`).toBeDefined()
  return col!
}

function paletteButton(field: string): Element | undefined {
  return Array.from(document.querySelectorAll('button[draggable]')).find((b) => b.textContent?.includes(field))
}

function buttonByText(text: string): HTMLButtonElement | undefined {
  return Array.from(document.querySelectorAll('button')).find((b) => b.textContent === text)
}

// Header, placed field and badge texts per column.
function mapStepSnapshot(): string[][] {
  const cols = Array.from(document.querySelectorAll('[data-testid="map-column"]'))
  expect(cols.length, 'control: the column grid rendered').toBeGreaterThan(0)
  return cols.map((c) => {
    const chip = c.querySelector('span[draggable]')
    const spans = chip ? Array.from(chip.querySelectorAll('span.mono')).map((s) => s.textContent ?? '') : ['drop field']
    return [c.querySelector('div.mono')?.textContent ?? '', ...spans]
  })
}

function sessionRemovals(): number {
  return (localStorage.removeItem as ReturnType<typeof vi.fn>).mock.calls.filter((c) => c[0] === SESSION_KEY).length
}

describe('a doubted placement is left unplaced on the Map step', () => {
  it('CHKA-01: a doubted suggested invoice number opens unplaced and gates Import', async () => {
    await boot({ suggest: { 'doc-c1': suggestAi(TWO_COL, { invoice_number: 'Invoice No' }) }, check: { 'doc-c1': doubts(['invoice_number']) } })
    await readToMapStep([{ name: 'a.csv', cols: TWO_COL, doc: 'doc-c1' }])

    expect(paletteButton('invoice_number'), 'invoice_number is back in the palette').toBeDefined()
    expect(columnByHeader('Invoice No').textContent).toContain('drop field')
    expect(document.querySelectorAll('[data-testid="map-suggested-badge"]')).toHaveLength(0)
    expect(buttonByText('Map invoice number to continue')).toBeDefined()
    expect(checkBodies, 'one check request').toHaveLength(1)

    fireEvent.click(paletteButton('invoice_number')!)
    fireEvent.click(columnByHeader('Invoice No'))
    const importButton = buttonByText('Import 1 rows')
    expect(importButton, 'placing invoice_number by hand enables Import').toBeDefined()
    expect(importButton!.disabled).toBe(false)

    fireEvent.click(importButton!)
    await waitFor(() => expect(FakeXhr.instances, 'the import must reach the transport').toHaveLength(2))
    expect(JSON.parse(FakeXhr.instances[1]!.body!.get('mapping') as string)).toEqual({ invoice_number: 'Invoice No' })
  })

  it('CHKA-02: one doubted AUTO placement is unplaced and its neighbours stay', async () => {
    await boot({ check: { 'doc-c2': doubts(['vat']) } })
    await readToMapStep([{ name: 'a.csv', cols: AUTO_COLS, doc: 'doc-c2' }])

    expect(checkBodies, 'one check request').toHaveLength(1)
    expect(checkBodies[0]!.mapping).toEqual({ issue_date: 'Issue Date', vat: 'VAT %', total: 'Total' })
    expect(columnByHeader('VAT %').textContent).toContain('drop field')
    expect(columnByHeader('VAT %').querySelector('span[draggable]')).toBeNull()
    for (const [header, field] of [
      ['Issue Date', 'issue_date'],
      ['Total', 'total'],
    ]) {
      const texts = Array.from(columnByHeader(header).querySelectorAll('span[draggable] span.mono')).map((s) => s.textContent)
      expect(texts, `${header} keeps ${field} with its AUTO badge`).toEqual([field, 'AUTO'])
    }
  })

  it('CHKA-03: a restored group and a saved suggestion make no check request', async () => {
    const SAVE_HIT: SavedMapping = { mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-09-01T10:15:00Z' }
    const savedSuggestion: SuggestMapping = {
      source: 'saved',
      header_row: 1,
      columns: ALT_COL,
      sample_rows: [['x', 'y']],
      rows_total: 1,
      mapping: { invoice_number: 'Ref Number' },
      saved_at: '2026-09-02T00:00:00Z',
    }
    await boot({ saved: { 'doc-r1': ok({ saved_mapping: SAVE_HIT }) }, suggest: { 'doc-r2': ok(savedSuggestion) } })
    await readToMapStep([
      { name: 'a.csv', cols: TWO_COL, doc: 'doc-r1' },
      { name: 'b.csv', cols: ALT_COL, doc: 'doc-r2' },
      { name: 'c.csv', cols: TOTAL_COLS, doc: 'doc-r3' },
    ])

    expect(checkBodies, 'only the unrestored group is checked').toHaveLength(1)
    expect(checkBodies[0]!.document_id).toBe('doc-r3')
    expect(requireCtx().groups.map((g) => g.restored !== null)).toEqual([true, true, false])
    expect(document.querySelector('[data-testid="map-restored-notice"]'), 'group 1 renders RESTORED').not.toBeNull()
  })

  it('CHKA-04: with no entity no check request is made', async () => {
    await boot({})
    await readToMapStep([{ name: 'a.csv', cols: TOTAL_COLS, doc: 'doc-e1' }])
    expect(checkBodies, 'control: with an entity the file is checked once').toHaveLength(1)

    await reboot({ entities: [] })
    expect(requireCtx().entityId, 'control: no entity').toBeNull()
    await readToMapStep([{ name: 'a.csv', cols: TOTAL_COLS, doc: 'doc-e2' }])
    expect(checkBodies, 'no entity, no check request').toHaveLength(1)
  })

  it('CHKA-05: the Map step waits for the check inside the request guard', async () => {
    let release!: (r: FetchResult) => void
    const pending = new Promise<FetchResult>((res) => {
      release = res
    })
    await boot({ suggest: { 'doc-c5': suggestAi(TWO_COL, { invoice_number: 'Invoice No' }) }, check: { 'doc-c5': () => pending } })
    await readColumns([{ name: 'a.csv', cols: TWO_COL, doc: 'doc-c5' }])

    await waitFor(() => expect(checkBodies, 'the check request was sent').toHaveLength(1))
    expect(requireCtx().createStep).not.toBe('mapping')
    expect(document.querySelectorAll('[data-testid="map-column"]')).toHaveLength(0)

    act(() => {
      requireCtx().readAllColumns()
    })
    expect(FakeXhr.instances, 'a second Read columns sends no preview while the check is pending').toHaveLength(1)

    await act(async () => {
      release({ ok: true, status: 200, json: () => Promise.resolve({ doubted: [] }) })
    })
    await waitFor(() => expect(requireCtx().createStep).toBe('mapping'))
  })

  it('CHKA-06: a rejected, failed or unauthorised check opens the same Map step as no doubt', async () => {
    const picks = (doc: string): Pick[] => [{ name: 'a.csv', cols: AUTO_COLS, doc }]
    const suggest = (doc: string) => ({ [doc]: suggestAi(AUTO_COLS, { invoice_number: 'Invoice No' }) })

    async function mapStepWith(doc: string, answer: Answer): Promise<string[][]> {
      await reboot({ suggest: suggest(doc), check: { [doc]: answer } })
      await readToMapStep(picks(doc))
      return mapStepSnapshot()
    }

    const noDoubt = await mapStepWith('doc-none', doubts([]))
    const doubted = await mapStepWith('doc-control', doubts(['invoice_number']))
    expect(doubted, "control: ['invoice_number'] changes the Map step").not.toEqual(noDoubt)
    expect(await mapStepWith('doc-throw', transportFails())).toEqual(noDoubt)
    expect(await mapStepWith('doc-500', fail(500))).toEqual(noDoubt)
    expect(checkBodies.map((b) => b.document_id), 'every leg sent its check request').toEqual([
      'doc-none',
      'doc-control',
      'doc-throw',
      'doc-500',
    ])

    // A 401 signs out, so the Map step is gone; the sign-out must match the suggestion's 401.
    const beforeSuggest = sessionRemovals()
    await reboot({ suggest: { 'doc-s401': fail(401) } })
    await readColumns(picks('doc-s401'))
    await waitFor(() => expect(sessionRemovals(), 'control: a suggestion 401 signs out').toBeGreaterThan(beforeSuggest))
    const suggestSignOut = sessionRemovals() - beforeSuggest

    const beforeCheck = sessionRemovals()
    const checksBefore = checkBodies.length
    await reboot({ suggest: suggest('doc-c401'), check: { 'doc-c401': fail(401) } })
    await readColumns(picks('doc-c401'))
    await waitFor(() => expect(checkBodies.length - checksBefore, 'the check request was sent').toBe(1))
    await waitFor(() => expect(sessionRemovals(), 'a check 401 signs out').toBeGreaterThan(beforeCheck))
    expect(sessionRemovals() - beforeCheck).toBe(suggestSignOut)
  })

  it('CHKA-07: the check runs once per Read columns and never after', async () => {
    await boot({
      suggest: {
        'doc-a1': suggestAi(TWO_COL, { invoice_number: 'Invoice No' }),
        'doc-b1': suggestAi(ALT_COL, { invoice_number: 'Ref Number' }),
      },
    })
    await readToMapStep([
      { name: 'a1.csv', cols: TWO_COL, doc: 'doc-a1' },
      { name: 'a2.csv', cols: TWO_COL, doc: 'doc-a2' },
      { name: 'b1.csv', cols: ALT_COL, doc: 'doc-b1' },
    ])
    expect(checkBodies.map((b) => b.document_id), 'one check request per group').toEqual(['doc-a1', 'doc-b1'])

    act(() => {
      requireCtx().armField('buyer_name')
    })
    act(() => {
      requireCtx().clickCol('Client Ref')
    })
    expect(requireCtx().groups[0]!.mapping.buyer_name, 'control: the hand placement landed').toBe('Client Ref')
    const shared = requireCtx().groups[0]!
    expect(shared.fileIds, 'control: the first group is shared').toHaveLength(2)
    act(() => {
      requireCtx().splitOutFile(shared.fileIds[1]!)
    })
    expect(requireCtx().groups, 'control: the split added a group').toHaveLength(3)
    for (let i = 0; i < 3; i++) {
      act(() => {
        requireCtx().continueMapping()
      })
    }
    await waitFor(() => expect(FakeXhr.instances.length, 'control: Import sent a create request').toBeGreaterThan(3))
    await act(async () => {})
    expect(checkBodies, 'no check request after a hand placement, a split or Import').toHaveLength(2)

    const SAVE_HIT: SavedMapping = { mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-09-01T10:15:00Z' }
    const before = checkBodies.length
    await reboot({ saved: { 'doc-r7': ok({ saved_mapping: SAVE_HIT }) }, suggest: { 'doc-u7': suggestAi(ALT_COL, { invoice_number: 'Ref Number' }) } })
    await readToMapStep([
      { name: 'r.csv', cols: TWO_COL, doc: 'doc-r7' },
      { name: 'u.csv', cols: ALT_COL, doc: 'doc-u7' },
    ])
    expect(checkBodies.slice(before).map((b) => b.document_id), 'control: the unrestored group was checked once').toEqual(['doc-u7'])
    const useAutomatic = buttonByText('Use automatic suggestions')
    expect(useAutomatic, 'the restored group offers Use automatic suggestions').toBeDefined()
    fireEvent.click(useAutomatic!)
    await act(async () => {})
    expect(requireCtx().groups[0]!.restored, 'control: the reset dropped the restore').toBeNull()
    expect(checkBodies.length - before, 'Use automatic suggestions makes no check request').toBe(1)
  })

  it('CHKA-08: a doubt adds no element', async () => {
    const suggest = (doc: string) => ({ [doc]: suggestAi(TWO_COL, { invoice_number: 'Invoice No' }) })

    await boot({ suggest: suggest('doc-d1'), check: { 'doc-d1': doubts(['invoice_number']) } })
    await readToMapStep([{ name: 'a.csv', cols: TWO_COL, doc: 'doc-d1' }])
    const doubtedHtml = document.querySelector('main')!.innerHTML

    await reboot({ suggest: suggest('doc-d2'), check: { 'doc-d2': doubts([]) } })
    await readToMapStep([{ name: 'a.csv', cols: TWO_COL, doc: 'doc-d2' }])
    const chip = columnByHeader('Invoice No').querySelector('span[draggable]')
    expect(chip, 'control: run B places invoice_number').not.toBeNull()
    fireEvent.click(chip!.lastElementChild!)
    expect(columnByHeader('Invoice No').querySelector('span[draggable]'), 'control: the remove glyph unplaced it').toBeNull()

    expect(doubtedHtml.length, 'control: run A rendered').toBeGreaterThan(0)
    expect(doubtedHtml).toBe(document.querySelector('main')!.innerHTML)
    expect(checkBodies.map((b) => b.document_id), 'both runs were checked').toEqual(['doc-d1', 'doc-d2'])
  })
})

describe('the placement check — adversarial (QA Mode B)', () => {
  it('QA-CHKA-01: the check follows the suggestion and precedes the Map step', async () => {
    const atCheck: { suggests: number; step: string | undefined }[] = []
    const recordThenDoubt: Answer = () => {
      atCheck.push({ suggests: suggestBodies.length, step: capturedCtx?.createStep })
      return doubts([])()
    }
    await boot({ suggest: { 'doc-o1': suggestAi(TWO_COL, { invoice_number: 'Invoice No' }) }, check: { 'doc-o1': recordThenDoubt } })
    await readToMapStep([{ name: 'a.csv', cols: TWO_COL, doc: 'doc-o1' }])

    expect(atCheck, 'control: the check ran').toHaveLength(1)
    expect(atCheck[0]!.suggests, 'the suggestion was requested before the check').toBe(1)
    expect(atCheck[0]!.step, 'the Map step was not open when the check was sent').not.toBe('mapping')
    expect(checkBodies[0]!.mapping, "the check carries the suggestion's placement").toEqual({ invoice_number: 'Invoice No' })
  })

  it('QA-CHKA-02: a failed check on one group leaves it, while the next group takes its doubt', async () => {
    await boot({
      suggest: {
        'doc-m1': suggestAi(TWO_COL, { invoice_number: 'Invoice No' }),
        'doc-m2': suggestAi(ALT_COL, { invoice_number: 'Ref Number' }),
      },
      check: { 'doc-m1': fail(500), 'doc-m2': doubts(['invoice_number']) },
    })
    await readToMapStep([
      { name: 'a.csv', cols: TWO_COL, doc: 'doc-m1' },
      { name: 'b.csv', cols: ALT_COL, doc: 'doc-m2' },
    ])

    expect(checkBodies.map((b) => b.document_id), 'both groups were checked, in order').toEqual(['doc-m1', 'doc-m2'])
    const groups = requireCtx().groups
    expect(groups.map((g) => g.preview.document_id)).toEqual(['doc-m1', 'doc-m2'])
    expect(groups[0]!.mapping.invoice_number, 'the failed check left group 1 as suggested').toBe('Invoice No')
    expect(groups[1]!.mapping.invoice_number, "group 2's doubt applied").toBeNull()
    expect(requireCtx().groupIndex).toBe(0)
  })

  it('QA-CHKA-03: doubting every placement opens a Map step with nothing placed and Import gated', async () => {
    await boot({
      suggest: { 'doc-all': suggestAi(AUTO_COLS, { invoice_number: 'Invoice No' }) },
      check: { 'doc-all': doubts(['invoice_number', 'issue_date', 'vat', 'total']) },
    })
    await readToMapStep([{ name: 'a.csv', cols: AUTO_COLS, doc: 'doc-all' }])

    expect(Object.keys(checkBodies[0]!.mapping).sort(), 'control: four placements were checked').toEqual([
      'invoice_number',
      'issue_date',
      'total',
      'vat',
    ])
    const snap = mapStepSnapshot()
    expect(snap).toEqual(AUTO_COLS.map((h) => [h, 'drop field']))
    expect(buttonByText('Map invoice number to continue'), 'Import stays gated').toBeDefined()
    expect(buttonByText('Import 1 rows')).toBeUndefined()
  })
})
