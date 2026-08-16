// @vitest-environment jsdom
// First jsdom coverage of either nav badge. Mirrors DashboardActive.test.tsx's fetch-mock
// + ctx-cast idiom (single-endpoint mock: Sidebar fires only getRollup).
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { Counts, Rollup, RollupClient } from '../lib/dashboard'
import type { PlatformCtx } from '../types'
import { Sidebar } from './Sidebar'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

const ZERO_COUNTS: Counts = { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 }

interface BucketFixture {
  validated: number
  awaitingApproval: number
  needsAttention: number
}

const ENTITY_ID = 'e1'

function clientRow(f: BucketFixture): RollupClient {
  return {
    entity_id: ENTITY_ID,
    entity_name: 'Acme Ltd',
    counts: { ...ZERO_COUNTS, validated: f.validated },
    needs_attention: f.needsAttention,
    awaiting_approval: f.awaitingApproval,
    metrics: {},
    top_violations: [],
  }
}

// The three numbers below are kept MUTUALLY DISTINCT in every fixture: a badge wired to
// the wrong field then renders a different string, never a coincidentally equal one. When
// `entity` is supplied its three are distinct from the totals' three as well, so a badge
// reading the wrong SCOPE is as visible as one reading the wrong field.
function rollup(f: BucketFixture & { entity?: BucketFixture }): Rollup {
  return {
    totals: {
      counts: { ...ZERO_COUNTS, validated: f.validated },
      needs_attention: f.needsAttention,
      awaiting_approval: f.awaitingApproval,
      metrics: {},
      top_violations: [],
    },
    clients: f.entity ? [clientRow(f.entity)] : [],
    top_violations: [],
  }
}

// mode 'inhouse' resolves scopedBucket straight to rollup.totals. Both modes carry the
// Approvals item since APPR-12-05; firm resolves the SELECTED entity's row instead.
function sidebarCtx(over: Record<string, unknown> = {}): PlatformCtx {
  const ctx = {
    mode: 'inhouse',
    active: { short: 'Acme', initials: 'AC', tin: '12345678-0001', entityId: null },
    clients: [],
    entities: [],
    user: { name: 'Ada Nwosu', initials: 'AN', verified: false, tenantName: null },
    view: 'dashboard',
    switcherOpen: false,
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    toggleSwitcher: vi.fn(),
    switchClient: vi.fn(),
    nav: vi.fn(),
    signOut: vi.fn(),
    ...over,
  }
  return ctx as unknown as PlatformCtx
}

// Firm mode: the switcher needs a client row whose entity is `active` and visible, else
// scopedBucket has nothing to scope to and every badge is EMPTY_BUCKET's zero.
function firmCtx(over: Record<string, unknown> = {}): PlatformCtx {
  return sidebarCtx({
    mode: 'firm',
    active: { short: 'Acme', initials: 'AC', tin: '12345678-0001', entityId: ENTITY_ID },
    clients: [{ entityId: ENTITY_ID, name: 'Acme Ltd', short: 'Acme', initials: 'AC' }],
    entities: [{ id: ENTITY_ID, status: 'active' }],
    switcherOpen: true,
    ...over,
  })
}

function mockRollupFetch(data: Rollup) {
  const fetchMock = vi.fn(() => Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(data) }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// A rollup held in flight until the test releases it, so the not-ready leg is asserted
// while the fetch is PROVABLY unresolved rather than merely early.
function deferredRollupFetch() {
  let release!: (data: Rollup) => void
  const inFlight = new Promise<Rollup>((r) => {
    release = r
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(() => inFlight.then((data): MockResponse => ({ ok: true, status: 200, json: () => Promise.resolve(data) }))),
  )
  return release
}

function navButton(label: string): HTMLElement {
  return screen.getByText(label).closest('button')!
}

// The badge span is the only `.mono` inside a nav button -- the glyph beside it is an
// inline <svg> carrying no text.
function badgeOf(label: string): HTMLElement | null {
  return navButton(label).querySelector('span.mono')
}

// The switcher row's second line, the one entityHealth feeds.
function switcherSubLabel(): string {
  return within(screen.getByTestId('company-switcher-option')).getByText(/needing attention|all clear|no invoices yet|—/).textContent!
}

// Without this stub gatewayBase() returns null, useAsync never fires, `bucket` stays null
// and both badges stay absent -- every assertion below would pass against an empty nav.
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

// The Invoices badge is the settle signal: it reads the same one rollup fetch, so waiting
// on it proves `bucket` is populated before any Approvals assertion runs. Without it an
// absent-badge assertion would pass on a still-pending fetch.
async function renderSidebar(data: Rollup, ctx: PlatformCtx = sidebarCtx()) {
  mockRollupFetch(data)
  const view = render(<Sidebar ctx={ctx} />)
  await within(navButton('Invoices')).findByText(String(data.totals.needs_attention))
  return view
}

describe('Sidebar nav badges', () => {
  it('the Approvals badge reads awaiting_approval, not counts.validated', async () => {
    await renderSidebar(rollup({ validated: 4, awaitingApproval: 2, needsAttention: 3 }))

    expect(badgeOf('Approvals')?.textContent).toBe('2')
  })

  it('awaiting_approval of 0 renders no Approvals badge even while validated is non-zero', async () => {
    await renderSidebar(rollup({ validated: 4, awaitingApproval: 0, needsAttention: 3 }))

    expect(badgeOf('Approvals')).toBeNull()
    expect(within(navButton('Approvals')).queryByText('0')).toBeNull()
  })

  it('the Invoices badge still reads needs_attention', async () => {
    await renderSidebar(rollup({ validated: 4, awaitingApproval: 2, needsAttention: 3 }))

    // The PAIRING is the point: asserted together, neither badge can be wired to the
    // other's field.
    expect(badgeOf('Invoices')?.textContent).toBe('3')
    expect(badgeOf('Approvals')?.textContent).toBe('2')
  })

  // Four digits, not two: the badge is `String(n)` with no cap, and a later `99+` truncation
  // would be a silent product change rather than a compile error.
  it('a four-digit awaiting_approval renders in full', async () => {
    await renderSidebar(rollup({ validated: 4, awaitingApproval: 1247, needsAttention: 3 }))

    expect(badgeOf('Approvals')?.textContent).toBe('1247')
  })

  // The onboarding leg is flipped by RERENDER, on a component whose rollup has ALREADY
  // settled -- so the badges' disappearance cannot be a still-pending fetch.
  it('onboarding suppresses both badges even once the rollup has landed', async () => {
    const data = rollup({ validated: 4, awaitingApproval: 2, needsAttention: 3 })
    const { rerender } = await renderSidebar(data)
    expect(badgeOf('Invoices')?.textContent).toBe('3')
    expect(badgeOf('Approvals')?.textContent).toBe('2')

    rerender(<Sidebar ctx={sidebarCtx({ active: { short: 'Acme', initials: 'AC', tin: '12345678-0001', entityId: null, onboarding: true } })} />)

    expect(badgeOf('Invoices')).toBeNull()
    expect(badgeOf('Approvals')).toBeNull()
  })

  // in-house reads rollup.totals whatever the `clients` array holds -- the entity row below
  // exists only so a badge reading the wrong SCOPE renders a different number.
  it('in-house reads the totals bucket, not a per-entity row', async () => {
    await renderSidebar(
      rollup({ validated: 4, awaitingApproval: 2, needsAttention: 3, entity: { validated: 11, awaitingApproval: 8, needsAttention: 6 } }),
    )

    expect(badgeOf('Approvals')?.textContent).toBe('2')
    expect(badgeOf('Invoices')?.textContent).toBe('3')
  })
})

describe('Sidebar nav badges, firm mode', () => {
  // Firm mode's badge follows the SELECTED entity's row, so totals and entity are given
  // six mutually distinct numbers and the assertion names the entity's.
  const FIRM_ROLLUP = rollup({ validated: 7, awaitingApproval: 5, needsAttention: 9, entity: { validated: 11, awaitingApproval: 6, needsAttention: 3 } })

  it('the Invoices badge is absent until the rollup lands, then follows the selected entity', async () => {
    const release = deferredRollupFetch()
    render(<Sidebar ctx={firmCtx()} />)

    // In flight: no badge, and the switcher row shows entityHealth's neutral placeholder.
    expect(badgeOf('Invoices')).toBeNull()
    expect(switcherSubLabel()).toBe('—')

    release(FIRM_ROLLUP)
    // 3 is the entity row's needs_attention; totals' is 9. Waiting on 3 is what proves the
    // badge did not widen to the whole firm.
    await within(navButton('Invoices')).findByText('3')

    // Undisturbed by this story: the switcher sub-label still reads needs_attention through
    // entityHealth, not the awaiting_approval (6) or validated (11) beside it.
    expect(switcherSubLabel()).toBe('3 needing attention')
  })

  // APPR-12-05 (task-530): firm gains Approvals in the CLIENT group. This asserts BOTH
  // that the item now renders AND that its badge follows the SELECTED entity (6), not the
  // firm-wide totals (5) -- the same scoping FIRM_ROLLUP already proves for Invoices above.
  it('the Approvals badge follows the selected entity, not the firm total', async () => {
    mockRollupFetch(FIRM_ROLLUP)
    render(<Sidebar ctx={firmCtx()} />)
    await within(navButton('Invoices')).findByText('3')

    expect(badgeOf('Approvals')?.textContent).toBe('6')
  })

  // QA adversarial: the selected entity's row, not the firm total, decides absence too.
  // Mirrors the in-house 'awaiting_approval of 0 renders no Approvals badge' case above,
  // but for scopedBucket's firm branch -- a badge that fell back to the firm total on a
  // falsy (zero) entity value would render '5' here instead of staying absent.
  it('the Approvals badge is absent when the selected entity has zero, even while the firm total is non-zero', async () => {
    mockRollupFetch(rollup({ validated: 7, awaitingApproval: 5, needsAttention: 9, entity: { validated: 11, awaitingApproval: 0, needsAttention: 3 } }))
    render(<Sidebar ctx={firmCtx()} />)
    await within(navButton('Invoices')).findByText('3')

    expect(badgeOf('Approvals')).toBeNull()
    expect(within(navButton('Approvals')).queryByText('0')).toBeNull()
    expect(within(navButton('Approvals')).queryByText('5')).toBeNull()
  })

  // QA adversarial, the asymmetry with the Reports card: that one was cut off this overlay,
  // this sub-label was deliberately left on it. Same violation count on both renders.
  it('the sub-label follows a widened needs_attention, unlike the Validation summary', async () => {
    const withViolations = (needsAttention: number): Rollup => {
      const r = rollup({ validated: 7, awaitingApproval: 5, needsAttention: 12, entity: { validated: 11, awaitingApproval: 6, needsAttention } })
      r.clients[0].metrics = { blocked_by_rules: { num: 1, den: 11 } }
      return r
    }

    mockRollupFetch(withViolations(3))
    render(<Sidebar ctx={firmCtx()} />)
    await within(navButton('Invoices')).findByText('3')
    expect(switcherSubLabel()).toBe('3 needing attention')

    cleanup()
    vi.unstubAllGlobals()

    mockRollupFetch(withViolations(9))
    render(<Sidebar ctx={firmCtx()} />)
    await within(navButton('Invoices')).findByText('9')
    expect(switcherSubLabel(), 'only the overlay moved between these two renders').toBe('9 needing attention')
  })
})
