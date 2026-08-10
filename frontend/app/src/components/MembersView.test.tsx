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
import { useState } from 'react'

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncStatus } from '@invoice-os/api-client'
import { PROTECTED_ADMIN_NOTE, replaceMember, setMembershipStatus, toMember, type Member, type MembershipWire } from '../lib/members'
import type { AuthedFetch } from '../lib/portfolio'
import type { Role } from '../lib/roles'
import type { PlatformCtx } from '../types'
import { MembersView } from './MembersView'

vi.mock('../lib/members', async (orig) => ({
  ...(await orig<typeof import('../lib/members')>()),
  setMembershipStatus: vi.fn(),
}))

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
function Harness({
  initial,
  membersState,
  membersError,
  refetchMembers,
  roles,
  rolesState,
  rolesError,
  refetchRoles,
}: {
  initial: Member[]
  membersState?: AsyncStatus
  membersError?: ApiError | null
  refetchMembers?: () => void
  roles?: Role[]
  rolesState?: AsyncStatus
  rolesError?: ApiError | null
  refetchRoles?: () => void
}) {
  const [members, setMembers] = useState<Member[]>(initial)

  async function setMemberStatus(id: string, status: Exclude<Member['status'], 'invited'>) {
    const wire = await setMembershipStatus(noopFetch, BASE, id, status)
    setMembers((list) => replaceMember(list, toMember(wire, SUBJECT)))
  }

  const ctx = {
    members,
    mode: 'firm',
    policies: [],
    roles: roles ?? [],
    membersState: membersState ?? 'ready',
    membersError: membersError ?? null,
    refetchMembers: refetchMembers ?? vi.fn(),
    rolesState: rolesState ?? 'ready',
    rolesError: rolesError ?? null,
    refetchRoles: refetchRoles ?? vi.fn(),
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
