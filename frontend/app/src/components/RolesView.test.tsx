// @vitest-environment jsdom
//
// QA (Stage 4) coverage for APPR-15-06 -- no component test existed for this tab before this
// commit. RolesView shares `membersSurface` with MembersView (MembersView.tsx / RolesView.tsx
// docblocks: "two rosters of one tenant on one screen must not disagree"), so the ladder is
// re-verified here rather than assumed from the sibling tab's coverage.
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
  membersState = 'ready',
  membersError = null,
  refetchMembers,
}: {
  members?: Member[]
  roles?: Role[]
  membersState?: AsyncStatus
  membersError?: ApiError | null
  refetchMembers?: () => void
}) {
  const ctx = {
    members,
    roles,
    policies: [],
    membersState,
    membersError,
    refetchMembers: refetchMembers ?? vi.fn(),
    saveRole: vi.fn(),
    addRole: vi.fn(),
    deleteRole: vi.fn(),
  }
  return <RolesView ctx={ctx as unknown as PlatformCtx} />
}

afterEach(cleanup)

describe('AC1: RolesView never renders an empty/loaded roster over an errored fetch', () => {
  it('renders the error surface, not the empty-roster card, when membersState is error', () => {
    render(<Harness membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} />)

    expect(screen.getByText('gateway is down')).toBeTruthy()
    expect(screen.queryByTestId('roles-grid')).toBeNull()
    expect(screen.queryByTestId('roles-empty')).toBeNull()
  })

  it('renders the loading surface, not a role card, when membersState is loading', () => {
    render(<Harness roles={[role()]} membersState="loading" />)

    expect(screen.queryByTestId('role-card')).toBeNull()
    expect(screen.queryByTestId('roles-grid')).toBeNull()
  })

  it('retry calls ctx.refetchMembers', () => {
    const refetch = vi.fn()
    render(<Harness membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} refetchMembers={refetch} />)

    fireEvent.click(screen.getByText('Retry'))
    expect(refetch).toHaveBeenCalledOnce()
  })

  it('the unassigned-roles amber notice never fires over an errored fetch, even with a genuinely unheld role', () => {
    // Zero holders would make the role "unassigned" under a landed roster too -- what this
    // test isolates is that an ERRORED fetch suppresses the notice regardless.
    render(<Harness roles={[role()]} membersState="error" membersError={new ApiError('http', 'gateway is down', 503)} />)

    expect(screen.queryByTestId('roles-unassigned')).toBeNull()
  })

  it('the same unheld role DOES raise the notice once the roster has actually landed', () => {
    render(<Harness roles={[role()]} membersState="ready" />)

    expect(screen.getByTestId('roles-unassigned')).toBeTruthy()
  })
})

describe('a null-email member in the role modal picker renders the shared em dash, not "null"', () => {
  it('the picker row shows the em dash label', () => {
    render(<Harness members={[member({ email: null })]} roles={[role()]} membersState="ready" />)

    fireEvent.click(screen.getByTestId('role-card-edit'))
    const picker = screen.getByTestId('role-modal-member')
    expect(within(picker).getByText('—')).toBeTruthy()
    expect(within(picker).queryByText(/null/i)).toBeNull()
  })
})
