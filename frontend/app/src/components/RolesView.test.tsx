// @vitest-environment jsdom
//
// QA (Stage 4) coverage for APPR-15-06 -- no component test existed for this tab before this
// commit. RolesView shares `membersSurface` with MembersView (MembersView.tsx / RolesView.tsx
// docblocks: "two rosters of one tenant on one screen must not disagree"), so the ladder is
// re-verified here rather than assumed from the sibling tab's coverage.
//
// Extended for the rolesSurface repoint (AC-1 through AC-4): the ladder must branch on BOTH
// the roles fetch and the members fetch, not membersState alone.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncStatus } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { RolesView } from './RolesView'

function member(over: Partial<Member> = {}): Member {
  return {
    id: 'u1',
    name: 'Ada Person',
    initials: 'AP',
    email: 'ada@x.ng',
    role: 'preparer',
    status: 'active',
    isYou: false,
    ...over,
  }
}

function role(over: Partial<Role> = {}): Role {
  return { key: 'finance-approver', title: 'Finance Approver', desc: 'Approves finance invoices', members: [], ...over }
}

/** One approval step naming `role()`'s default key — the landed answer the gate must let through. */
function policy(over: Partial<Policy> = {}): Policy {
  return {
    id: 'p1',
    name: 'Test policy',
    scope: 'All invoices',
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'n1', type: 'approval', role: 'finance-approver', sla: '24', delegate: false }],
    ...over,
  }
}

function Harness({
  members = [],
  roles = [],
  policies = [],
  policiesState = 'ready',
  rolesState = 'ready',
  rolesError = null,
  membersState = 'ready',
  membersError = null,
  refetchRoles,
  refetchMembers,
}: {
  members?: Member[]
  roles?: Role[]
  policies?: Policy[]
  policiesState?: AsyncStatus
  rolesState?: AsyncStatus
  rolesError?: ApiError | null
  membersState?: AsyncStatus
  membersError?: ApiError | null
  refetchRoles?: () => void
  refetchMembers?: () => void
}) {
  const ctx = {
    members,
    roles,
    policies,
    policiesState,
    policiesError: null,
    refetchPolicies: vi.fn(),
    publishPolicy: vi.fn(),
    rolesState,
    rolesError,
    refetchRoles: refetchRoles ?? vi.fn(),
    membersState,
    membersError,
    refetchMembers: refetchMembers ?? vi.fn(),
    createRole: vi.fn(),
    renameRole: vi.fn(),
    staffRole: vi.fn(),
    deleteRole: vi.fn(),
  }
  return <RolesView ctx={ctx as unknown as PlatformCtx} />
}

afterEach(cleanup)

describe('AC-1: RolesView branches on rolesSurface(rolesState, membersState), never on roles.length/members.length', () => {
  it('an errored roles fetch renders the error surface, not an empty grid', () => {
    render(<Harness rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" />)

    expect(screen.getByText('gateway is down')).toBeTruthy()
    expect(screen.queryByTestId('roles-grid')).toBeNull()
    expect(screen.queryByTestId('roles-empty')).toBeNull()
  })

  it('an errored members fetch under a landed roles fetch still blocks the grid', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} />)

    expect(screen.queryByTestId('roles-grid')).toBeNull()
    expect(screen.getByText('gateway is down')).toBeTruthy()
  })

  it('a loading roles fetch renders no card', () => {
    render(<Harness roles={[role()]} rolesState="loading" membersState="ready" />)

    expect(screen.queryByTestId('role-card')).toBeNull()
    expect(screen.queryByTestId('roles-grid')).toBeNull()
  })
})

describe('AC-2: ErrorState renders rolesError ?? membersError; retry calls the matching refetch', () => {
  it('retry calls refetchRoles when the roles fetch is the one that failed', () => {
    const refetchRoles = vi.fn()
    render(<Harness rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" refetchRoles={refetchRoles} />)

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchRoles).toHaveBeenCalledOnce()
  })

  it('retry calls refetchMembers when the members fetch is the one that failed', () => {
    const refetchMembers = vi.fn()
    render(<Harness rolesState="ready" membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} refetchMembers={refetchMembers} />)

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchMembers).toHaveBeenCalledOnce()
  })

  // QA: mutation-tested gap. `retry` firing refetchMembers unconditionally stayed green
  // under the existing two tests, since neither asserted the OTHER fetch's refetch was NOT
  // called. The docblock's own claim ("a roles-only failure must not re-kick members",
  // RolesView.tsx:67) had nothing pinning it.
  it('retry does not call refetchMembers when only the roles fetch errored', () => {
    const refetchRoles = vi.fn()
    const refetchMembers = vi.fn()
    render(
      <Harness rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" refetchRoles={refetchRoles} refetchMembers={refetchMembers} />,
    )

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchRoles).toHaveBeenCalledOnce()
    expect(refetchMembers).not.toHaveBeenCalled()
  })

  it('retry does not call refetchRoles when only the members fetch errored', () => {
    const refetchRoles = vi.fn()
    const refetchMembers = vi.fn()
    render(
      <Harness rolesState="ready" membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} refetchRoles={refetchRoles} refetchMembers={refetchMembers} />,
    )

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchMembers).toHaveBeenCalledOnce()
    expect(refetchRoles).not.toHaveBeenCalled()
  })

  it('retry calls both refetches when both fetches errored, and the roles error message wins', () => {
    const refetchRoles = vi.fn()
    const refetchMembers = vi.fn()
    render(
      <Harness
        rolesState="error"
        rolesError={new ApiError('http', 'roles down', 503)}
        membersState="error"
        membersError={new ApiError('http', 'members down', 503)}
        refetchRoles={refetchRoles}
        refetchMembers={refetchMembers}
      />,
    )

    expect(screen.getByText('roles down')).toBeTruthy()
    expect(screen.queryByText('members down')).toBeNull()

    fireEvent.click(screen.getByText('Retry'))
    expect(refetchRoles).toHaveBeenCalledOnce()
    expect(refetchMembers).toHaveBeenCalledOnce()
  })
})

describe('AC-3: roles-empty renders only on a landed, genuinely empty roles list', () => {
  it('the empty state renders on a landed, empty roles list', () => {
    render(<Harness roles={[]} rolesState="ready" membersState="ready" />)

    expect(screen.getByTestId('roles-empty')).toBeTruthy()
  })

  it('the empty state does not render over an errored roles fetch, even with an empty list', () => {
    render(<Harness roles={[]} rolesState="error" rolesError={new ApiError('http', 'gateway is down', 503)} membersState="ready" />)

    expect(screen.queryByTestId('roles-empty')).toBeNull()
  })
})

describe('AC-4: the roles-unassigned banner stays gated on the full roster surface', () => {
  it('the unassigned banner never fires over an unlanded roles fetch', () => {
    // Zero holders makes the role genuinely unassigned once the roster has landed -- what
    // this isolates is that an UNLANDED roles fetch suppresses the notice regardless.
    render(<Harness roles={[role()]} rolesState="loading" membersState="ready" />)

    expect(screen.queryByTestId('roles-unassigned')).toBeNull()
  })

  it('the same unheld role does raise the notice once the roster has actually landed', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" />)

    expect(screen.getByTestId('roles-unassigned')).toBeTruthy()
  })
})

// ============================================================================
// APPR-09-06 (task-510) — the fifth `ctx.policies` reader
// ============================================================================
// The card footer renders on EVERY card and was gated only by `rolesSurface(rolesState,
// membersState)`, which does not carry policies — so an unlanded fetch read 'not used in any
// policy' on every card at once, the same false claim as RoleModal's delete confirm at grid
// volume. The footer now forks on `policiesLanded` (RolesView.tsx:181).

describe('APPR-09-06 AC-1/AC-3: a role card claims policy usage only off a landed policies fetch', () => {
  /** Header / holders / footer. MembersTable.test.tsx:42's `row.children[2]` idiom — the
   *  footer carries no test id, and RolesView.tsx is not edited to give it one. */
  function footerOf(card: HTMLElement): HTMLElement {
    return card.children[2] as HTMLElement
  }

  it('a role card claims no policy usage only once the policies fetch has landed', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="loading" />)

    const footer = footerOf(screen.getByTestId('role-card'))
    // Population floor: the footer's OTHER slot must still render, or both absences below
    // would pass on a card that simply lost its footer.
    expect(footer.textContent, 'the footer did not render, so the absences below are vacuous').toContain('0 people')
    expect(footer.textContent, 'an unlanded policies fetch reads as "this role is used nowhere"').not.toContain('not used in any policy')
    // The unlanded answer is the EMPTY STRING. '—' claims "nothing here" in its own right —
    // MembersTable.tsx:195-197 sets that convention, pinned on the roster side at
    // MembersTable.test.tsx:49.
    expect(footer.textContent, "the unlanded footer fell back to the em dash, which is its own claim").not.toContain('—')
  })

  it('a landed-empty policy list still prints the real zero-usage footer', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="empty" />)

    expect(footerOf(screen.getByTestId('role-card')).textContent, 'the guard swallowed a genuinely landed-empty answer').toContain(
      'not used in any policy',
    )
  })

  // ------------------------------------------------------------------------
  // QA (Stage 4) — adversarial coverage the RED set did not carry
  // ------------------------------------------------------------------------

  it('a landed policy that names the role prints the real count — the footer CAN make a claim', () => {
    // The population floor under BOTH absences above: without this, a gate that blanked the
    // footer in every state would satisfy every assertion in this describe.
    render(
      <Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[policy()]} policiesState="ready" />,
    )

    expect(footerOf(screen.getByTestId('role-card')).textContent).toContain('1 approval step · 1 policy')
  })

  it('an errored policies fetch withholds the claim too, not only a loading one', () => {
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="error" />)

    const footer = footerOf(screen.getByTestId('role-card'))
    expect(footer.textContent, 'the footer did not render, so the absences below are vacuous').toContain('0 people')
    expect(footer.textContent).not.toContain('not used in any policy')
    expect(footer.textContent).not.toContain('—')
  })

  it("'idle' — no gateway configured — is the LANDED side, matching the Workflows screen", () => {
    // `membersSurface` folds 'idle' into 'empty' (lib/members.ts:586), which WorkflowsView.tsx:65-68
    // relies on to render its own no-policies-yet card on that build. A gate written as
    // `surface === 'roster'` would disagree with the Workflows screen about the same fetch.
    render(<Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[]} policiesState="idle" />)

    expect(footerOf(screen.getByTestId('role-card')).textContent).toContain('not used in any policy')
  })

  it('a refetch WITHHOLDS the last landed count rather than printing it stale', () => {
    // Reachable: createPolicy/deletePolicy call `policiesAsync.run()` (App.tsx:1037,:1046), which
    // dispatches 'start' → status 'loading' while the mirror keeps the rows it just patched
    // (App.tsx:299-301). So 'loading' WITH rows is a real state, and this pins which way it falls.
    // DELIBERATE DIVERGENCE from MemberDrawer/MembersTable, which keep printing off the same
    // stale rows — their gate is `stepsForMember`'s null-at-zero, which never makes a negative
    // claim, so they have nothing to withhold. Withholding here is one round trip of silence
    // against a sentence that could be wrong; the Workflows list blanks wholesale in the same
    // window (WorkflowsView.tsx:116's `surface === 'loading'` arm).
    render(
      <Harness roles={[role()]} rolesState="ready" membersState="ready" policies={[policy()]} policiesState="loading" />,
    )

    const footer = footerOf(screen.getByTestId('role-card'))
    expect(footer.textContent, 'the footer did not render, so the absence below is vacuous').toContain('0 people')
    expect(footer.textContent, 'the mid-refetch footer printed a count the fetch had not confirmed').not.toContain('approval step')
  })

  it('EVERY card blanks, not merely the first', () => {
    const roles = [role(), role({ key: 'cfo', title: 'CFO' }), role({ key: 'ceo', title: 'CEO' })]
    render(<Harness roles={roles} rolesState="ready" membersState="ready" policies={[]} policiesState="loading" />)

    const cards = screen.getAllByTestId('role-card')
    expect(cards, 'the grid rendered no cards, so the loop below is vacuous').toHaveLength(roles.length)
    for (const card of cards) {
      const footer = footerOf(card)
      expect(footer.textContent, 'a card lost its footer, so its absence below is vacuous').toContain('0 people')
      expect(footer.textContent, 'one card still claims "used nowhere" off an unlanded fetch').not.toContain('not used in any policy')
    }
  })
})

describe('a null-email member in the role modal picker renders the shared em dash, not "null"', () => {
  it('the picker row shows the em dash label', () => {
    render(<Harness members={[member({ email: null })]} roles={[role()]} rolesState="ready" membersState="ready" />)

    fireEvent.click(screen.getByTestId('role-card-edit'))
    const picker = screen.getByTestId('role-modal-member')
    expect(within(picker).getByText('—')).toBeTruthy()
    expect(within(picker).queryByText(/null/i)).toBeNull()
  })
})
