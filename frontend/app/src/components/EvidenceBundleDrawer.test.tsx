// @vitest-environment jsdom
// AUDIT-08-04's RED specs. Authored RED against a stub (Stage 2.5 -- EvidenceBundleDrawer.tsx
// renders null), per the convention AUDIT-08-02's evidenceBundleView.ts stub set. Harness
// idiom copied from AuditView.test.tsx's (itself ApprovalsView.test.tsx's).

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuditEvent, AuditResponse } from '../lib/audit'
import type { AuditRange } from '../lib/auditFilters'
import { createAuthedFetch } from '../lib/authedFetch'
import { bundleRequestFor, type EvidenceBundlePreview } from '../lib/evidenceBundle'
import { BUNDLE_INVOICE_LIMIT, EVIDENCE_COPY, bundleBasisLine, bundleBlockReason, bundlePeriodLabel } from '../lib/evidenceBundleView'
import type { Entity } from '../lib/portfolio'
import type { PlatformCtx } from '../types'

import { AuditView } from './AuditView'
import { DATE_PRESETS } from './AuditFilterCard'
import { EvidenceBundleDrawer } from './EvidenceBundleDrawer'

const BASE = 'https://gw.test'
// Frozen so a preset's from/to can be asserted byte-exact against bundleRequestFor's own
// output, instead of a tolerance window that would also pass a wrong-by-a-day value.
const NOW = new Date('2026-08-24T12:00:00.000Z')

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

// Copied from AuditView.test.tsx: NOT a queue -- mockResolvedValue overwrites its own default
// on every iteration, so every call gets the LAST response passed in.
function mockFetchSequence(responses: MockResponse[]) {
  const fetchMock = vi.fn()
  for (const r of responses) fetchMock.mockResolvedValue(r)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function auditEvent(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'evt-1',
    created_at: '2026-08-20T09:15:00Z',
    event: 'invoice.created',
    actor: 'c0000000-0000-0000-0000-000000000001',
    actor_name: 'Chinedu Okafor',
    actor_kind: 'person',
    entity_id: 'ent-1',
    company_name: 'Honeywell Group',
    company_scope: 'company',
    payload: { id: 'inv-9', invoice_number: 'INV-9' },
    ...over,
  }
}

// Non-empty and unfiltered, so AuditView lands on 'loaded' -- the only state that renders
// audit-bundle-open.
function logResponse(over: Partial<AuditResponse> = {}): MockResponse {
  const body: AuditResponse = {
    events: [auditEvent()],
    page: { limit: 25, has_more: false, next_cursor: null },
    total: 1,
    log_is_empty: false,
    facets: { event: [], actor: [], company: [] },
    ...over,
  }
  return { ok: true, status: 200, json: () => Promise.resolve(body) }
}

function mkEntity(id: string, name: string, status: 'active' | 'archived' = 'active'): Entity {
  return { id, name, tin: null, registration: null, sector: null, address: null, status, created_at: '2026-01-01T00:00:00.000Z' }
}

function evidenceCtx(entities: Entity[] = []): PlatformCtx {
  return {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    entities,
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
  } as unknown as PlatformCtx
}

function renderDrawer(
  opts: {
    ctx?: PlatformCtx
    base?: string
    onClose?: () => void
    onToast?: (t: { kind: 'success' | 'error'; text: string }) => void
  } = {},
) {
  const onClose = opts.onClose ?? vi.fn()
  const onToast = opts.onToast ?? vi.fn()
  const ctx = opts.ctx ?? evidenceCtx()
  const base = opts.base ?? BASE
  const utils = render(<EvidenceBundleDrawer ctx={ctx} base={base} onClose={onClose} onToast={onToast} />)
  return { ...utils, onClose, onToast }
}

function readDrawerSource(): string {
  const dir = dirname(fileURLToPath(import.meta.url))
  return readFileSync(join(dir, 'EvidenceBundleDrawer.tsx'), 'utf8')
}

// Path-scoped, not a substring match: '.../evidence-bundle' (the download URL) also
// contains 'evidence-bundle', so a loose filter here would double-count as a preview call.
function pathOf(call: unknown[]): string {
  return new URL(String(call[0])).pathname
}
function previewCalls(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter((call) => pathOf(call).endsWith('/evidence-bundle/preview'))
}
function downloadCalls(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter((call) => pathOf(call).endsWith('/evidence-bundle'))
}

// Per-LEAF text, never one glued textContent blob -- adjacent siblings (e.g. the period
// label and the basis line) glue into one string under plain textContent, which false-trips
// a regex looking for a boundary that only exists between two separate leaves.
function leafTexts(root: HTMLElement): string[] {
  return [root, ...Array.from(root.querySelectorAll('*'))]
    .filter((el) => el.children.length === 0)
    .map((el) => el.textContent ?? '')
    .filter((s) => s.trim().length > 0)
}

function previewResponse(preview: EvidenceBundlePreview): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve(preview) }
}

function errorResponse(status: number, error: string): MockResponse {
  return { ok: false, status, json: () => Promise.resolve({ error }) }
}

// Mirrors evidenceBundleView.test.ts's fixture. Two deliberate divergences from the LOCAL
// selection: the entity name differs, and the period is not the 30d default -- a block that
// renders the selection instead of the server's response cannot pass against this pair.
const PREVIEW: EvidenceBundlePreview = {
  entity: { id: 'ent-a', name: 'Honeywell Group', tin: '12345678-0001' },
  period: { from: '2026-07-01T00:00:00Z', to: '2026-07-31T23:59:59Z', bounds: 'inclusive', basis: 'invoices.created_at' },
  filename: 'ASComply_evidence_Honeywell_Group_20260701_20260731.zip',
  counts: { invoices: 507, status_transitions: 2028, submissions: 1204, exchange_attempts: 1521, body_files: 3042 },
  over_limit: false,
}
const LOCAL = mkEntity('ent-a', 'Locally Picked Ltd')

function parseCall(call: unknown[]): { entity_id: string | null; from: string | null; to: string | null } {
  const u = new URL(String(call[0]))
  return { entity_id: u.searchParams.get('entity_id'), from: u.searchParams.get('from'), to: u.searchParams.get('to') }
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', BASE)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(previewResponse(PREVIEW)))
  // Only Date is faked -- setTimeout/setInterval stay real, so waitFor's polling and React's
  // own scheduling are unaffected. This lets from/to assert byte-exact, not just close.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  vi.useRealTimers()
})

describe('EvidenceBundleDrawer', () => {
  // EB-04-1a
  it('drawer_everyCloseRouteCallsOnCloseExactlyOnce', async () => {
    const routes: Array<() => void> = [
      () => fireEvent.keyDown(window, { key: 'Escape' }),
      () => fireEvent.click(screen.getByTestId('evidence-bundle-scrim')),
      () => fireEvent.click(screen.getByTestId('evidence-bundle-cancel')),
    ]
    for (const trigger of routes) {
      const { onClose } = await renderDrawer()
      trigger()
      expect(onClose).toHaveBeenCalledTimes(1)
      cleanup()
    }
  })

  // EB-04-1b
  it('drawer_escapeReallyUnmountsItThroughAuditView', async () => {
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={evidenceCtx()} />)
    fireEvent.click(await screen.findByTestId('audit-bundle-open'))
    await waitFor(() => expect(screen.getByTestId('evidence-bundle-drawer')).toBeTruthy())
    expect(screen.getByTestId('audit-bundle-open').getAttribute('aria-expanded')).toBe('true')
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByTestId('evidence-bundle-drawer')).toBeNull())
    expect(screen.getByTestId('audit-bundle-open').getAttribute('aria-expanded')).toBe('false')
  })

  // EB-04-1c
  it('drawerForm_closingIssuesNoRequest', async () => {
    const fetchMock = mockFetchSequence([logResponse()])
    render(<AuditView ctx={evidenceCtx()} />)
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('audit-log'))).toBe(true))

    const openThenClose = async (closeIt: () => void) => {
      fireEvent.click(await screen.findByTestId('audit-bundle-open'))
      await waitFor(() => expect(screen.getByTestId('evidence-bundle-drawer')).toBeTruthy())
      closeIt()
      await waitFor(() => expect(screen.queryByTestId('evidence-bundle-drawer')).toBeNull())
    }
    await openThenClose(() => fireEvent.keyDown(window, { key: 'Escape' }))
    await openThenClose(() => fireEvent.click(screen.getByTestId('evidence-bundle-scrim')))
    await openThenClose(() => fireEvent.click(screen.getByTestId('evidence-bundle-cancel')))

    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('evidence-bundle'))).toBe(false)
  })

  // EB-04-2
  it('drawer_pfDrawerClassIsOnThePanelItself', async () => {
    await renderDrawer()
    const panel = screen.getByTestId('evidence-bundle-drawer')
    expect(panel).toBe(screen.getByRole('dialog'))
    expect(panel.className).toContain('pf-drawer')
  })

  // EB-04-3
  it('drawerCompany_isSingleSelect', async () => {
    const entities = [mkEntity('ent-a', 'Alpha'), mkEntity('ent-b', 'Beta'), mkEntity('ent-c', 'Gamma')]
    await renderDrawer({ ctx: evidenceCtx(entities) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    const rows = entities.map((e) => screen.getByTestId(`evidence-company-row-${e.id}`))
    rows.forEach((r) => expect(r.getAttribute('aria-pressed')).toBe('false'))
    fireEvent.click(rows[0])
    fireEvent.click(rows[1])
    const pressed = rows.filter((r) => r.getAttribute('aria-pressed') === 'true')
    expect(pressed).toHaveLength(1)
    expect(pressed[0]).toBe(rows[1])
    expect(screen.getByTestId('evidence-company-trigger').textContent).toContain('Beta')
    const panel = screen.getByTestId('evidence-company-panel')
    expect(panel.querySelector('input[type="checkbox"]')).toBeNull()
    expect(screen.queryByText(/all compan/i)).toBeNull()
  })

  // EB-04-4
  it('drawerCompany_listsTheEntityListNotTheFacets', async () => {
    const entities = [mkEntity('ent-a', 'Alpha'), mkEntity('ent-b', 'Beta'), mkEntity('ent-c', 'Gamma')]
    mockFetchSequence([logResponse({ facets: { event: [], actor: [], company: [{ value: 'ent-zenith', name: 'Zenith', count: 4 }] } })])
    await renderDrawer({ ctx: evidenceCtx(entities) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    const panel = screen.getByTestId('evidence-company-panel')
    expect(within(panel).getAllByTestId(/^evidence-company-row-/)).toHaveLength(3)
    expect(within(panel).queryByText(/zenith/i)).toBeNull()
  })

  // EB-04-4b
  it('drawerCompany_includesAnArchivedEntity', async () => {
    const entities = [mkEntity('ent-x', 'Old Co', 'archived')]
    await renderDrawer({ ctx: evidenceCtx(entities) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    expect(screen.getByTestId('evidence-company-row-ent-x')).toBeTruthy()
    expect(screen.getByTestId('evidence-company-label-ent-x').textContent).toContain('Old Co')
  })

  // EB-04-5
  it('drawerCompany_helperIsTheCopyModulesSentence', async () => {
    await renderDrawer()
    expect(screen.getByTestId('evidence-company-helper').textContent).toBe(EVIDENCE_COPY.companyHelper)
    const src = readDrawerSource()
    expect(src.length).toBeGreaterThan(1000)
    expect(src).toContain('FilterPopover')
    // The DOM check alone passes even if the sentence is hardcoded as a literal; a compliant
    // drawer's source has the IDENTIFIER, never the sentence text itself.
    expect(src, 'the sentence must be read from EVIDENCE_COPY, not typed inline').not.toContain(EVIDENCE_COPY.companyHelper)
  })

  // EB-04-6
  it('drawerPeriod_offersAuditSevensPresets', async () => {
    expect(DATE_PRESETS.length).toBe(4)
    await renderDrawer()
    const chips = DATE_PRESETS.map((p) => screen.getByTestId(`evidence-period-${p.id}`))
    expect(chips.map((c) => c.textContent)).toEqual(DATE_PRESETS.map((p) => p.label))
  })

  // EB-04-7
  it('drawerForm_oneRequestPerSelectionChange', async () => {
    const entities = [mkEntity('ent-a', 'Alpha')]
    const fetchMock = mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx(entities) })
    expect(previewCalls(fetchMock)).toHaveLength(0)

    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await waitFor(() => expect(previewCalls(fetchMock)).toHaveLength(1))
    const first = parseCall(previewCalls(fetchMock)[0])
    expect(first.entity_id).toBe('ent-a')
    const range30d: AuditRange = { preset: '30d' }
    const expected = bundleRequestFor('ent-a', range30d, new Date())
    expect(first.from).toBe(expected?.from)
    expect(first.to).toBe(expected?.to)

    fireEvent.click(screen.getByTestId('evidence-period-7d'))
    await waitFor(() => expect(previewCalls(fetchMock)).toHaveLength(2))
    const second = parseCall(previewCalls(fetchMock)[1])
    // Byte-exact, not just "differs from the first": a merely-different from would also be
    // satisfied by the wrong preset (24h instead of 7d).
    const expected7d = bundleRequestFor('ent-a', { preset: '7d' }, new Date())
    expect(second.from).toBe(expected7d?.from)
    expect(second.to).toBe(expected7d?.to)
  })

  // EB-04-9
  it('drawerDisclosure_isAnInlineSvgChevronAndNoBackgroundImage', async () => {
    await renderDrawer()
    const trigger = screen.getByTestId('evidence-company-trigger')
    const closedChevron = screen.getByTestId('evidence-company-chevron')
    expect(closedChevron.querySelector('svg')).toBeTruthy()
    const closedTransform = (closedChevron as HTMLElement).style.transform
    fireEvent.click(trigger)
    const openChevron = screen.getByTestId('evidence-company-chevron')
    expect((openChevron as HTMLElement).style.transform).not.toBe(closedTransform)

    const src = readDrawerSource()
    expect(src.length).toBeGreaterThan(1000)
    expect(src).toContain('FilterPopover')
    expect(src).not.toMatch(/background-image/i)
    expect(src).not.toMatch(/backgroundImage/)
  })

  // EB-04-10
  it('drawerEscape_closesTheInnermostSurfaceFirst', async () => {
    const onClose = vi.fn()
    await renderDrawer({ onClose })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    expect(screen.getByTestId('evidence-company-panel')).toBeTruthy()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByTestId('evidence-company-panel')).toBeNull()
    expect(screen.getByTestId('evidence-bundle-drawer')).toBeTruthy()
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  // EB-04-11
  it('drawer_opensWithNoCompanyAndTheThirtyDayDefault', async () => {
    await renderDrawer()
    expect(screen.getByTestId('evidence-company-trigger').textContent).toContain(EVIDENCE_COPY.companyPlaceholder)
    expect(screen.getByTestId('evidence-period-30d').getAttribute('aria-pressed')).toBe('true')
    for (const id of ['24h', '7d', 'custom']) {
      expect(screen.getByTestId(`evidence-period-${id}`).getAttribute('aria-pressed')).toBe('false')
    }
  })

  // EB-04-12
  it('drawerPeriod_customCommitsOnlyWhenBothDatesAreSet', async () => {
    const entities = [mkEntity('ent-a', 'Alpha')]
    const fetchMock = mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx(entities) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await waitFor(() => expect(previewCalls(fetchMock).length).toBeGreaterThan(0))
    const baseline = previewCalls(fetchMock).length
    // The 30d default fired on company pick -- pin it exactly, now that the clock is frozen.
    const baselineCall = parseCall(previewCalls(fetchMock)[baseline - 1])
    const expectedBaseline = bundleRequestFor('ent-a', { preset: '30d' }, new Date())
    expect(baselineCall.from).toBe(expectedBaseline?.from)
    expect(baselineCall.to).toBe(expectedBaseline?.to)

    fireEvent.click(screen.getByTestId('evidence-period-custom'))
    const from = screen.getByTestId('evidence-period-from') as HTMLInputElement
    const to = screen.getByTestId('evidence-period-to') as HTMLInputElement
    expect(from.getAttribute('type')).toBe('date')
    expect(from.className).toBe('pf-input')
    expect(to.getAttribute('type')).toBe('date')
    expect(to.className).toBe('pf-input')
    expect(previewCalls(fetchMock).length).toBe(baseline)

    fireEvent.change(from, { target: { value: '2026-05-01' } })
    expect(previewCalls(fetchMock).length).toBe(baseline)

    fireEvent.change(to, { target: { value: '2026-05-10' } })
    await waitFor(() => expect(previewCalls(fetchMock).length).toBe(baseline + 1))
    const calls = previewCalls(fetchMock)
    const last = parseCall(calls[calls.length - 1])
    expect(last.from).toMatch(/T00:00:00\.000Z$/)
    expect(last.to).toMatch(/T23:59:59\.999Z$/)
  })

  // EB-04-13
  it('drawer_panelGeometryMatchesTheSiblings', async () => {
    await renderDrawer()
    const panel = screen.getByTestId('evidence-bundle-drawer') as HTMLElement
    expect(panel.style.width).toBe('560px')
    expect(panel.style.maxWidth).toBe('94vw')
    expect(panel.style.position).toBe('fixed')
    expect(panel.style.top).toBe('0px')
    expect(panel.style.right).toBe('0px')
    expect(panel.style.bottom).toBe('0px')
  })

  // EB-04-14 -- zero entities: floor is the panel itself rendering, not a row count of zero.
  it('drawerCompany_zeroEntitiesRendersEmptyPopoverAndFiresNoPreview', async () => {
    const fetchMock = mockFetchSequence([logResponse()])
    render(<AuditView ctx={evidenceCtx([])} />)
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('audit-log'))).toBe(true))
    fireEvent.click(await screen.findByTestId('audit-bundle-open'))
    await waitFor(() => expect(screen.getByTestId('evidence-bundle-drawer')).toBeTruthy())

    const trigger = screen.getByTestId('evidence-company-trigger')
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(trigger)
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByTestId('evidence-company-panel')).toBeTruthy()
    expect(screen.queryAllByTestId(/^evidence-company-row-/)).toHaveLength(0)
    expect(previewCalls(fetchMock)).toHaveLength(0)
  })

  // EB-04-15 -- localeCompare, not insertion order and not a bare lexicographic sort:
  // 'apple' < 'Banana' < 'cherry' under localeCompare, but insertion order (and a bare
  // .sort() with no comparator) both give Banana/apple/cherry (uppercase sorts first).
  it('drawerCompany_sortIsLocaleAwareNotInsertionOrder', async () => {
    const entities = [mkEntity('ent-b', 'Banana Corp'), mkEntity('ent-a', 'apple Ltd'), mkEntity('ent-c', 'cherry Inc')]
    await renderDrawer({ ctx: evidenceCtx(entities) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    const panel = screen.getByTestId('evidence-company-panel')
    const rows = within(panel).getAllByTestId(/^evidence-company-row-/)
    expect(rows).toHaveLength(3)
    expect(rows.map((r) => r.textContent)).toEqual(['apple Ltd', 'Banana Corp', 'cherry Inc'])
  })

  // EB-04-16 -- reqKey is unchanged by a same-value reselect (new object, same id/name),
  // so the effect's deps comparison must not refire.
  it('drawerForm_reselectingTheSameCompanyFiresNoSecondRequest', async () => {
    const entities = [mkEntity('ent-a', 'Alpha')]
    const fetchMock = mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx(entities) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await waitFor(() => expect(previewCalls(fetchMock)).toHaveLength(1))

    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await new Promise((resolve) => setTimeout(resolve, 10))
    expect(previewCalls(fetchMock)).toHaveLength(1)
  })

  // EB-04-17 -- a half-entered Custom range never commits (baseline stays put), and
  // abandoning it for a preset fires that preset's own request, not a fused leftover.
  it('drawerPeriod_switchingAwayFromCustomDiscardsAPartialDate', async () => {
    const entities = [mkEntity('ent-a', 'Alpha')]
    const fetchMock = mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx(entities) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await waitFor(() => expect(previewCalls(fetchMock).length).toBeGreaterThan(0))
    const baseline = previewCalls(fetchMock).length

    fireEvent.click(screen.getByTestId('evidence-period-custom'))
    fireEvent.change(screen.getByTestId('evidence-period-from'), { target: { value: '2026-05-01' } })
    expect(previewCalls(fetchMock).length).toBe(baseline)

    fireEvent.click(screen.getByTestId('evidence-period-24h'))
    await waitFor(() => expect(previewCalls(fetchMock).length).toBe(baseline + 1))
    // 'evidence-period-custom' is the always-rendered chip's own testid; 'evidence-period-from' exists only inside the conditional wrapper, so its absence is the real proof.
    expect(screen.queryByTestId('evidence-period-from')).toBeNull()
    const last = parseCall(previewCalls(fetchMock)[previewCalls(fetchMock).length - 1])
    const expected24h = bundleRequestFor('ent-a', { preset: '24h' }, new Date())
    expect(last.from).toBe(expected24h?.from)
    expect(last.to).toBe(expected24h?.to)
  })

  // EB-04-18 -- the scrim's onClose is unconditional (task-667 §5), unlike Escape's
  // !companyOpen gate (EB-04-10): clicking it closes the drawer even mid-popover.
  it('drawer_scrimClosesEvenWithThePopoverOpenUnlikeEscape', async () => {
    const onClose = vi.fn()
    await renderDrawer({ onClose })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    expect(screen.getByTestId('evidence-company-panel')).toBeTruthy()
    fireEvent.click(screen.getByTestId('evidence-bundle-scrim'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  // AUDIT-08-05 -- the confirmation block, the pre-build refusals and the two suppression
  // gates (staleness, the client's own reason over the server's echoed 400).

  // EB-05-1
  it('confirmBlock_statesCompanyPeriodFilenameBeforeAnyDownload', async () => {
    const fetchMock = mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    await screen.findByTestId('evidence-confirm-block')
    // The server's name/period, never the local selection -- LOCAL and PREVIEW deliberately differ.
    expect(screen.getByTestId('evidence-confirm-company').textContent).toBe('Honeywell Group')
    expect(screen.getByTestId('evidence-confirm-period').textContent).toBe(bundlePeriodLabel(PREVIEW.period))
    expect(screen.getByTestId('evidence-confirm-filename').textContent).toBe(PREVIEW.filename)
    expect(downloadCalls(fetchMock)).toHaveLength(0)
  })

  // EB-05-2
  it('confirmBlock_filenameIsThePreviewsNotAComputedOne', async () => {
    const sentinel = 'sentinel_filename_do_not_recompute.zip'
    mockFetchSequence([previewResponse({ ...PREVIEW, filename: sentinel })])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    await waitFor(() => expect(screen.getByTestId('evidence-confirm-filename').textContent).toBe(sentinel))

    const src = readDrawerSource()
    expect(src.length).toBeGreaterThan(1000)
    expect(src).toContain('FilterPopover')
    // No client-side name construction: a compliant drawer's source never carries the prefix.
    expect(src).not.toContain('ASComply_evidence')
  })

  // EB-05-3 -- see the vacuity audit: bundlePeriodLabel ends in a year and bundleBasisLine
  // starts with 'Both' for an inclusive period, so a glued textContent read matches this
  // regex on CORRECT copy. leafTexts() is the fix.
  it('confirmBlock_statesNoSize', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')

    const SIZE_RE = /\d+(\.\d+)?\s?(B|KB|MB|GB)/
    const panel = screen.getByTestId('evidence-bundle-drawer')
    const texts = leafTexts(panel)
    // Floor: a non-trivial haystack that holds two known needles.
    expect(texts.length).toBeGreaterThanOrEqual(20)
    expect(texts).toContain(PREVIEW.filename)
    expect(texts).toContain(EVIDENCE_COPY.confirmFooter)
    // Control: the scanner fires on a planted size sentence.
    expect(['The bundle is 4.2 MB.'].filter((s) => SIZE_RE.test(s))).toHaveLength(1)
    // Claim: no single leaf states a size.
    expect(texts.filter((s) => SIZE_RE.test(s))).toEqual([])
  })

  // EB-05-4
  it('confirmBlock_neverSaysSigned', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')

    // Not /sign/i -- rowManifest deliberately carries "not a cryptographic signature".
    const SIGNED_RE = /signed/i
    const panel = screen.getByTestId('evidence-bundle-drawer')
    const texts = leafTexts(panel)
    expect(texts.length).toBeGreaterThanOrEqual(20)
    expect(texts).toContain(PREVIEW.filename)
    expect(texts).toContain(EVIDENCE_COPY.confirmFooter)
    expect(['A signed SHA-256 manifest is attached.'].filter((s) => SIGNED_RE.test(s))).toHaveLength(1)
    expect(texts.filter((s) => SIGNED_RE.test(s))).toEqual([])

    // U+00B7, asserted before use -- the EB-02-17 idiom.
    const MID = '·'
    expect(MID.charCodeAt(0)).toBe(0x00b7)
    expect(texts.some((t) => t.includes(`MANIFEST ${MID} SHA-256`))).toBe(true)
  })

  // EB-05-5
  it('confirmBlock_footerSaysNothingDownloadsUntilYouConfirm', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    await waitFor(() => expect(screen.getByTestId('evidence-confirm-footnote').textContent).toBe(EVIDENCE_COPY.confirmFooter))

    const src = readDrawerSource()
    expect(src.length).toBeGreaterThan(1000)
    expect(src).toContain('FilterPopover')
    // DOM equality alone passes on a hardcoded literal too -- the source must read the key.
    expect(src).not.toContain(EVIDENCE_COPY.confirmFooter)
  })

  // EB-05-6
  it('confirmBlock_zeroInvoicesRefusesBeforeTheBuild', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    // Control: the happy path leaves Prepare enabled -- without this, "disabled" below proves nothing.
    expect((screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement).disabled).toBe(false)
    cleanup()

    const empty = { ...PREVIEW, counts: { ...PREVIEW.counts, invoices: 0 } }
    const fetchMock = mockFetchSequence([previewResponse(empty)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    const expectedReason = bundleBlockReason({ kind: 'empty', company: 'Honeywell Group', period: bundlePeriodLabel(PREVIEW.period) })
    await waitFor(() => expect(screen.getByTestId('evidence-bundle-reason').textContent).toBe(expectedReason))
    expect(expectedReason).toContain('Honeywell Group')
    expect(expectedReason).toContain('1 July 2026')

    const prepare = screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement
    expect(prepare.disabled).toBe(true)

    await waitFor(() => expect(previewCalls(fetchMock)).toHaveLength(1))
    fireEvent.click(prepare)
    expect(downloadCalls(fetchMock)).toHaveLength(0)
  })

  // EB-05-7
  it('confirmBlock_disabledPrepareNeutralisesFilter', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    const enabled = screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement
    expect(enabled.style.filter).toBe('')
    expect(enabled.style.background).toBe('')
    cleanup()

    const empty = { ...PREVIEW, counts: { ...PREVIEW.counts, invoices: 0 } }
    mockFetchSequence([previewResponse(empty)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await waitFor(() => expect(screen.getByTestId('evidence-bundle-reason')).toBeTruthy())

    const disabled = screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement
    expect(disabled.style.filter).toBe('none')
    expect(disabled.style.background).toBe('var(--bg-3)')
    expect(disabled.style.cursor).toBe('not-allowed')
  })

  // EB-05-8
  it('confirmBlock_overLimitRefusesAndNamesBothNumbers', async () => {
    const overLimit = { ...PREVIEW, over_limit: true, counts: { ...PREVIEW.counts, invoices: 12345 } }
    const fetchMock = mockFetchSequence([previewResponse(overLimit)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    const expectedReason = bundleBlockReason({ kind: 'over-limit', invoices: 12345, limit: BUNDLE_INVOICE_LIMIT })
    await waitFor(() => expect(screen.getByTestId('evidence-bundle-reason').textContent).toBe(expectedReason))
    // The AC's own claim, independent of the lib producing the sentence.
    expect(expectedReason).toContain('12,345')
    expect(expectedReason).toContain('10,000')

    const prepare = screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement
    expect(prepare.disabled).toBe(true)
    expect(prepare.style.filter).toBe('none')

    await waitFor(() => expect(previewCalls(fetchMock)).toHaveLength(1))
    expect(downloadCalls(fetchMock)).toHaveLength(0)
  })

  // EB-05-9
  it('confirmBlock_a404NeverClaimsNonExistence', async () => {
    mockFetchSequence([errorResponse(404, 'not found')])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    const error = await screen.findByTestId('evidence-bundle-error')
    // Floor: the card is present and IS the 404 path, not some other failure.
    expect(error.textContent).toContain('not found')
    expect(error.textContent).toContain('HTTP 404')

    const NON_EXISTENCE_RE = /does not exist|no such company/i
    // Control: the scanner fires on a planted non-existence claim.
    expect(['This company does not exist.'].filter((s) => NON_EXISTENCE_RE.test(s))).toHaveLength(1)

    // Claim: D-26's indistinguishability, preserved by construction over the whole panel.
    const panel = screen.getByTestId('evidence-bundle-drawer')
    expect(leafTexts(panel).filter((s) => NON_EXISTENCE_RE.test(s))).toEqual([])

    expect(screen.queryByTestId('evidence-confirm-block')).toBeNull()
    expect((screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement).disabled).toBe(true)
  })

  // EB-05-10 -- jsdom applies no stylesheet, so only the inline `style` attribute is a live
  // oracle, and it must be scoped to the block: ErrorState ships '#fff' and the drawer's own
  // scrim ships an oklch() literal, both correct and both outside this block.
  it('confirmBlock_usesTealTokensOnly', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    const block = await screen.findByTestId('evidence-confirm-block')
    const heading = screen.getByTestId('evidence-confirm-heading')
    expect(block.style.background).toBe('var(--action-tint)')
    expect(block.style.border).toBe('1px solid var(--teal-200)')
    expect(heading.style.color).toBe('var(--action)')

    const nodes = [block, ...Array.from(block.querySelectorAll('*'))]
    expect(nodes.length).toBeGreaterThanOrEqual(15)
    const withVarBg = nodes.filter((n) => (n.getAttribute('style') ?? '').includes('var(--'))
    expect(withVarBg.length).toBeGreaterThanOrEqual(5)

    const OKLCH_RE = /oklch\(/i
    const HEX_RE = /#[0-9a-f]{3,8}\b/i
    // Control: both regexes fire on a planted literal-colour declaration.
    const planted = 'color: #ff0000; background: oklch(50% 0 0)'
    expect(HEX_RE.test(planted)).toBe(true)
    expect(OKLCH_RE.test(planted)).toBe(true)

    for (const n of nodes) {
      const style = n.getAttribute('style') ?? ''
      expect(style).not.toMatch(OKLCH_RE)
      expect(style).not.toMatch(HEX_RE)
    }
  })

  // EB-05-11
  it('confirmBlock_prepareHelperSaysSizeIsUnknownUntilBuilt', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))

    const helper = await screen.findByTestId('evidence-prepare-helper')
    expect(helper.textContent).toBe(EVIDENCE_COPY.prepareHelper)
    expect(helper.textContent).toContain('not known until the bundle is built')
    expect(helper.textContent).toContain('held in your browser until you save')
    expect(helper.textContent).not.toMatch(/\d/)

    const prepare = screen.getByTestId('evidence-bundle-prepare')
    expect(prepare.getAttribute('aria-describedby')).toContain('evidence-prepare-helper')

    const src = readDrawerSource()
    expect(src.length).toBeGreaterThan(1000)
    expect(src).toContain('FilterPopover')
    expect(src).not.toContain(EVIDENCE_COPY.prepareHelper)
  })

  // EB-05-12 -- re-homed EB-04-8. The plan's literal "settle the first promise late" step
  // cannot be replayed on the SAME promise (a Promise settles once); rungs (i)-(iii) below
  // are the plan's abandoned-pending-request scenario verbatim, and the "slow first, fast
  // second" regression (rung iv, over useAsync's inherited runId) is reproduced with a
  // fresh pair of requests rather than reusing the already-settled first one.
  it('confirmBlock_aStaleResponseNeverRenders', async () => {
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn(() => new Promise<MockResponse>((resolve) => resolvers.push(resolve)))
    vi.stubGlobal('fetch', fetchMock)

    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await waitFor(() => expect(resolvers).toHaveLength(1))
    resolvers[0](previewResponse({ ...PREVIEW, counts: { ...PREVIEW.counts, invoices: 507 } }))
    await waitFor(() =>
      expect(screen.getAllByTestId('evidence-confirm-row-value').map((v) => v.textContent)).toContain('507'),
    )

    // The new selection's request is left pending -- the guard must drop the block on the
    // key change alone, before any response has landed.
    fireEvent.click(screen.getByTestId('evidence-period-7d'))
    await waitFor(() => expect(resolvers).toHaveLength(2))
    expect(screen.queryByTestId('evidence-confirm-block')).toBeNull()
    expect(screen.queryByText('507')).toBeNull()

    resolvers[1](previewResponse({ ...PREVIEW, counts: { ...PREVIEW.counts, invoices: 42 } }))
    await waitFor(() =>
      expect(screen.getAllByTestId('evidence-confirm-row-value').map((v) => v.textContent)).toContain('42'),
    )
    expect(screen.queryByText('507')).toBeNull()

    // Regression rung: fire a slow request, then a fast one that lands first, then settle
    // the slow one late -- it must not clobber the fast one's already-rendered state.
    fireEvent.click(screen.getByTestId('evidence-period-24h'))
    await waitFor(() => expect(resolvers).toHaveLength(3))
    fireEvent.click(screen.getByTestId('evidence-period-custom'))
    fireEvent.change(screen.getByTestId('evidence-period-from'), { target: { value: '2026-06-01' } })
    fireEvent.change(screen.getByTestId('evidence-period-to'), { target: { value: '2026-06-05' } })
    await waitFor(() => expect(resolvers).toHaveLength(4))

    resolvers[3](previewResponse({ ...PREVIEW, counts: { ...PREVIEW.counts, invoices: 99 } }))
    await waitFor(() =>
      expect(screen.getAllByTestId('evidence-confirm-row-value').map((v) => v.textContent)).toContain('99'),
    )

    resolvers[2](previewResponse({ ...PREVIEW, counts: { ...PREVIEW.counts, invoices: 12 } }))
    await new Promise((resolve) => setTimeout(resolve, 10))
    const finalValues = screen.getAllByTestId('evidence-confirm-row-value').map((v) => v.textContent)
    expect(finalValues).toContain('99')
    expect(finalValues).not.toContain('12')
  })

  // EB-05-13 -- the oracle for `landed.key === reqKey`: a deps change that does NOT re-run
  // (req becomes null, immediate:false) leaves useAsync's own state untouched, so only the
  // key compare can hide the block.
  it('confirmBlock_anAbandonedPeriodDropsTheLandedPreview', async () => {
    const fetchMock = mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    await waitFor(() => expect(previewCalls(fetchMock)).toHaveLength(1))

    fireEvent.click(screen.getByTestId('evidence-period-custom'))

    expect(screen.queryByTestId('evidence-confirm-block')).toBeNull()
    expect(screen.queryByTestId('evidence-prepare-helper')).toBeNull()
    expect(screen.getByTestId('evidence-bundle-reason').textContent).toBe(EVIDENCE_COPY.noPeriodReason)
    expect((screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement).disabled).toBe(true)
    // No new request fired -- proving the guard, not a refetch, hid the block.
    expect(previewCalls(fetchMock)).toHaveLength(1)
  })

  // EB-05-14 -- pins the `block == null` term of the error gate: an invalid range is caught
  // client-side before the server's echo of the same fact ever gets to speak.
  it('confirmBlock_anInvalidRangeStatesItsOwnReasonNotTheServers', async () => {
    const fetchMock = mockFetchSequence([errorResponse(400, 'from must be before to')])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    fireEvent.click(screen.getByTestId('evidence-period-custom'))
    fireEvent.change(screen.getByTestId('evidence-period-from'), { target: { value: '2026-05-10' } })
    fireEvent.change(screen.getByTestId('evidence-period-to'), { target: { value: '2026-05-01' } })

    expect(screen.getByTestId('evidence-bundle-reason').textContent).toBe(EVIDENCE_COPY.invalidRangeReason)
    await waitFor(() => expect(previewCalls(fetchMock)).toHaveLength(1))
    // Let the stubbed 400 settle before asserting it never surfaces its own card.
    await new Promise((resolve) => setTimeout(resolve, 10))

    expect(screen.queryByTestId('evidence-bundle-error')).toBeNull()
    const panel = screen.getByTestId('evidence-bundle-drawer')
    expect(leafTexts(panel).some((s) => s.includes('from must be before to'))).toBe(false)
    expect((screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement).disabled).toBe(true)
  })

  // QA (AUDIT-08-05 Stage 4) -- mutation testing found AC-4 (the basis line) had no oracle
  // at all: deleting evidence-confirm-basis entirely left all 34 specs green. These three
  // fixtures differ in bounds/basis, so no single hardcoded sentence could satisfy all three
  // -- the same discriminator EB-04-5 uses for static copy, applied to a derived one.
  it('confirmBlock_basisLineStatesTheInclusiveBoundsHonestly', async () => {
    mockFetchSequence([previewResponse(PREVIEW)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    const text = screen.getByTestId('evidence-confirm-basis').textContent
    expect(text).toBe(bundleBasisLine(PREVIEW.period))
    expect(text).toMatch(/^Both dates are included\./)
  })

  it('confirmBlock_basisLineOmitsTheInclusiveClaimWhenBoundsAreNotInclusive', async () => {
    const exclusive = { ...PREVIEW, period: { ...PREVIEW.period, bounds: 'exclusive' } }
    mockFetchSequence([previewResponse(exclusive)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    const text = screen.getByTestId('evidence-confirm-basis').textContent
    expect(text).toBe(bundleBasisLine(exclusive.period))
    expect(text).not.toMatch(/^Both dates are included\./)
  })

  it('confirmBlock_basisLineNamesAnUnrecognisedBasisPlainly', async () => {
    const oddBasis = { ...PREVIEW, period: { ...PREVIEW.period, basis: 'submissions.created_at' } }
    mockFetchSequence([previewResponse(oddBasis)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    const text = screen.getByTestId('evidence-confirm-basis').textContent
    expect(text).toBe(bundleBasisLine(oddBasis.period))
    expect(text).toContain('submissions.created_at')
  })

  // A single invoice must not read "1 invoices" anywhere on the panel.
  it('confirmBlock_singleInvoiceRowNeverPluralizesAsInvoices', async () => {
    const one = { ...PREVIEW, counts: { ...PREVIEW.counts, invoices: 1 } }
    mockFetchSequence([previewResponse(one)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    const panel = screen.getByTestId('evidence-bundle-drawer')
    const texts = leafTexts(panel)
    const BAD_RE = /\b1\s+invoices\b/i
    // Control: the scanner fires on a planted mis-pluralisation.
    expect(['1 invoices were exported.'].filter((s) => BAD_RE.test(s))).toHaveLength(1)
    expect(texts.filter((s) => BAD_RE.test(s))).toEqual([])
    expect(texts).toContain('1')
  })

  // bundleBlockFor checks counts.invoices === 0 before over_limit -- pin the precedence
  // through the whole wired component, not just the lib.
  it('confirmBlock_emptyRefusalWinsOverOverLimitWhenBothConditionsHold', async () => {
    const both = { ...PREVIEW, over_limit: true, counts: { ...PREVIEW.counts, invoices: 0 } }
    mockFetchSequence([previewResponse(both)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    const expectedEmpty = bundleBlockReason({ kind: 'empty', company: 'Honeywell Group', period: bundlePeriodLabel(PREVIEW.period) })
    await waitFor(() => expect(screen.getByTestId('evidence-bundle-reason').textContent).toBe(expectedEmpty))
    expect(screen.getByTestId('evidence-bundle-reason').textContent).not.toContain('10,000')
  })

  // A 500 is not a 404: the generic surface renders, and carries neither the 404's
  // sentinel nor a non-existence claim.
  it('confirmBlock_a500RendersAGenericErrorNeverA404Claim', async () => {
    mockFetchSequence([errorResponse(500, 'internal error')])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    const error = await screen.findByTestId('evidence-bundle-error')
    expect(error.textContent).toContain('internal error')
    expect(error.textContent).toContain('HTTP 500')
    expect(error.textContent).not.toContain('HTTP 404')
    expect(screen.queryByTestId('evidence-confirm-block')).toBeNull()
    expect((screen.getByTestId('evidence-bundle-prepare') as HTMLButtonElement).disabled).toBe(true)
  })

  // Regex/wordBreak-hostile characters -- apostrophe, ampersand, parens, slash, em dash,
  // diacritics -- must render verbatim, not be stripped, escaped or truncated.
  it('confirmBlock_specialCharacterEntityNameAndFilenameRenderVerbatim', async () => {
    const weird = {
      ...PREVIEW,
      entity: { id: 'ent-a', name: "O'Réilly & Zàng (Nig.) Ltd — Special/Chars", tin: '12345678-0001' },
      filename: "ASComply_evidence_O'Reilly_&_Zang_(Special-Chars)_20260701_20260731.zip",
    }
    mockFetchSequence([previewResponse(weird)])
    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await screen.findByTestId('evidence-confirm-block')
    expect(screen.getByTestId('evidence-confirm-company').textContent).toBe(weird.entity.name)
    expect(screen.getByTestId('evidence-confirm-filename').textContent).toBe(weird.filename)
  })

  // QA -- EB-05-14 as shipped fires its 400 for a request that gets discarded by useAsync's
  // runId before it can settle (the deps change to Custom happens before the mocked promise's
  // microtask fires), so it never actually exercises the `block == null` term: preview.error
  // stays null throughout that test. This spec lets the FIRST request genuinely settle into
  // an error, then abandons the selection into a `no-period` block -- the one sequence where
  // a real (non-discarded) stale error and a non-null block coexist.
  it('confirmBlock_aSettledErrorIsSuppressedByALaterClientSideRefusal', async () => {
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn(() => new Promise<MockResponse>((resolve) => resolvers.push(resolve)))
    vi.stubGlobal('fetch', fetchMock)

    await renderDrawer({ ctx: evidenceCtx([LOCAL]) })
    fireEvent.click(screen.getByTestId('evidence-company-trigger'))
    fireEvent.click(screen.getByTestId('evidence-company-row-ent-a'))
    await waitFor(() => expect(resolvers).toHaveLength(1))
    resolvers[0](errorResponse(400, 'from must be before to'))
    // Baseline: with nothing else in the way, the settled error DOES render.
    const error = await screen.findByTestId('evidence-bundle-error')
    expect(error.textContent).toContain('from must be before to')

    fireEvent.click(screen.getByTestId('evidence-period-custom'))
    expect(screen.queryByTestId('evidence-bundle-error')).toBeNull()
    expect(screen.getByTestId('evidence-bundle-reason').textContent).toBe(EVIDENCE_COPY.noPeriodReason)
  })
})
