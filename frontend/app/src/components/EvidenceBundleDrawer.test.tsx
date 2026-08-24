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
import { bundleRequestFor } from '../lib/evidenceBundle'
import { EVIDENCE_COPY } from '../lib/evidenceBundleView'
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

function previewCalls(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter((call) => String(call[0]).includes('evidence-bundle'))
}

function parseCall(call: unknown[]): { entity_id: string | null; from: string | null; to: string | null } {
  const u = new URL(String(call[0]))
  return { entity_id: u.searchParams.get('entity_id'), from: u.searchParams.get('from'), to: u.searchParams.get('to') }
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', BASE)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve({}) }))
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
    const fetchMock = mockFetchSequence([{ ok: true, status: 200, json: () => Promise.resolve({}) }])
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
    const fetchMock = mockFetchSequence([{ ok: true, status: 200, json: () => Promise.resolve({}) }])
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
    const fetchMock = mockFetchSequence([{ ok: true, status: 200, json: () => Promise.resolve({}) }])
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
    const fetchMock = mockFetchSequence([{ ok: true, status: 200, json: () => Promise.resolve({}) }])
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
    // Not queryByTestId('evidence-period-custom') here: that id is shared by the "Custom
    // range" CHIP (always rendered) and this date-fields wrapper (conditional) -- see
    // EB-04-19, which pins the collision. `evidence-period-from` is the wrapper-only proof.
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
})
