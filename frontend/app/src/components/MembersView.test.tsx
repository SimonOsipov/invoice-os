// @vitest-environment jsdom
//
// RED specs for AC2 (the live status write) and AC4 (the server's reason at the control) —
// the only two ACs of APPR-15-06 that are test-first. MembersTable/MemberDrawer still call
// ctx.saveMember today; neither the live wire nor the failure-reason surface exists yet, so
// every spec below fails on its OWN target assertion (a call that never happens, or an
// element that never renders) — never on import/typecheck.
//
// Harness: MembersView takes the whole roster and its write verbs off `ctx`, so a click is
// only observable through whatever funnel owns them. This file pre-builds the funnel the
// story's plan settles on — patch-in-place off the SERVER's own row via
// replaceMember(toMember(wire, subject)), no optimistic write, rejection uncaught — as
// `setMemberStatus`, and hands it to ctx alongside the two verbs the UNWIRED component still
// calls (`saveMember`/`dropMember`, both no-ops here). A click today still runs `saveMember`
// and never reaches `setMemberStatus`/`setMembershipStatus`, which is the RED. Once 06's feat
// commit re-points MembersTable.tsx/MemberDrawer.tsx's Suspend/Reactivate handlers at
// `ctx.setMemberStatus`, the same click starts driving this harness with no edit here.
import { createElement, useState } from 'react'

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncStatus } from '@invoice-os/api-client'
import {
  PROTECTED_ADMIN_NOTE,
  replaceMember,
  setMembershipStatus,
  toMember,
  type InvitationWire,
  type Member,
  type MembershipWire,
} from '../lib/members'
import type { AuthedFetch } from '../lib/portfolio'
import type { Role } from '../lib/roles'
import type { PlatformCtx } from '../types'
import { MembersView } from './MembersView'

vi.mock('../lib/members', async (orig) => ({
  ...(await orig<typeof import('../lib/members')>()),
  setMembershipStatus: vi.fn(),
}))

// Records the title of every EmptyState render pass, so a one-frame "Just you" (N3) is visible
// even though React commits the next frame before a test can read the DOM.
const emptyStateTitles = vi.hoisted(() => [] as string[])
vi.mock('@invoice-os/api-client', async (orig) => {
  const real = await orig<typeof import('@invoice-os/api-client')>()
  return {
    ...real,
    EmptyState: (props: Parameters<typeof real.EmptyState>[0]) => {
      emptyStateTitles.push(props.title ?? '')
      return createElement(real.EmptyState, props)
    },
  }
})

const mockedSetMembershipStatus = vi.mocked(setMembershipStatus)

const SUBJECT = 'u1'
const BASE = 'https://gw'
// setMembershipStatus is fully mocked -- this is a type-satisfying placeholder, never
// actually invoked.
const noopFetch: AuthedFetch = () => Promise.reject(new Error('unused — setMembershipStatus is mocked'))
// The real server text (task-436's own read of the wire) — long and specific enough that it
// cannot appear by accident, unlike a one-word fixture.
const REASON = "this is the tenant's last active admin — make another member an active admin first"

function member(over: Partial<Member> = {}): Member {
  return {
    id: SUBJECT,
    name: 'Ada Person',
    initials: 'AP',
    email: 'ada@x.ng',
    role: 'preparer', // never 'admin' -- isProtectedAdmin would disable Suspend outright
    status: 'active',
    isYou: false,
    ...over,
  }
}

function wireFor(m: Member, status: Member['status']): MembershipWire {
  return { user_id: m.id, role: m.role, status, display_name: m.name, email: m.email }
}

// A second, unrelated row -- a roster of exactly one member renders MembersView's "Just you"
// empty state instead of the table (MembersView.tsx:101), which every test below that needs
// the table or the drawer must avoid.
function otherMember(): Member {
  return member({ id: 'other1', name: 'Other Person', initials: 'OP', status: 'invited' })
}

// Reproduces the funnel App.tsx's own feat commit will build (story plan §3): the server's
// row wins via replaceMember(toMember(...)), no optimistic write, and the rejection is never
// caught here so AC4's owner (MembersView) can render it once it exists. Test scaffolding,
// not production code -- App.tsx itself is untouched by this commit.
//
// `membersState`/`membersError` default to a landed roster, matching this harness's
// pre-existing (unset) behaviour -- membersSurface(undefined) also falls through to 'roster'.
type HarnessControls = { setMembers: (list: Member[]) => void }

function Harness({
  initial,
  authedFetch,
  controls,
  membersState,
  membersError,
  refetchMembers,
  roles,
  rolesState,
  rolesError,
  refetchRoles,
}: {
  initial: Member[]
  /** The spy `listInvitations` / `sendInvitations` / `resendInvitation` call through. */
  authedFetch?: AuthedFetch
  /** Filled on render: lets a test replace the roster after it landed. */
  controls?: { current: HarnessControls | null }
  membersState?: AsyncStatus
  membersError?: ApiError | null
  refetchMembers?: () => void
  roles?: Role[]
  rolesState?: AsyncStatus
  rolesError?: ApiError | null
  refetchRoles?: () => void
}) {
  const [members, setMembers] = useState<Member[]>(initial)
  if (controls) controls.current = { setMembers }

  async function setMemberStatus(id: string, status: Exclude<Member['status'], 'invited'>) {
    const wire = await setMembershipStatus(noopFetch, BASE, id, status)
    setMembers((list) => replaceMember(list, toMember(wire, SUBJECT)))
  }

  const ctx = {
    members,
    mode: 'firm',
    policies: [],
    policiesState: 'ready',
    policiesError: null,
    refetchPolicies: vi.fn(),
    publishPolicy: vi.fn(),
    roles: roles ?? [],
    membersState: membersState ?? 'ready',
    membersError: membersError ?? null,
    refetchMembers: refetchMembers ?? vi.fn(),
    rolesState: rolesState ?? 'ready',
    rolesError: rolesError ?? null,
    refetchRoles: refetchRoles ?? vi.fn(),
    authedFetch: authedFetch ?? noopFetch,
    saveMember: vi.fn(), // the verb the UNWIRED table/drawer still call -- deliberately inert
    dropMember: vi.fn(),
    inviteMembers: vi.fn(),
    saveRole: vi.fn(),
    setSettingsTab: vi.fn(),
    setMemberStatus, // unused until MembersTable/MemberDrawer re-point their onClick here
  }

  return <MembersView ctx={ctx as unknown as PlatformCtx} />
}

// Scoped to the table: once the drawer is open, its header repeats the same name, and an
// unscoped getByText would find both.
function rowFor(name: string): HTMLElement {
  const table = screen.getByTestId('members-table')
  return within(table).getByText(name).closest('[data-testid="member-row"]') as HTMLElement
}

function openDrawer(name: string) {
  fireEvent.click(screen.getByText(name))
}

function clickSuspend() {
  fireEvent.click(screen.getByTestId('member-suspend'))
}

// The row's own `⋯` menu, as opposed to the drawer's Suspend button `clickSuspend` drives.
function suspendFromRowMenu(row: HTMLElement) {
  fireEvent.click(within(row).getByTestId('member-menu-trigger'))
  fireEvent.click(within(screen.getByTestId('member-menu')).getByText('Suspend'))
}

afterEach(() => {
  cleanup()
  mockedSetMembershipStatus.mockReset()
})

describe('AC2: a successful suspend leaves no stale row', () => {
  it('the row carries the SERVER-returned status once the write settles, not the pre-click one', async () => {
    const m = member()
    const other = member({ id: 'other1', name: 'Other Person', initials: 'OP', status: 'invited' })
    mockedSetMembershipStatus.mockResolvedValue(wireFor(m, 'suspended'))

    render(<Harness initial={[m, other]} />)
    openDrawer('Ada Person')
    const row = rowFor('Ada Person')
    clickSuspend()

    expect(mockedSetMembershipStatus, 'the write must go through the live wire, not stay on the mock saveMember verb').toHaveBeenCalledWith(
      noopFetch,
      BASE,
      SUBJECT,
      'suspended',
    )

    await within(row).findByText('SUSPENDED', { exact: true })
    expect(within(row).queryByText('ACTIVE', { exact: true }), 'the pre-click status must not linger once the server row lands').toBeNull()
  })
})

describe('AC2: a failed suspend does not leave an optimistic status on screen', () => {
  it('the row stays on its ORIGINAL status when the write rejects', async () => {
    const m = member()
    const other = member({ id: 'other1', name: 'Other Person', initials: 'OP', status: 'invited' })
    mockedSetMembershipStatus.mockRejectedValue(new ApiError('http', REASON, 409))

    render(<Harness initial={[m, other]} />)
    openDrawer('Ada Person')
    const row = rowFor('Ada Person')
    clickSuspend()

    expect(mockedSetMembershipStatus, 'the write must go through the live wire even when it is about to fail').toHaveBeenCalledWith(
      noopFetch,
      BASE,
      SUBJECT,
      'suspended',
    )

    await within(row).findByText('ACTIVE', { exact: true })
    expect(within(row).queryByText('SUSPENDED', { exact: true }), 'a rejected write must never render as if it had succeeded').toBeNull()
  })
})

describe("AC4: a rejected write renders the server's own reason at the control", () => {
  it('the exact 409 message becomes visible, verbatim', async () => {
    const m = member()
    const other = member({ id: 'other1', name: 'Other Person', initials: 'OP', status: 'invited' })
    mockedSetMembershipStatus.mockRejectedValue(new ApiError('http', REASON, 409))

    render(<Harness initial={[m, other]} />)
    openDrawer('Ada Person')
    clickSuspend()

    // findAllByText, not findByText: the story's own plan puts the reason on BOTH the table
    // strip and the drawer sibling at once (gated by member id, not by surface), so exactly
    // one match is not a safe assumption -- at least one is.
    const matches = await screen.findAllByText(REASON)
    expect(matches.length, 'the server 409 message must reach the screen verbatim').toBeGreaterThan(0)
  })

  it('the reason is not replaced by client copy -- not the pre-disable lock note, not a generic fallback', async () => {
    const m = member()
    const other = member({ id: 'other1', name: 'Other Person', initials: 'OP', status: 'invited' })
    mockedSetMembershipStatus.mockRejectedValue(new ApiError('http', REASON, 409))

    render(<Harness initial={[m, other]} />)
    openDrawer('Ada Person')
    clickSuspend()

    await screen.findAllByText(REASON) // presence, proven above -- the gate for this spec
    expect(screen.queryByText(PROTECTED_ADMIN_NOTE), "the pre-disable lock note must not stand in for the write's own failure").toBeNull()
    expect(screen.queryByText(/something went wrong/i), 'must never fall back to generic client copy ([the-clientsview-167-trap])').toBeNull()
  })
})

// ============================================================================
// QA (Stage 4) -- adversarial coverage over the live surface ladder and the write race
// ============================================================================

describe('AC1: an errored roster never renders as an empty success', () => {
  it('the error surface renders, not the "just you" empty state, over a roster that never loaded', () => {
    render(<Harness initial={[]} membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} />)

    expect(screen.getByText('gateway is down')).toBeTruthy()
    expect(screen.queryByText('Just you at the firm')).toBeNull()
    expect(screen.queryByTestId('members-table')).toBeNull()
  })

  it('the unassigned-roles amber notice never renders over an errored fetch, even with a genuinely unheld role', () => {
    // An unheld role makes `unassigned.length > 0` true regardless of the fetch outcome --
    // if the notice were gated on that alone (not on the surface too) it would fire here and
    // assert a coverage failure that is really a fetch failure.
    const unheldRole: Role = { key: 'r1', title: 'Finance Approver', desc: '', members: [] }
    render(<Harness initial={[]} roles={[unheldRole]} membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} />)

    expect(screen.queryByTestId('members-unassigned')).toBeNull()
    expect(screen.queryByText('Finance Approver')).toBeNull()
  })

  it('retry calls the ctx-level refetch, not a local re-render', () => {
    const refetch = vi.fn()
    render(<Harness initial={[]} membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} refetchMembers={refetch} />)

    fireEvent.click(screen.getByText('Retry'))
    expect(refetch).toHaveBeenCalledOnce()
  })
})

// ============================================================================
// APPR-04-06 AC1 -- the roster branches on the ROLES fetch too, not just members
// ============================================================================

describe('APPR-04-06 AC1: a roles-only fetch failure must not render the roster as if it loaded', () => {
  function threeMembers(): Member[] {
    return [member(), otherMember(), member({ id: 'u3', name: 'Third Person', initials: 'TP' })]
  }

  it('renders the error surface, not the table, when only ctx.rolesState is error', () => {
    render(<Harness initial={threeMembers()} rolesState="error" rolesError={new ApiError('http', 'roles gateway is down', 503)} />)

    expect(screen.queryByTestId('members-table'), 'a roles-only failure must not render the roster as if it loaded').toBeNull()
    expect(screen.getByText('roles gateway is down')).toBeTruthy()
  })

  it('still renders the roster when the roles list is merely EMPTY, not errored', () => {
    render(<Harness initial={threeMembers()} rolesState="ready" roles={[]} />)

    expect(screen.getByTestId('members-table')).toBeTruthy()
    expect(screen.queryByTestId('members-unassigned')).toBeNull()
  })

  // A genuinely landed-empty roles fetch reports rolesState:'empty', NOT 'ready' with roles:[]
  // (resolveStatus's default isEmpty makes an empty array 'empty', never 'ready' -- see
  // async-state.ts). The spec above never exercises this real combination, so it cannot
  // catch a mutation that removes MembersView's rolesState==='empty' remap: verified this
  // exact mutation passes all 16 pre-existing specs in this file untouched.
  it('a REAL landed-empty roles fetch (rolesState "empty", not "ready") still renders the roster for real members', () => {
    render(<Harness initial={threeMembers()} rolesState="empty" roles={[]} />)

    expect(screen.getByTestId('members-table'), 'a tenant with 3 real members and zero configured roles is not a tenant with zero members').toBeTruthy()
    expect(screen.queryByText('Just you at the firm'), 'zero roles must never collapse into "just you"').toBeNull()
  })
})

describe('APPR-04-06 QA: both the roles fetch and the members fetch fail at once', () => {
  it('the roles error wins the display -- ctx.rolesError ?? ctx.membersError -- and Retry fires both refetches', () => {
    const refetchRoles = vi.fn()
    const refetchMembers = vi.fn()
    render(
      <Harness
        initial={[]}
        rolesState="error"
        rolesError={new ApiError('http', 'roles gateway is down', 503)}
        refetchRoles={refetchRoles}
        membersState="error"
        membersError={new ApiError('http', 'members gateway is down', 503)}
        refetchMembers={refetchMembers}
      />,
    )

    expect(screen.getByText('roles gateway is down')).toBeTruthy()
    expect(screen.queryByText('members gateway is down'), 'only one error surface can show at a time').toBeNull()

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchRoles, 'both fetches actually failed, so both must be retried').toHaveBeenCalledOnce()
    expect(refetchMembers).toHaveBeenCalledOnce()
  })
})

describe('a second suspend fired while the first write is still in flight', () => {
  it('two clicks before either resolves both target the same status, and the row lands correctly suspended', async () => {
    const m = member()
    let resolveFirst!: (wire: MembershipWire) => void
    let resolveSecond!: (wire: MembershipWire) => void
    mockedSetMembershipStatus
      .mockImplementationOnce(() => new Promise((res) => (resolveFirst = res)))
      .mockImplementationOnce(() => new Promise((res) => (resolveSecond = res)))

    render(<Harness initial={[m, otherMember()]} />)
    openDrawer('Ada Person')
    const row = rowFor('Ada Person')
    clickSuspend()
    clickSuspend() // the drawer button's label hasn't changed yet -- no optimistic write -- so this is a SECOND suspend, not a reactivate

    expect(mockedSetMembershipStatus).toHaveBeenCalledTimes(2)

    // The later click's write settles first -- out-of-order resolution must not corrupt the row.
    resolveSecond(wireFor(m, 'suspended'))
    await within(row).findByText('SUSPENDED', { exact: true })
    resolveFirst(wireFor(m, 'suspended'))

    expect(within(row).queryByText('ACTIVE', { exact: true }), 'the stale first response must not revert the row').toBeNull()
  })
})

describe('the drawer and the row menu act on the same directory', () => {
  it('a suspend fired from the row menu is reflected when the drawer is opened afterward', async () => {
    const m = member()
    mockedSetMembershipStatus.mockResolvedValue(wireFor(m, 'suspended'))

    render(<Harness initial={[m, otherMember()]} />)
    const row = rowFor('Ada Person')
    suspendFromRowMenu(row)
    await within(row).findByText('SUSPENDED', { exact: true })

    openDrawer('Ada Person')
    const drawer = screen.getByTestId('member-drawer')
    expect(within(drawer).getByText('Reactivate'), 'the drawer must read the same server-patched status the row menu just wrote').toBeTruthy()
    expect(within(drawer).queryByText('Suspend')).toBeNull()
  })
})

describe('statusError clears on the next write, success or failure', () => {
  it('a failed suspend followed by a successful one leaves no stale error on screen', async () => {
    const m = member()
    mockedSetMembershipStatus.mockRejectedValueOnce(new ApiError('http', REASON, 409)).mockResolvedValueOnce(wireFor(m, 'suspended'))

    render(<Harness initial={[m, otherMember()]} />)
    openDrawer('Ada Person')
    const row = rowFor('Ada Person')
    clickSuspend()
    await screen.findAllByText(REASON)

    clickSuspend() // setStatusError(null) fires synchronously, before this second write even settles
    expect(screen.queryByText(REASON), 'the stale reason must be cleared the instant a new write starts').toBeNull()

    await within(row).findByText('SUSPENDED', { exact: true })
    expect(screen.queryByText(REASON)).toBeNull()
  })
})

describe('a null email renders the shared em-dash label, not blank text or the literal "null"', () => {
  // Scoped to `.mono` -- the row's "Workflow roles" cell ALSO renders '—' (no roles wired
  // into this harness), so an unscoped getByText('—') would match two elements.
  it('in the table row', () => {
    render(<Harness initial={[member({ email: null }), otherMember()]} />)
    const row = rowFor('Ada Person')
    expect(row.querySelector('.mono')?.textContent).toBe('—')
  })

  it('in the drawer header', () => {
    // getByText, not `.mono` -- the status pill (MemberStatusPill) is ALSO `.mono` and
    // precedes the email line in DOM order, so a bare class query hits the wrong element.
    render(<Harness initial={[member({ email: null }), otherMember()]} />)
    openDrawer('Ada Person')
    const drawer = screen.getByTestId('member-drawer')
    expect(within(drawer).getByText('—')).toBeTruthy()
  })
})

describe('isYou can still be suspended when not the protected admin', () => {
  it('a non-admin isYou row suspends through the same control as anyone else', async () => {
    const m = member({ isYou: true })
    mockedSetMembershipStatus.mockResolvedValue(wireFor(m, 'suspended'))

    render(<Harness initial={[m, otherMember()]} />)
    openDrawer('Ada Person')
    const row = rowFor('Ada Person')
    clickSuspend()

    expect(mockedSetMembershipStatus).toHaveBeenCalledWith(noopFetch, BASE, SUBJECT, 'suspended')
    await within(row).findByText('SUSPENDED', { exact: true })
  })

  it("the last-active-admin 409 renders verbatim on the isYou row too, and YOU doesn't collide with the reason text", async () => {
    const m = member({ isYou: true })
    const LAST_ADMIN_REASON = "this is the tenant's last active admin -- make another member an active admin first"
    mockedSetMembershipStatus.mockRejectedValue(new ApiError('http', LAST_ADMIN_REASON, 409))

    render(<Harness initial={[m, otherMember()]} />)
    openDrawer('Ada Person')
    clickSuspend()

    const matches = await screen.findAllByText(LAST_ADMIN_REASON)
    expect(matches.length).toBeGreaterThan(0)
  })
})

// ----------------------------------------------------------------------------
// APPR-10-04 Stage 4 QA — the primitive widened under this screen
// ----------------------------------------------------------------------------

describe('APPR-10-04 QA AC-1: the Access role filter is untouched by WfSelect\'s new props', () => {
  // APPR-10-04 gave the Workflows-owned `WfSelect` its first `disabled`/`title`/
  // `ariaDescribedBy`, and disabled them on the delegation controls two screens away. All three
  // props are OPTIONAL and this call site (MembersView.tsx:127) passes none — but "the source
  // diff is empty" is not the same claim as "the rendered control did not move", and an
  // unconditional paint or a defaulted `disabled` inside the shared primitive would satisfy
  // every Workflows spec while muting this toolbar. This is the RENDERED half of AC-1.
  it('renders live, unpainted, and still filters', () => {
    render(<Harness initial={[member(), otherMember()]} />)

    const filter = screen.getByLabelText('Access role') as HTMLElement
    // `hideLabel` puts the aria-label on the <label> wrapper, so descend to the control.
    const select = (filter.tagName === 'LABEL' ? filter.querySelector('select') : filter) as HTMLSelectElement
    expect(select, 'the Access role filter is not a <select>, so the properties below read nothing').toBeTruthy()
    expect(select.tagName).toBe('SELECT')

    expect(select.disabled, 'the shared primitive shut a control this story never opened').toBe(false)
    expect(select.closest('fieldset[disabled]'), 'this call site has no fieldset ancestor and must not grow one').toBeNull()
    expect(select.getAttribute('title'), 'the primitive defaulted a tooltip onto a live control').toBeNull()
    expect(select.getAttribute('aria-describedby'), 'the primitive defaulted an aria pointer onto a live control').toBeNull()

    // The resting paint, not merely "not the disabled paint" — an unconditional spread is caught
    // by the first two, a defaulted-away resting style only by these.
    expect(select.style.backgroundColor, 'the filter lost its resting background').toBe('var(--bg-1)')
    expect(select.style.color, 'the filter lost its resting foreground').toBe('var(--fg-1)')
    expect(select.style.cursor, 'the filter paints itself dead').toBe('pointer')

    // Still WIRED, not merely still painted: the roster narrows to the chosen role.
    const options = Array.from(select.options).map((o) => o.value)
    expect(options.length, 'the filter has no options, so the change below proves nothing').toBeGreaterThan(1)
    expect(screen.getByText('Other Person'), 'the fixture row is missing before the filter runs').toBeTruthy()
    fireEvent.change(select, { target: { value: 'admin' } })
    expect(select.value, 'the change never landed, so the filter was inert').toBe('admin')
    expect(screen.queryByText('Other Person'), 'the filter no longer narrows the roster').toBeNull()
  })
})

// ============================================================================
// RESEND-07-04 — admins invite from the Members screen
// ============================================================================
// Backend values, copied from their Go constants (internal/tenancy/tenancy.go errorStatus):
const ERR_NOT_PENDING = 'this invite is no longer pending' // 409 ErrInvitationNotPending
const ERR_NOT_FOUND = 'invitation not found' // 404 ErrInvitationNotFound
const ERR_DAILY_LIMIT = 'daily invite limit reached: 20 invite mails per workspace per 24 hours' // 429 ErrDailyInviteLimit
const ERR_INTERNAL = 'internal server error' // 500
const ADMIN_ONLY = 'Only an admin can invite people.' // D4; the 403 reads "only an admin can invite people"
const GW_BASE = 'https://gw'
const DAY_MS = 86_400_000

type Call = { url: string; method: string; body: unknown }
type Responder = (call: Call) => Promise<unknown>

const answer = (value: unknown): Responder => () => Promise.resolve(value)
const refuse = (status: number, message: string): Responder => () => Promise.reject(new ApiError('http', message, status))
function hold() {
  let resolve!: (v: unknown) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<unknown>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { responder: (() => promise) as Responder, resolve, reject }
}

// The nth call to a route takes the nth responder; the last one repeats.
function gateway(routes: { list?: Responder[]; send?: Responder[]; resend?: Responder[] }) {
  const calls: Call[] = []
  const seen = { list: 0, send: 0, resend: 0 }
  const authedFetch = vi.fn((url: string, opts?: { method?: string; body?: unknown }) => {
    const call: Call = { url, method: opts?.method ?? 'GET', body: opts?.body }
    calls.push(call)
    const path = url.replace(GW_BASE, '')
    const kind =
      path === '/api/tenancy/v1/invitations'
        ? call.method === 'GET'
          ? 'list'
          : 'send'
        : /^\/api\/tenancy\/v1\/invitations\/[^/]+\/resend$/.test(path) && call.method === 'POST'
          ? 'resend'
          : null
    const queue = kind ? routes[kind] : undefined
    if (!kind || !queue?.length) return Promise.reject(new Error(`unrouted ${call.method} ${url}`))
    return queue[Math.min(seen[kind]++, queue.length - 1)](call)
  })
  const of = (kind: 'list' | 'send' | 'resend') =>
    calls.filter((c) => {
      const isResend = /\/resend$/.test(c.url)
      if (kind === 'resend') return isResend
      if (isResend || !c.url.endsWith('/invitations')) return false
      return kind === 'list' ? c.method === 'GET' : c.method === 'POST'
    })
  return {
    authedFetch: authedFetch as unknown as AuthedFetch,
    lists: () => of('list'),
    sends: () => of('send'),
    resends: () => of('resend'),
    invitationCalls: () => calls.filter((c) => c.url.includes('/invitations')),
  }
}

function wire(over: Partial<InvitationWire> = {}): InvitationWire {
  return {
    id: 'i1',
    email: 'zed@x.ng',
    role: 'reviewer',
    status: 'pending',
    expires_at: new Date(Date.now() + 7 * DAY_MS).toISOString(),
    delivery: 'sent',
    ...over,
  }
}
const listOf = (...items: InvitationWire[]) => ({ invitations: items })
const inDays = (n: number) => new Date(Date.now() + n * DAY_MS).toISOString()

const selfRow = (over: Partial<Member> = {}) =>
  member({ id: 'me', name: 'Sam Admin', initials: 'SA', email: 'sam@x.ng', role: 'admin', status: 'active', isYou: true, ...over })
const adminRoster = () => [selfRow(), member()]

const inviteButton = () => screen.getByTestId('members-invite') as HTMLButtonElement
const inviteRows = () => screen.queryAllByTestId('invite-row')
const memberRows = () => screen.queryAllByTestId('member-row')
const inviteRowFor = (email: string) => inviteRows().find((r) => r.textContent?.includes(email)) as HTMLElement
const flash = () => screen.getByTestId('members-flash')

async function landed() {
  await waitFor(() => expect(inviteButton().disabled, 'the invite button never enabled').toBe(false))
}

function clickResendOn(row: HTMLElement) {
  fireEvent.click(within(row).getByTestId('member-menu-trigger'))
  fireEvent.click(within(screen.getByTestId('member-menu')).getByRole('button', { name: 'Resend invite' }))
}

describe('RESEND-07-04', () => {
  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', GW_BASE)
    emptyStateTitles.length = 0
  })
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  describe('the Invite button', () => {
    it('MembersView: an admin sees Invite enabled and it opens the modal', async () => {
      const gw = gateway({ list: [answer(listOf())] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)

      await landed()
      expect(screen.queryByTestId('members-invite-reason')).toBeNull()
      expect(gw.lists()).toHaveLength(1)
      expect(screen.queryByTestId('invite-modal')).toBeNull()

      fireEvent.click(inviteButton())
      expect(screen.getByTestId('invite-modal')).toBeTruthy()
    })

    it('MembersView: a non-admin sees Invite disabled with the admin-only reason', async () => {
      const gw = gateway({ list: [answer(listOf())] })
      render(<Harness initial={[selfRow({ role: 'preparer' }), member()]} authedFetch={gw.authedFetch} />)

      const note = screen.getByTestId('members-invite-reason')
      expect(note.textContent).toBe(ADMIN_ONLY)
      expect(inviteButton().disabled).toBe(true)
      expect(note.id, 'the note needs an id for aria-describedby to point at').not.toBe('')
      expect(inviteButton().getAttribute('aria-describedby')).toBe(note.id)

      await act(async () => {})
      expect(gw.invitationCalls(), 'a non-admin never touches /invitations').toEqual([])
    })

    it('MembersView: a suspended admin self row is not an admin', async () => {
      const gw = gateway({ list: [answer(listOf())] })
      render(<Harness initial={[selfRow({ status: 'suspended' }), member()]} authedFetch={gw.authedFetch} />)

      expect(screen.getByTestId('members-invite-reason').textContent).toBe(ADMIN_ONLY)
      expect(inviteButton().disabled).toBe(true)
      await act(async () => {})
      expect(gw.invitationCalls()).toEqual([])
    })

    it('MembersView: Invite is disabled with no reason while the roster loads', async () => {
      for (const state of ['loading', 'idle', 'error'] as const) {
        const gw = gateway({ list: [answer(listOf())] })
        render(
          <Harness
            initial={[]}
            authedFetch={gw.authedFetch}
            membersState={state}
            membersError={state === 'error' ? new ApiError('http', 'gateway is down', 503) : null}
          />,
        )

        expect(inviteButton().disabled, `${state}: the button is disabled`).toBe(true)
        expect(screen.queryByTestId('members-invite-reason'), `${state}: "only an admin" would be false to an admin`).toBeNull()
        await act(async () => {})
        expect(gw.invitationCalls(), `${state}: no roster, no viewer, no /invitations call`).toEqual([])
        cleanup()
      }
    })
  })

  describe('the first-load gate (D20, N3)', () => {
    it('MembersView: an admin with a one-person roster sees Loading, not Just you, until the list lands', async () => {
      const pending = hold()
      const gw = gateway({ list: [pending.responder] })
      render(<Harness initial={[selfRow()]} authedFetch={gw.authedFetch} />)

      expect(await screen.findByText('Loading members…')).toBeTruthy()
      expect(screen.queryByText('Just you at the firm')).toBeNull()
      expect(inviteButton().disabled).toBe(true)
      expect(gw.lists()).toHaveLength(1)

      await act(async () => pending.resolve(listOf()))
      expect(await screen.findByText('Just you at the firm')).toBeTruthy()
      expect(screen.queryByText('Loading members…')).toBeNull()
      expect(inviteButton().disabled).toBe(false)
    })

    it('MembersView: the first-load gate covers idle as well as loading', async () => {
      const pending = hold()
      const gw = gateway({ list: [pending.responder] })
      const controls: { current: HarnessControls | null } = { current: null }
      render(<Harness initial={[selfRow({ role: 'preparer' }), member()]} authedFetch={gw.authedFetch} controls={controls} />)
      expect(screen.getByTestId('members-table'), 'the roster landed as a table first').toBeTruthy()
      expect(gw.lists(), 'a preparer fetches nothing').toHaveLength(0)

      // The viewer's own row becomes an active admin, alone: the first admin render has the
      // list hook still idle, and an ungated roster would paint "Just you" for that frame.
      emptyStateTitles.length = 0
      act(() => controls.current!.setMembers([selfRow()]))

      expect(emptyStateTitles, 'no "Just you" frame between the roster landing and the list landing').not.toContain('Just you at the firm')
      expect(screen.getByText('Loading members…')).toBeTruthy()
      expect(inviteButton().disabled).toBe(true)
      expect(gw.lists()).toHaveLength(1)

      await act(async () => pending.resolve(listOf()))
      expect(await screen.findByText('Just you at the firm')).toBeTruthy()
      expect(emptyStateTitles, 'the spy sees the empty state once it is legitimate').toContain('Just you at the firm')
      expect(inviteButton().disabled).toBe(false)
    })
  })

  describe('pending rows', () => {
    it('MembersView: pending invites render after the members as invite rows', async () => {
      const gw = gateway({
        list: [
          answer(listOf(wire({ id: 'i1', email: 'zed@x.ng', role: 'reviewer' }), wire({ id: 'i2', email: 'yan@x.ng', role: 'admin' }))),
        ],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)

      await waitFor(() => expect(inviteRows()).toHaveLength(2))
      const members = memberRows()
      expect(members).toHaveLength(2)
      for (const m of members) {
        for (const i of inviteRows()) {
          expect(m.compareDocumentPosition(i) & Node.DOCUMENT_POSITION_FOLLOWING, 'every member row precedes every invite row').toBeTruthy()
        }
      }
      for (const [email, role] of [
        ['zed@x.ng', 'Reviewer'],
        ['yan@x.ng', 'Admin'],
      ]) {
        const row = inviteRowFor(email)
        expect(row, `${email} has an invite row`).toBeTruthy()
        expect(within(row).getByText(email)).toBeTruthy()
        expect(within(row).getByText(role)).toBeTruthy()
        expect(row.children[2].textContent, 'Workflow roles cell').toBe('—')
        expect(within(row).getByText('INVITED')).toBeTruthy()
        expect(within(row).getByText('Expires in 7 days')).toBeTruthy()
      }
    })

    it('MembersView: an empty list renders no invite row and no error', async () => {
      const gw = gateway({ list: [answer(listOf())] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)

      await landed()
      expect(memberRows(), 'the memberships render').toHaveLength(2)
      expect(inviteRows()).toHaveLength(0)
      expect(screen.queryByTestId('members-invites-error')).toBeNull()
    })

    it('MembersView: a failed delivery reads Email not sent', async () => {
      const gw = gateway({ list: [answer(listOf(wire({ delivery: 'failed' })))] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)

      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      expect(within(inviteRows()[0]).getByText('Email not sent')).toBeTruthy()
      expect(within(inviteRows()[0]).queryByText(/Expires/)).toBeNull()
    })

    it('MembersView: search and role filter reach pending rows', async () => {
      const gw = gateway({ list: [answer(listOf(wire({ email: 'zed@x.ng', role: 'reviewer' })))] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))

      fireEvent.change(screen.getByLabelText('Search members'), { target: { value: 'zed' } })
      expect(inviteRows()).toHaveLength(1)
      expect(memberRows(), 'only the pending row matches "zed"').toHaveLength(0)

      fireEvent.change(screen.getByLabelText('Search members'), { target: { value: '' } })
      const select = (screen.getByLabelText('Access role') as HTMLElement).querySelector('select') as HTMLSelectElement
      fireEvent.change(select, { target: { value: 'admin' } })
      expect(memberRows().length, 'the admin self row stays, so the filter ran').toBeGreaterThan(0)
      expect(inviteRows(), 'a reviewer invite is hidden by the Admin filter').toHaveLength(0)

      fireEvent.change(select, { target: { value: 'reviewer' } })
      expect(inviteRows()).toHaveLength(1)
    })

    it('MembersView: just-you plus a pending invite shows the table', async () => {
      const gw = gateway({ list: [answer(listOf(wire()))] })
      render(<Harness initial={[selfRow()]} authedFetch={gw.authedFetch} />)

      expect(await screen.findByTestId('members-table')).toBeTruthy()
      expect(inviteRows()).toHaveLength(1)
      expect(screen.queryByText('Just you at the firm')).toBeNull()
    })
  })

  describe('sending', () => {
    async function openModalAndType(gw: ReturnType<typeof gateway>, address: string) {
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await landed()
      fireEvent.click(inviteButton())
      fireEvent.change(screen.getByTestId('invite-modal-input'), { target: { value: address } })
      fireEvent.click(within(screen.getByTestId('invite-role-reviewer')).getByRole('radio'))
    }

    it("MembersView: sending adds the server's rows, closes the modal and flashes", async () => {
      const gw = gateway({
        list: [answer(listOf())],
        send: [answer(listOf(wire({ id: 'n1', email: 'a@x.ng', role: 'reviewer' })))],
      })
      await openModalAndType(gw, 'a@x.ng')
      fireEvent.click(screen.getByTestId('invite-modal-send'))

      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      expect(gw.sends()).toHaveLength(1)
      expect(gw.sends()[0].body, 'the modal sent its address and the chosen role').toEqual({ emails: ['a@x.ng'], role: 'reviewer' })
      expect(within(inviteRowFor('a@x.ng')).getByText('Reviewer')).toBeTruthy()
      expect(screen.queryByTestId('invite-modal')).toBeNull()
      expect(flash().textContent).toBe('Invite sent to a@x.ng.')
      expect(flash().style.color).toBe('var(--status-green-text)')
      expect(gw.lists(), 'a send adds the server rows without a refetch').toHaveLength(1)
    })

    it('MembersView: a failed delivery flashes the red notice', async () => {
      const gw = gateway({
        list: [answer(listOf())],
        send: [answer(listOf(wire({ id: 'n1', email: 'a@x.ng', delivery: 'failed' })))],
      })
      await openModalAndType(gw, 'a@x.ng')
      fireEvent.click(screen.getByTestId('invite-modal-send'))

      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      // D8: n = 1, f = 1.
      expect(flash().textContent).toBe("The invite email to a@x.ng did not go out. Resend it from the row's ⋯ menu.")
      expect(flash().style.color).toBe('var(--status-red-text)')
      expect(within(inviteRowFor('a@x.ng')).getByText('Email not sent')).toBeTruthy()
    })
  })

  describe('resending', () => {
    it('MembersView: Resend posts the id and replaces the row', async () => {
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng', expires_at: inDays(1) })))],
        resend: [answer(wire({ id: 'i1', email: 'zed@x.ng', expires_at: inDays(7) }))],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      expect(within(inviteRows()[0]).getByText('Expires in 1 day')).toBeTruthy()

      clickResendOn(inviteRows()[0])

      await waitFor(() => expect(within(inviteRowFor('zed@x.ng')).getByText('Expires in 7 days')).toBeTruthy())
      expect(gw.resends()).toHaveLength(1)
      expect(gw.resends()[0]).toMatchObject({ url: `${GW_BASE}/api/tenancy/v1/invitations/i1/resend`, method: 'POST' })
      expect(inviteRows(), 'one row for that address').toHaveLength(1)
      expect(flash().textContent).toBe('Invite sent to zed@x.ng.')
    })

    it('MembersView: a second Resend while the first is in flight sends nothing', async () => {
      const first = hold()
      const gw = gateway({ list: [answer(listOf(wire()))], resend: [first.responder] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))

      clickResendOn(inviteRows()[0])
      expect(gw.resends()).toHaveLength(1)

      fireEvent.click(within(inviteRows()[0]).getByTestId('member-menu-trigger'))
      const again = within(screen.getByTestId('member-menu')).getByRole('button', { name: 'Resend invite' }) as HTMLButtonElement
      expect(again.disabled).toBe(true)
      fireEvent.click(again)
      expect(gw.resends(), 'the POST count stays 1').toHaveLength(1)

      await act(async () => first.resolve(wire()))
    })

    it("MembersView: a refused resend renders the server's reason on that row and the next attempt clears it", async () => {
      const retry = hold()
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' }), wire({ id: 'i2', email: 'yan@x.ng' })))],
        resend: [refuse(429, ERR_DAILY_LIMIT), retry.responder],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(2))

      clickResendOn(inviteRowFor('zed@x.ng'))

      const errors = await screen.findAllByTestId('member-status-error')
      expect(errors, 'one error, on one row').toHaveLength(1)
      expect(errors[0].textContent, 'verbatim').toBe(ERR_DAILY_LIMIT)
      expect(inviteRowFor('zed@x.ng').nextElementSibling, "under that row").toBe(errors[0].parentElement)
      expect(inviteRowFor('yan@x.ng').nextElementSibling?.querySelector('[data-testid="member-status-error"]') ?? null).toBeNull()
      expect(screen.queryByTestId('members-invites-error'), 'a 429 belongs to the row, not the list').toBeNull()

      clickResendOn(inviteRowFor('zed@x.ng'))
      expect(screen.queryByTestId('member-status-error'), 'the attempt clears it at the click, before the POST settles').toBeNull()
      expect(gw.resends()).toHaveLength(2)

      await act(async () => retry.resolve(wire({ id: 'i1', email: 'zed@x.ng' })))
      expect(screen.queryByTestId('member-status-error')).toBeNull()
      expect(flash().textContent).toBe('Invite sent to zed@x.ng.')
    })

    // N1 + N4: a 404/409 means the row is stale; the screen refetches without blanking.
    async function stale(status: 409 | 404, message: string) {
      const refetch = hold()
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' }))), refetch.responder],
        resend: [refuse(status, message)],
      })
      const refetchMembers = vi.fn()
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} refetchMembers={refetchMembers} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))

      clickResendOn(inviteRows()[0])
      await waitFor(() => expect(gw.lists(), 'the list is refetched').toHaveLength(2))

      expect(memberRows(), 'every member row stays mounted while the refetch is held').toHaveLength(2)
      expect(inviteRows(), 'the invite row stays mounted while the refetch is held').toHaveLength(1)
      expect(screen.queryByText('Loading members…')).toBeNull()
      expect(screen.getByTestId('members-invites-error').textContent).toBe(message)

      await act(async () => refetch.resolve(listOf()))
      await waitFor(() => expect(inviteRows(), 'the row leaves once the list no longer holds it').toHaveLength(0))
      expect(memberRows()).toHaveLength(2)
      expect(screen.getByTestId('members-invites-error').textContent, 'the reason survives the refetch (N4)').toBe(message)
      expect(gw.lists()).toHaveLength(2)
      expect(refetchMembers).toHaveBeenCalledTimes(1)
      expect(gw.resends()).toHaveLength(1)
    }

    it('MembersView: a 409 resend refetches and the accepted invite leaves the list', async () => {
      await stale(409, ERR_NOT_PENDING)
    })

    it('MembersView: a 404 resend takes the same path', async () => {
      await stale(404, ERR_NOT_FOUND)
    })

    it('MembersView: the stale-resend reason clears at the next invite action', async () => {
      // After a 409 on i1 the refetched list holds i2 only.
      async function reachReason(list3?: Responder) {
        const gw = gateway({
          list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' }), wire({ id: 'i2', email: 'yan@x.ng' }))), answer(listOf(wire({ id: 'i2', email: 'yan@x.ng' }))), ...(list3 ? [list3] : [])],
          resend: [refuse(409, ERR_NOT_PENDING), hold().responder],
        })
        render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
        await waitFor(() => expect(inviteRows()).toHaveLength(2))
        clickResendOn(inviteRowFor('zed@x.ng'))
        await waitFor(() => expect(inviteRows()).toHaveLength(1))
        expect(screen.getByTestId('members-invites-error').textContent).toBe(ERR_NOT_PENDING)
        return gw
      }

      // (a) the next resend
      const gw = await reachReason()
      clickResendOn(inviteRowFor('yan@x.ng'))
      expect(gw.resends(), 'the POST is in flight').toHaveLength(2)
      expect(screen.queryByTestId('members-invites-error'), 'cleared at the click, before the POST settles').toBeNull()
      cleanup()

      // (b) opening the Invite modal
      await reachReason()
      await landed()
      fireEvent.click(inviteButton())
      expect(screen.getByTestId('invite-modal')).toBeTruthy()
      expect(screen.queryByText(ERR_NOT_PENDING), 'cleared when the modal opens').toBeNull()
      cleanup()

      // (c) Retry: the refetch after the 409 fails, so the list error offers Retry.
      const third = hold()
      const gw3 = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' }))), refuse(500, ERR_INTERNAL), third.responder],
        resend: [refuse(409, ERR_NOT_PENDING)],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw3.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      clickResendOn(inviteRows()[0])
      const retry = await screen.findByTestId('members-invites-retry')
      expect(screen.getByTestId('members-invites-error')).toBeTruthy()
      fireEvent.click(retry)
      expect(gw3.lists(), 'Retry re-runs the list').toHaveLength(3)
      expect(screen.queryByText(ERR_NOT_PENDING), 'cleared at Retry').toBeNull()
      await act(async () => third.resolve(listOf()))
    })

    it('MembersView: a resend error never reaches the drawer', async () => {
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' })))],
        resend: [refuse(429, ERR_DAILY_LIMIT)],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))

      clickResendOn(inviteRows()[0])
      expect(await screen.findByTestId('member-status-error')).toBeTruthy()

      openDrawer('Ada Person')
      const drawer = screen.getByTestId('member-drawer')
      expect(within(drawer).queryByTestId('member-status-error')).toBeNull()
      expect(within(drawer).queryByTestId('member-drawer-status-error')).toBeNull()
      expect(within(drawer).queryByText(ERR_DAILY_LIMIT)).toBeNull()
      expect(screen.getAllByTestId('member-status-error'), 'the table row still carries it').toHaveLength(1)
    })
  })

  describe('a failed list', () => {
    it('MembersView: a failed invitations fetch shows its reason, keeps the roster and disables Invite', async () => {
      const retried = hold()
      const refetchMembers = vi.fn()
      const gw = gateway({ list: [refuse(500, ERR_INTERNAL), retried.responder] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} refetchMembers={refetchMembers} />)

      const error = await screen.findByTestId('members-invites-error')
      expect(error.textContent).toContain(ERR_INTERNAL)
      expect(memberRows()).toHaveLength(2)
      expect(inviteButton().disabled).toBe(true)

      fireEvent.click(screen.getByTestId('members-invites-retry'))
      expect(gw.lists(), 'Retry re-runs the list').toHaveLength(2)
      expect(memberRows(), 'every member row stays mounted while the retry is held (N1)').toHaveLength(2)
      expect(screen.queryByText('Loading members…')).toBeNull()
      expect(refetchMembers, 'Retry re-runs only the invitations fetch').not.toHaveBeenCalled()

      await act(async () => retried.resolve(listOf()))
      await landed()
      expect(screen.queryByTestId('members-invites-error')).toBeNull()
    })

    it('MembersView: a failed invitations fetch links the disabled Invite to its reason', async () => {
      const gw = gateway({ list: [refuse(500, ERR_INTERNAL)] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)

      const error = await screen.findByTestId('members-invites-error')
      expect(error.id).not.toBe('')
      expect(inviteButton().disabled).toBe(true)
      expect(inviteButton().getAttribute('aria-describedby')).toBe(error.id)
    })

    it('MembersView: Invite is disabled while any invitations refetch is in flight, rows stay mounted', async () => {
      const refetch = hold()
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' }))), refetch.responder],
        resend: [refuse(409, ERR_NOT_PENDING)],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await landed()
      expect(inviteButton().disabled).toBe(false)

      clickResendOn(inviteRows()[0])
      await waitFor(() => expect(gw.lists()).toHaveLength(2))
      expect(inviteButton().disabled, 'a POST now could race the held GET').toBe(true)
      expect(memberRows()).toHaveLength(2)
      expect(screen.queryByText('Loading members…')).toBeNull()

      await act(async () => refetch.resolve(listOf(wire({ id: 'i1', email: 'zed@x.ng' }))))
      await landed()
      expect(inviteButton().disabled).toBe(false)
    })

    it('MembersView: a refetch that fails after the list landed keeps the rows, shows both reasons and disables Invite until Retry lands', async () => {
      const retried = hold()
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' }))), refuse(500, ERR_INTERNAL), retried.responder],
        resend: [refuse(409, ERR_NOT_PENDING)],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await landed()
      expect(inviteRows()).toHaveLength(1)

      clickResendOn(inviteRows()[0])
      await screen.findByTestId('members-invites-retry')

      const error = screen.getByTestId('members-invites-error')
      expect(error.textContent, 'the stale reason and the list error both show').toContain(ERR_NOT_PENDING)
      expect(error.textContent).toContain(ERR_INTERNAL)
      expect(inviteRows(), 'a failed refetch leaves the mirror alone').toHaveLength(1)
      expect(memberRows()).toHaveLength(2)
      expect(inviteButton().disabled, 'Invite waits for a list it can check "Already invited" against').toBe(true)

      fireEvent.click(screen.getByTestId('members-invites-retry'))
      await act(async () => retried.resolve(listOf()))
      await waitFor(() => expect(inviteRows()).toHaveLength(0))
      expect(screen.queryByTestId('members-invites-error')).toBeNull()
      expect(inviteButton().disabled).toBe(false)
    })
  })

  describe('adversarial: the viewer, the gateway and the wiring', () => {
    it('MembersView: an admin who stops being an admin loses the pending rows and Invite, with no new fetch', async () => {
      const gw = gateway({ list: [answer(listOf(wire()))] })
      const controls: { current: HarnessControls | null } = { current: null }
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} controls={controls} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      expect(inviteButton().disabled).toBe(false)

      act(() => controls.current!.setMembers([selfRow({ role: 'reviewer' }), member()]))

      expect(inviteRows(), 'a non-admin sees no pending invite').toHaveLength(0)
      expect(memberRows().length, 'the memberships still render').toBeGreaterThan(0)
      expect(inviteButton().disabled).toBe(true)
      expect(screen.getByTestId('members-invite-reason').textContent).toBe(ADMIN_ONLY)
      await act(async () => {})
      expect(gw.lists(), 'losing admin never refetches').toHaveLength(1)
    })

    it("MembersView: another member's admin row does not make the viewer an admin", async () => {
      const gw = gateway({ list: [answer(listOf())] })
      const boss = member({ id: 'boss', name: 'Boss Admin', initials: 'BA', email: 'boss@x.ng', role: 'admin', status: 'active', isYou: false })
      render(<Harness initial={[selfRow({ role: 'preparer' }), boss]} authedFetch={gw.authedFetch} />)

      expect(inviteButton().disabled).toBe(true)
      expect(screen.getByTestId('members-invite-reason').textContent).toBe(ADMIN_ONLY)
      await act(async () => {})
      expect(gw.invitationCalls()).toEqual([])
    })

    it('MembersView: with no gateway configured the roster renders and Invite stays disabled without a call', async () => {
      vi.stubEnv('VITE_GATEWAY_URL', '')
      const gw = gateway({ list: [answer(listOf(wire()))] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)

      await act(async () => {})
      expect(memberRows(), 'the roster is not held behind a list that will never load').toHaveLength(2)
      expect(screen.queryByText('Loading members…')).toBeNull()
      expect(inviteButton().disabled).toBe(true)
      expect(gw.invitationCalls()).toEqual([])
    })

    it('MembersView: the Invite modal checks addresses against the memberships and the pending invites', async () => {
      const gw = gateway({ list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' })))] })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      await landed()
      fireEvent.click(inviteButton())

      const input = screen.getByTestId('invite-modal-input')
      for (const address of ['ada@x.ng', 'zed@x.ng', 'fresh@x.ng']) {
        fireEvent.change(input, { target: { value: address } })
        fireEvent.keyDown(input, { key: 'Enter' })
      }
      const chips = screen.getAllByTestId('invite-chip')
      expect(chips.map((c) => c.getAttribute('data-verdict'))).toEqual(['member', 'invited', 'ok'])
      expect(within(chips[0]).getByTestId('invite-chip-error').textContent).toBe('Already a member')
      expect(within(chips[1]).getByTestId('invite-chip-error').textContent).toBe('Already invited')
    })

    it('MembersView: a click on a pending row opens no drawer, and the same click on a member row does', async () => {
      // The invited membership has a drawer row to open, so a live click handler would show one.
      const gw = gateway({ list: [answer(listOf())] })
      render(<Harness initial={[...adminRoster(), otherMember()]} authedFetch={gw.authedFetch} />)
      await landed()
      expect(inviteRows()).toHaveLength(1)

      fireEvent.click(inviteRows()[0])
      expect(screen.queryByTestId('member-drawer'), 'an invitation has no membership to edit').toBeNull()

      fireEvent.click(memberRows()[1])
      expect(screen.getByTestId('member-drawer')).toBeTruthy()
    })

    it('MembersView: the flash clears after three seconds', async () => {
      const timeouts = vi.spyOn(window, 'setTimeout')
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' })))],
        resend: [answer(wire({ id: 'i1', email: 'zed@x.ng' }))],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))
      clickResendOn(inviteRows()[0])
      await screen.findByTestId('members-flash')

      const timer = timeouts.mock.calls.find((c) => c[1] === 3000)
      expect(timer, 'the flash arms a 3000 ms timer').toBeTruthy()
      act(() => (timer![0] as () => void)())
      expect(screen.queryByTestId('members-flash')).toBeNull()
      timeouts.mockRestore()
    })
  })

  describe('adversarial: resend outcomes', () => {
    it('MembersView: a resend whose mail fails again reads Email not sent and flashes red', async () => {
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng', expires_at: inDays(1) })))],
        resend: [answer(wire({ id: 'i1', email: 'zed@x.ng', expires_at: inDays(7), delivery: 'failed' }))],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))

      clickResendOn(inviteRows()[0])

      await waitFor(() => expect(within(inviteRows()[0]).getByText('Email not sent')).toBeTruthy())
      expect(inviteRows(), 'still one row').toHaveLength(1)
      expect(flash().textContent).toBe("The invite email to zed@x.ng did not go out. Resend it from the row's ⋯ menu.")
      expect(flash().style.color).toBe('var(--status-red-text)')
    })

    it('MembersView: a successful resend repairs a row that said Email not sent', async () => {
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng', delivery: 'failed' })))],
        resend: [answer(wire({ id: 'i1', email: 'zed@x.ng', delivery: 'sent' }))],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(within(inviteRows()[0]).getByText('Email not sent')).toBeTruthy())

      clickResendOn(inviteRows()[0])

      await waitFor(() => expect(within(inviteRows()[0]).getByText('Expires in 7 days')).toBeTruthy())
      expect(within(inviteRows()[0]).queryByText('Email not sent')).toBeNull()
      expect(inviteRows()).toHaveLength(1)
      expect(flash().style.color).toBe('var(--status-green-text)')
    })

    it('MembersView: a 500 resend is a row error, not a stale-invite refetch', async () => {
      const refetchMembers = vi.fn()
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' })))],
        resend: [refuse(500, ERR_INTERNAL)],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} refetchMembers={refetchMembers} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))

      clickResendOn(inviteRows()[0])

      expect((await screen.findByTestId('member-status-error')).textContent).toBe(ERR_INTERNAL)
      expect(screen.queryByTestId('members-invites-error')).toBeNull()
      expect(inviteRows(), 'the row stays').toHaveLength(1)
      expect(gw.lists(), 'no list refetch').toHaveLength(1)
      expect(refetchMembers).not.toHaveBeenCalled()
    })

    it("MembersView: resending one invite leaves another invite's Resend enabled", async () => {
      const first = hold()
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' }), wire({ id: 'i2', email: 'yan@x.ng' })))],
        resend: [first.responder, answer(wire({ id: 'i2', email: 'yan@x.ng' }))],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(2))

      clickResendOn(inviteRowFor('zed@x.ng'))
      expect(gw.resends()).toHaveLength(1)

      fireEvent.click(within(inviteRowFor('yan@x.ng')).getByTestId('member-menu-trigger'))
      const other = within(screen.getByTestId('member-menu')).getByRole('button', { name: 'Resend invite' }) as HTMLButtonElement
      expect(other.disabled, "i1's flight does not lock i2").toBe(false)
      fireEvent.click(other)
      expect(gw.resends().map((c) => c.url)).toEqual([
        `${GW_BASE}/api/tenancy/v1/invitations/i1/resend`,
        `${GW_BASE}/api/tenancy/v1/invitations/i2/resend`,
      ])

      await act(async () => first.resolve(wire({ id: 'i1', email: 'zed@x.ng' })))
    })

    it("MembersView: a resend does not clear another row's failed-suspend reason", async () => {
      mockedSetMembershipStatus.mockRejectedValue(new ApiError('http', REASON, 409))
      const gw = gateway({
        list: [answer(listOf(wire({ id: 'i1', email: 'zed@x.ng' })))],
        resend: [answer(wire({ id: 'i1', email: 'zed@x.ng' }))],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await waitFor(() => expect(inviteRows()).toHaveLength(1))

      suspendFromRowMenu(rowFor('Ada Person'))
      expect((await screen.findByTestId('member-status-error')).textContent).toBe(REASON)

      clickResendOn(inviteRows()[0])
      await waitFor(() => expect(flash().textContent).toBe('Invite sent to zed@x.ng.'))
      expect(screen.getByTestId('member-status-error').textContent, "the resend touched only its own row's slot").toBe(REASON)
    })

    it('MembersView: an invited membership with no invitation answers Resend with the 404 path and throws nothing', async () => {
      const refetchMembers = vi.fn()
      const gw = gateway({ list: [answer(listOf()), answer(listOf())], resend: [refuse(404, ERR_NOT_FOUND)] })
      render(<Harness initial={[...adminRoster(), otherMember()]} authedFetch={gw.authedFetch} refetchMembers={refetchMembers} />)
      await landed()
      expect(inviteRows(), 'the invited membership renders as an invite row').toHaveLength(1)
      expect(within(inviteRows()[0]).getByText('INVITED')).toBeTruthy()

      clickResendOn(inviteRows()[0])

      await waitFor(() => expect(screen.getByTestId('members-invites-error').textContent).toBe(ERR_NOT_FOUND))
      expect(gw.resends()[0].url).toBe(`${GW_BASE}/api/tenancy/v1/invitations/other1/resend`)
      expect(refetchMembers).toHaveBeenCalledTimes(1)
      await waitFor(() => expect(gw.lists()).toHaveLength(2))
    })
  })

  describe('adversarial: a mixed batch', () => {
    it('MembersView: one failed mail in a batch flashes the count in red and marks only that row', async () => {
      const gw = gateway({
        list: [answer(listOf())],
        send: [
          answer(
            listOf(
              wire({ id: 'n1', email: 'a@x.ng', role: 'preparer' }),
              wire({ id: 'n2', email: 'b@x.ng', role: 'preparer', delivery: 'failed' }),
            ),
          ),
        ],
      })
      render(<Harness initial={adminRoster()} authedFetch={gw.authedFetch} />)
      await landed()
      fireEvent.click(inviteButton())
      fireEvent.change(screen.getByTestId('invite-modal-input'), { target: { value: 'a@x.ng, b@x.ng' } })
      fireEvent.click(screen.getByTestId('invite-modal-send'))

      await waitFor(() => expect(inviteRows()).toHaveLength(2))
      expect(gw.sends()[0].body).toEqual({ emails: ['a@x.ng', 'b@x.ng'], role: 'preparer' })
      expect(flash().textContent).toBe("1 of 2 invite emails did not go out. Resend it from the row's ⋯ menu.")
      expect(flash().style.color).toBe('var(--status-red-text)')
      expect(within(inviteRowFor('a@x.ng')).getByText('Expires in 7 days')).toBeTruthy()
      expect(within(inviteRowFor('a@x.ng')).queryByText('Email not sent')).toBeNull()
      expect(within(inviteRowFor('b@x.ng')).getByText('Email not sent')).toBeTruthy()
      expect(screen.queryByTestId('invite-modal')).toBeNull()
    })
  })
})
