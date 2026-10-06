// @vitest-environment jsdom
// First jsdom coverage of either nav badge. Mirrors DashboardActive.test.tsx's fetch-mock
// + ctx-cast idiom (single-endpoint mock: Sidebar fires only getRollup).
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
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

// Badge-spec fixtures keep the three numbers MUTUALLY DISTINCT, and an `entity`'s three
// distinct from the totals': a badge reading the wrong field or SCOPE then renders a
// different string. The footer pin and FIRM_EVEN_ROLLUP assert no badge value, so they don't.
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

// Tripwire: any padding, gap, avatar or dot change in the footer fails it. Circles are 50%; Sign out carries no pf-btn.
describe('Sidebar footer, characterization pin', () => {
  it('the footer renders the v2 markup', async () => {
    await renderSidebar(
      rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 }),
      sidebarCtx({ user: { name: 'Chinedu Okafor', initials: 'CO', verified: true, tenantName: 'Okafor & Partners' } }),
    )

    const footer = document.querySelector('aside.pf-sidebar > div:last-of-type')!
    expect(footer.outerHTML).toBe(
      '<div data-testid="identity-card" style="flex: 0 0 auto; padding: 12px; border-top: 1px solid var(--line-1); display: flex; align-items: center; gap: 10px;"><span style="flex: 0 0 auto; width: 30px; height: 30px; border-radius: 50%; background: var(--slate-800); color: var(--text-on-dark); display: grid; place-items: center; font-size: 11px; font-weight: 600;" data-testid="persona-initials">CO</span><div style="flex: 1 1 0%; min-width: 0;"><div style="font-size: 13px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" data-testid="persona-name">Chinedu Okafor</div><div class="mono" style="display: flex; align-items: center; gap: 5px; font-size: 10px; color: var(--fg-3); white-space: nowrap; overflow: hidden;"><span style="flex: 0 0 auto; width: 5px; height: 5px; border-radius: 50%; background: var(--status-green-text);" title="Tenant verified via /v1/me"></span><span style="overflow: hidden; text-overflow: ellipsis;">OKAFOR &amp; PARTNERS</span></div></div><button class="pf-signout" aria-label="Sign out" title="Sign out" style="flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0px; border: 0px; border-radius: var(--radius-sm); cursor: pointer;"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"></path><path d="M16 17l5-5-5-5"></path><path d="M21 12H9"></path></svg></button></div>',
    )
  })
})

describe('Sidebar footer, the card reads ctx.user (AUTH-09-02)', () => {
  const BADGE = '[title="Tenant verified via /v1/me"]'
  const ROLLUP = rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 })
  const footer = () => document.querySelector('aside.pf-sidebar > div:last-of-type')!

  it('shows the name and initials ctx.user carries', async () => {
    await renderSidebar(ROLLUP, sidebarCtx({ user: { name: 'Adaeze Nwankwo', initials: 'AN', verified: true, tenantName: 'Acme' } }))
    const text = footer().textContent ?? ''
    expect(text).toContain('Adaeze Nwankwo')
    expect(footer().querySelector('span')!.textContent).toBe('AN')
    expect(footer().querySelectorAll(BADGE)).toHaveLength(1)
  })

  it('renders an empty identity and keeps the badge rule', async () => {
    await renderSidebar(ROLLUP, sidebarCtx({ user: { name: '', initials: '', verified: true, tenantName: 'Acme' } }))
    expect(footer().querySelector('span')!.textContent).toBe('')
    expect(footer().querySelectorAll(BADGE)).toHaveLength(1)
    cleanup()
    await renderSidebar(ROLLUP, sidebarCtx({ user: { name: '', initials: '', verified: false, tenantName: 'Acme' } }))
    expect(footer().querySelectorAll(BADGE)).toHaveLength(0)
  })
})

describe('Sidebar brand mark', () => {
  it('the wordmark link carries the 20px mark', async () => {
    await renderSidebar(rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 }))
    const mark = document.querySelector('aside.pf-sidebar a[href="#"] img')
    expect(mark?.getAttribute('width')).toBe('20')
    expect(mark?.getAttribute('height')).toBe('20')
  })
})

describe('Sidebar identity card (AUTH-15-11)', () => {
  const BADGE = '[title="Tenant verified via /v1/me"]'
  const ROLLUP = rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 })

  it('the identity card renders the session\'s person', async () => {
    await renderSidebar(
      ROLLUP,
      sidebarCtx({ user: { name: 'Ada Nwosu', initials: 'AN', verified: true, tenantName: 'Acme Holdings' } }),
    )

    const card = screen.getByTestId('identity-card')
    expect(within(card).getByTestId('persona-name').textContent).toBe('Ada Nwosu')
    expect(within(card).getByTestId('persona-initials').textContent).toBe('AN')
    expect(card.querySelectorAll(BADGE)).toHaveLength(1)
    expect(card.textContent).toContain('ACME HOLDINGS')
  })

  // The unverified label is mode-derived: in-house `<SHORT> · FINANCE`, firm `OKAFOR & PARTNERS`.
  it('the unverified card falls back to the org label', async () => {
    const unverified = { name: 'Ada Nwosu', initials: 'AN', verified: false, tenantName: 'Acme Holdings' }
    await renderSidebar(ROLLUP, sidebarCtx({ user: unverified }))
    let card = screen.getByTestId('identity-card')
    expect(card.querySelectorAll(BADGE)).toHaveLength(0)
    expect(card.textContent).toContain('ACME · FINANCE')
    expect(card.textContent).not.toContain('ACME HOLDINGS')

    cleanup()
    // Firm badges read the selected entity's row, so the settle signal needs one.
    mockRollupFetch(rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1, entity: { validated: 1, awaitingApproval: 1, needsAttention: 1 } }))
    render(<Sidebar ctx={firmCtx({ user: unverified })} />)
    await within(navButton('Invoices')).findByText('1')
    card = screen.getByTestId('identity-card')
    expect(card.querySelectorAll(BADGE)).toHaveLength(0)
    expect(card.textContent).toContain('OKAFOR & PARTNERS')
  })

  it('the card holds the Sign out control', async () => {
    const ctx = sidebarCtx()
    await renderSidebar(ROLLUP, ctx)

    const signOut = within(screen.getByTestId('identity-card')).getByRole('button', { name: 'Sign out' })
    fireEvent.click(signOut)
    expect(ctx.signOut).toHaveBeenCalledTimes(1)
  })

  it('the card switches nobody', async () => {
    mockRollupFetch(ROLLUP)
    const ctx = sidebarCtx({
      members: [
        { id: 'm-seat', name: 'Ada Nwosu', initials: 'AN', email: null, role: 'admin', status: 'active', isYou: true },
        { id: 'm-other', name: 'Tunde Bello', initials: 'TB', email: null, role: 'preparer', status: 'active', isYou: false },
      ],
    })
    const { container } = render(<Sidebar ctx={ctx} />)
    await within(navButton('Invoices')).findByText('1')

    const noSwitcher = () => {
      expect(screen.queryByTestId('persona-trigger')).toBeNull()
      expect(screen.queryByTestId('persona-popover')).toBeNull()
      expect(screen.queryAllByTestId(/^persona-row/)).toHaveLength(0)
      expect(screen.queryByTestId('persona-toast')).toBeNull()
    }
    noSwitcher()

    const card = screen.getByTestId('identity-card')
    const before = container.innerHTML
    fireEvent.click(card)
    noSwitcher()
    expect(container.innerHTML, 'clicking the card must change nothing').toBe(before)
  })
})

// BUG-17-01: the v2 layer's `.pf-btn` rule forces --radius-btn with !important, which beats an
// inline radius, so the firm switcher carries no button class.
describe('BUG-17-01 company switcher corner', () => {
  const TOKENS_CSS = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../../../packages/design-tokens/v2/app-layer.css'), 'utf8')

  // Raw style attribute, not .style.*: jsdom's CSSStyleDeclaration can drop var() shorthands.
  function borderRadiusOf(el: HTMLElement): string | null {
    return el.getAttribute('style')?.match(/border-radius:\s*([^;]+)/)?.[1].trim() ?? null
  }

  // entity.needs_attention == totals.needs_attention so renderSidebar's findByText settles
  // regardless of which bucket the firm branch resolves to.
  const FIRM_EVEN_ROLLUP = rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1, entity: { validated: 1, awaitingApproval: 1, needsAttention: 1 } })

  it('switcher_carriesNoPillButtonClass', async () => {
    await renderSidebar(FIRM_EVEN_ROLLUP, firmCtx())

    const switcher = screen.getByTestId('company-switcher')
    expect(switcher.className.split(/\s+/).filter(Boolean)).not.toContain('pf-btn')
  })

  it('companyChip_declaresTheSwitchersCorner', async () => {
    await renderSidebar(rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 }))
    const chip = screen.queryByTestId('company-chip')
    expect(chip, 'company-chip testid').not.toBeNull()
    const chipRadius = borderRadiusOf(chip!)

    cleanup()
    vi.unstubAllGlobals()

    await renderSidebar(FIRM_EVEN_ROLLUP, firmCtx())
    const switcherRadius = borderRadiusOf(screen.getByTestId('company-switcher'))

    expect(switcherRadius).toBe(chipRadius)
    expect(switcherRadius).toBe('var(--radius-input)')
    expect(TOKENS_CSS.replace(/\/\*[\s\S]*?\*\//g, '')).toMatch(/--radius-input:\s*var\(--radius-btn\);/)
  })

  it('switcher_declaresBorderAndBackgroundTransitions', async () => {
    await renderSidebar(FIRM_EVEN_ROLLUP, firmCtx())

    // The switcher's OWN style, not a descendant: the chevron span carries its own
    // `transition: transform 160ms`.
    const style = screen.getByTestId('company-switcher').getAttribute('style') ?? ''
    expect(style).toContain('background var(--dur-fast) var(--ease-out)')
    expect(style).toContain('border-color var(--dur-fast) var(--ease-out)')
  })

  it('switcher_openStateBorderIsAction', async () => {
    await renderSidebar(FIRM_EVEN_ROLLUP, firmCtx())
    expect(screen.getByTestId('company-switcher').getAttribute('style')).toContain('border: 1px solid var(--action)')

    cleanup()
    vi.unstubAllGlobals()

    const closedCtx = firmCtx({ switcherOpen: false })
    await renderSidebar(FIRM_EVEN_ROLLUP, closedCtx)
    const switcher = screen.getByTestId('company-switcher')
    expect(switcher.getAttribute('style')).toContain('border: 1px solid var(--line-2)')

    switcher.click()
    expect(closedCtx.toggleSwitcher).toHaveBeenCalledTimes(1)
  })

  it('pfBtnRule_forcesTheButtonCorner', () => {
    const noComments = TOKENS_CSS.replace(/\/\*[\s\S]*?\*\//g, '')
    const blocks = noComments.match(/[^{}]+\{[^{}]*\}/g) ?? []
    // Exact selector-token match, not substring: the `.pf-btn:active` block also contains
    // ".asc-app .pf-btn" but carries no radius.
    const buttonRule = blocks.find((b) =>
      b
        .slice(0, b.indexOf('{'))
        .split(',')
        .map((s) => s.trim())
        .includes('.asc-app .pf-btn'),
    )
    expect(buttonRule, '.asc-app .pf-btn rule block (not its :active sibling)').toBeDefined()
    expect(buttonRule).toContain('border-radius: var(--radius-btn) !important')
  })

  // Bans every class, not only pf-btn: v2-btn, ops-btn, dev-btn and pf-chip force the same !important radius.
  it('switcher_carriesNoClassAtAll', async () => {
    await renderSidebar(FIRM_EVEN_ROLLUP, firmCtx())
    expect(screen.getByTestId('company-switcher').getAttribute('class') ?? '').toBe('')
  })

  it('companyChipAndSwitcher_areModeExclusive', async () => {
    await renderSidebar(rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 }))
    expect(screen.queryByTestId('company-chip')).not.toBeNull()
    expect(screen.queryByTestId('company-switcher')).toBeNull()

    cleanup()
    vi.unstubAllGlobals()

    await renderSidebar(FIRM_EVEN_ROLLUP, firmCtx())
    expect(screen.queryByTestId('company-switcher')).not.toBeNull()
    expect(screen.queryByTestId('company-chip')).toBeNull()
  })

  it('nav_offersClientsToFirmOnly', async () => {
    await renderSidebar(rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 }))
    expect(screen.queryByText('Invoices'), 'floor: the in-house nav rendered').not.toBeNull()
    expect(screen.queryByText('Clients'), 'an in-house nav must not offer Clients').toBeNull()

    cleanup()
    vi.unstubAllGlobals()

    await renderSidebar(FIRM_EVEN_ROLLUP, firmCtx())
    expect(screen.queryByText('Clients'), 'control: the firm nav offers Clients').not.toBeNull()
  })

  it('switcher_borderAndClientListFollowSwitcherOpen', async () => {
    const ctx = firmCtx({ switcherOpen: false })
    const view = await renderSidebar(FIRM_EVEN_ROLLUP, ctx)
    const switcherStyle = () => screen.getByTestId('company-switcher').getAttribute('style') ?? ''
    expect(switcherStyle()).toContain('border: 1px solid var(--line-2)')
    expect(screen.queryAllByTestId('company-switcher-option')).toHaveLength(0)

    view.rerender(<Sidebar ctx={{ ...ctx, switcherOpen: true }} />)
    expect(switcherStyle()).toContain('border: 1px solid var(--action)')
    expect(screen.queryAllByTestId('company-switcher-option')).toHaveLength(1)

    view.rerender(<Sidebar ctx={ctx} />)
    expect(switcherStyle()).toContain('border: 1px solid var(--line-2)')
    expect(screen.queryAllByTestId('company-switcher-option')).toHaveLength(0)
  })
})

describe('Sidebar in-house chip ERP pill (AUTH-10-07, Core AC-7)', () => {
  const ROLLUP = rollup({ validated: 1, awaitingApproval: 1, needsAttention: 1 })

  it('hand-off: the in-house chip shows no ERP pill', async () => {
    await renderSidebar(ROLLUP, sidebarCtx({ handoff: true }))
    const chip = screen.getByTestId('company-chip')
    expect(chip.textContent, 'the chip still names the workspace').toContain('WORKSPACE')
    expect(within(chip).queryByText('ERP')).toBeNull()
  })

  // Control: green before and after.
  it('persona: the in-house chip keeps its ERP pill', async () => {
    await renderSidebar(ROLLUP, sidebarCtx({ handoff: false }))
    const chip = screen.getByTestId('company-chip')
    expect(chip.textContent).toContain('WORKSPACE')
    expect(within(chip).getByText('ERP')).toBeDefined()
  })
})

// Raw style attribute, not .style.*: jsdom's CSSStyleDeclaration can drop var() shorthands.
function styleDecl(el: Element, prop: string): string | null {
  return el.getAttribute('style')?.match(new RegExp(`(?:^|;\\s*)${prop}:\\s*([^;]+)`))?.[1].trim() ?? null
}

const asideEl = () => document.querySelector<HTMLElement>('aside.pf-sidebar')!
const footerEl = () => document.querySelector<HTMLElement>('aside.pf-sidebar > div:last-of-type')!

describe('Sidebar v2 dark scope', () => {
  const ONES = { validated: 1, awaitingApproval: 1, needsAttention: 1 }
  const V2_ROLLUP = rollup({ ...ONES, entity: ONES })
  const VERIFIED_USER = { name: 'Chinedu Okafor', initials: 'CO', verified: true, tenantName: 'Okafor & Partners' }
  const bothModes = (): [string, PlatformCtx][] => [
    ['firm', firmCtx({ switcherOpen: true, user: VERIFIED_USER })],
    ['in-house', sidebarCtx({ user: VERIFIED_USER })],
  ]

  async function renderFresh(ctx: PlatformCtx) {
    cleanup()
    vi.unstubAllGlobals()
    return renderSidebar(V2_ROLLUP, ctx)
  }

  it('SB-01 the aside is the dark scope on --surface', async () => {
    for (const [mode, ctx] of bothModes()) {
      await renderFresh(ctx)

      const aside = asideEl()
      expect(aside.classList.contains('pf-sidebar'), `${mode} pf-sidebar`).toBe(true)
      expect(aside.classList.contains('asc-dark'), `${mode} asc-dark`).toBe(true)
      expect(styleDecl(aside, 'background'), `${mode} background`).toBe('var(--surface)')
      expect(styleDecl(aside, 'border-right'), `${mode} edge`).toBe('1px solid var(--surface-panel-border)')
    }
  })

  it('SB-02 the active row keeps its action bar and icon (pin, green at write)', async () => {
    for (const [mode, ctx] of [
      ['firm', firmCtx({ view: 'invoices' })],
      ['in-house', sidebarCtx({ view: 'invoices' })],
    ] as [string, PlatformCtx][]) {
      await renderFresh(ctx)

      const active = navButton('Invoices')
      expect(styleDecl(active, 'background'), `${mode} active bg`).toBe('var(--bg-3)')
      const [bar, icon] = [active.children[0], active.children[1]]
      expect(styleDecl(bar, 'background'), `${mode} active bar`).toBe('var(--action)')
      expect(styleDecl(icon, 'color'), `${mode} active icon`).toBe('var(--action)')

      const rows = [...asideEl().querySelectorAll<HTMLElement>('nav button.pf-nav')]
      expect(rows.length, `${mode} nav rows`).toBeGreaterThanOrEqual(8)
      const inactive = rows.filter((r) => r !== active)
      expect(inactive.length, `${mode} inactive rows`).toBe(rows.length - 1)
      for (const row of inactive) {
        expect(styleDecl(row, 'background'), `${mode} ${row.textContent} bg`).toBe('transparent')
        expect(styleDecl(row.children[0], 'background'), `${mode} ${row.textContent} bar`).toBe('transparent')
        expect(styleDecl(row.children[1], 'color'), `${mode} ${row.textContent} icon`).toBe('var(--fg-3)')
      }
    }
  })

  it('SB-03 the dropdown is light inside the dark aside', async () => {
    await renderSidebar(V2_ROLLUP, firmCtx({ switcherOpen: true }))

    expect(screen.getAllByTestId('company-switcher-option').length).toBeGreaterThan(0)
    const dropdown = screen.getByText('Switch company').parentElement!
    expect(dropdown.classList.contains('asc-light')).toBe(true)
    expect(styleDecl(dropdown, 'box-shadow')).toBe('var(--shadow-card)')
    expect(asideEl().querySelectorAll('.asc-light').length, 'only the dropdown is light').toBe(1)
  })

  it('SB-04 the switcher states its own text colour', async () => {
    await renderSidebar(V2_ROLLUP, firmCtx())

    expect(styleDecl(screen.getByTestId('company-switcher'), 'color')).toBe('var(--fg-1)')
  })

  it('SB-05 badges are 4px, circles are 50%', async () => {
    for (const [mode, ctx] of bothModes()) {
      await renderFresh(ctx)

      for (const label of ['Invoices', 'Approvals']) {
        const badge = badgeOf(label)
        expect(badge, `${mode} ${label} badge rendered`).not.toBeNull()
        expect(styleDecl(badge!, 'border-radius'), `${mode} ${label} badge`).toBe('var(--radius-sm)')
      }
      const avatar = footerEl().querySelector('span')!
      expect(avatar.textContent, `${mode} avatar`).toBe('CO')
      expect(styleDecl(avatar, 'border-radius'), `${mode} avatar`).toBe('50%')
      const verifiedDot = footerEl().querySelector('[title="Tenant verified via /v1/me"]')!
      expect(verifiedDot, `${mode} verified dot rendered`).not.toBeNull()
      expect(styleDecl(verifiedDot, 'border-radius'), `${mode} verified dot`).toBe('50%')
    }

    const erp = within(screen.getByTestId('company-chip')).getByText('ERP').parentElement!
    expect(styleDecl(erp, 'border-radius')).toBe('var(--radius-sm)')
    const erpDot = erp.firstElementChild!
    expect(erpDot.tagName).toBe('SPAN')
    expect(styleDecl(erpDot, 'border-radius')).toBe('50%')
  })

  it('SB-06 labels pass contrast; no --fg-4 text', async () => {
    const modes: [string, PlatformCtx, number, number][] = [
      ['firm', firmCtx({ switcherOpen: true }), 10, 2],
      ['in-house', sidebarCtx(), 8, 1],
    ]
    for (const [mode, ctx, navCount, groupCount] of modes) {
      await renderFresh(ctx)

      expect(asideEl().querySelectorAll('nav button.pf-nav').length, `${mode} nav buttons`).toBeGreaterThanOrEqual(navCount)
      const labels = [...asideEl().querySelectorAll('nav .label')]
      expect(labels.length, `${mode} group labels`).toBe(groupCount)
      for (const label of labels) expect(styleDecl(label, 'color'), `${mode} group label`).toBe('var(--eyebrow-on-dark)')
      const scopes = [...asideEl().querySelectorAll('nav .label > span.mono')]
      expect(scopes.length, `${mode} scope spans`).toBe(groupCount)
      for (const scope of scopes) expect(styleDecl(scope, 'color'), `${mode} scope text`).toBe('var(--fg-3)')
      const html = asideEl().outerHTML
      expect(html, `${mode} control: the scan reads token refs`).toContain('var(--fg-3)')
      expect(html, `${mode} outerHTML`).not.toMatch(/--fg-4/i)
    }
  })

  it('SB-07 signOut_carriesNoPfBtn', async () => {
    await renderSidebar(V2_ROLLUP, sidebarCtx())

    const btn = screen.getByRole('button', { name: 'Sign out' })
    expect(btn.getAttribute('aria-label')).toBe('Sign out')
    expect([...btn.classList]).toEqual(['pf-signout'])
    expect(styleDecl(btn, 'border-radius')).toBe('var(--radius-sm)')
  })

  it('SB-08 no oklch or pill corners in the rendered sidebar (boundary)', async () => {
    for (const [mode, ctx] of bothModes()) {
      await renderFresh(ctx)
      const html = asideEl().outerHTML

      if (mode === 'firm') expect(html, 'firm dropdown rendered').toContain('data-testid="company-switcher-option"')
      expect(html, `${mode} badges rendered`).toContain('border-radius: var(--radius-sm)')
      expect(html, `${mode} circles`).toContain('border-radius: 50%')
      expect(html, `${mode} oklch`).not.toMatch(/oklch/i)
      expect(html, `${mode} 99px or 999px corner`).not.toMatch(/border-radius:\s*(?:9{2,}|\d{3,})px/i)
    }
  })

  it('SB-09 the roster, groups and badge placement are unchanged', async () => {
    const roster = () =>
      [...asideEl().querySelectorAll('nav > *')].map((el) => {
        if (el.matches('.label')) return `group ${el.children[0].textContent} ${el.children[1].textContent}`
        const badge = el.querySelector('span.mono')?.textContent
        return `${el.children[2].textContent}${badge ? ` [${badge}]` : ''}`
      })

    await renderFresh(firmCtx({ switcherOpen: false }))
    expect(roster()).toEqual([
      'group Acme · CLIENT',
      'Overview',
      'Invoices [1]',
      'Approvals [1]',
      'Rules',
      'Customers',
      'Reports',
      'group OKAFOR & PARTNERS · FIRM-WIDE',
      'Workflows',
      'Clients',
      'Audit',
      'Settings',
    ])

    await renderFresh(sidebarCtx())
    expect(roster()).toEqual([
      'group Workspace · Acme',
      'Overview',
      'Invoices [1]',
      'Workflows',
      'Rules',
      'Approvals [1]',
      'Reports',
      'Audit',
      'Settings',
    ])
  })

  it('SB-10 the Sign out hover fill is not shadowed by an inline background', async () => {
    await renderSidebar(V2_ROLLUP, sidebarCtx())

    // An inline `background` beats `.pf-signout:hover`'s fill, so the button declares none.
    expect(styleDecl(screen.getByRole('button', { name: 'Sign out' }), 'background')).toBeNull()
  })
})
