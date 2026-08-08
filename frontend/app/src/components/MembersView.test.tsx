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

import { ApiError } from '@invoice-os/api-client'
import { PROTECTED_ADMIN_NOTE, replaceMember, setMembershipStatus, toMember, type Member, type MembershipWire } from '../lib/members'
import type { AuthedFetch } from '../lib/portfolio'
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

// Reproduces the funnel App.tsx's own feat commit will build (story plan §3): the server's
// row wins via replaceMember(toMember(...)), no optimistic write, and the rejection is never
// caught here so AC4's owner (MembersView) can render it once it exists. Test scaffolding,
// not production code -- App.tsx itself is untouched by this commit.
function Harness({ initial }: { initial: Member[] }) {
  const [members, setMembers] = useState<Member[]>(initial)

  async function setMemberStatus(id: string, status: Exclude<Member['status'], 'invited'>) {
    const wire = await setMembershipStatus(noopFetch, BASE, id, status)
    setMembers((list) => replaceMember(list, toMember(wire, SUBJECT)))
  }

  const ctx = {
    members,
    mode: 'firm',
    policies: [],
    roles: [],
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
