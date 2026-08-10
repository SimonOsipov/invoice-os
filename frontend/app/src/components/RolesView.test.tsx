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

function Harness({
  members = [],
  roles = [],
  rolesState = 'ready',
  rolesError = null,
  membersState = 'ready',
  membersError = null,
  refetchRoles,
  refetchMembers,
}: {
  members?: Member[]
  roles?: Role[]
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
    policies: [],
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

describe('a null-email member in the role modal picker renders the shared em dash, not "null"', () => {
  it('the picker row shows the em dash label', () => {
    render(<Harness members={[member({ email: null })]} roles={[role()]} rolesState="ready" membersState="ready" />)

    fireEvent.click(screen.getByTestId('role-card-edit'))
    const picker = screen.getByTestId('role-modal-member')
    expect(within(picker).getByText('—')).toBeTruthy()
    expect(within(picker).queryByText(/null/i)).toBeNull()
  })
})
