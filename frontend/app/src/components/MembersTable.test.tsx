// @vitest-environment jsdom
//
// RED specs for APPR-04-06 AC2: an unlanded roles fetch must not read as "this member holds
// no roles". Per the story's lead decision, an unlanded fetch renders an EMPTY cell (no new
// copy minted) — distinguishable from a landed-empty roster, which keeps '—' (ABSENT_LABEL).
//
// Reachability note (see the QA report, not restated here): MembersTable's only production
// caller is MembersView, which — once AC-1 lands — only mounts this table when
// rolesSurface(...) resolves to 'roster', meaning ctx.rolesState is always 'ready' by then.
// This guard is therefore unreachable through the shipped app and is defense-in-depth only.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { MembersTable } from './MembersTable'

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

function ctxWith(over: Record<string, unknown> = {}): PlatformCtx {
  return {
    members: [member()],
    rolesState: 'ready',
    ...over,
  } as unknown as PlatformCtx
}

// The Workflow-roles cell is the third of five direct grid children on the row
// (Person, Access role, Workflow roles, Status, ⋯) — MembersTable.tsx's own COLS/HEADS
// order. No test-id exists on the cell itself (MembersTable.tsx is not edited here).
function roleCellOf(row: HTMLElement): HTMLElement {
  return row.children[2] as HTMLElement
}

afterEach(cleanup)

describe('APPR-04-06 AC2: an unlanded roles fetch must not claim "no roles"', () => {
  it('does not render the ABSENT_LABEL em-dash while ctx.rolesState is loading', () => {
    const m = member()
    render(
      <MembersTable
        ctx={ctxWith({ members: [m], rolesState: 'loading' })}
        rows={[m]}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const cell = roleCellOf(screen.getByTestId('member-row'))
    expect(cell.textContent, 'an unlanded roles fetch must render nothing, not the "no roles" em-dash').not.toBe('—')
  })

  it('renders an empty cell specifically -- no new copy is invented for the unlanded state', () => {
    const m = member()
    render(
      <MembersTable
        ctx={ctxWith({ members: [m], rolesState: 'loading' })}
        rows={[m]}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const cell = roleCellOf(screen.getByTestId('member-row'))
    expect(cell.textContent).toBe('')
  })

  it('still renders the em-dash once the roles fetch has genuinely landed empty', () => {
    // A real landed-empty fetch resolves to rolesState:'empty', not 'ready' with an empty
    // array (resolveStatus's default isEmpty, packages/api-client/src/async-state.ts:64-69)
    // — 'ready' never carries an empty list in practice.
    const m = member()
    render(
      <MembersTable
        ctx={ctxWith({ members: [m], rolesState: 'empty' })}
        rows={[m]}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const cell = roleCellOf(screen.getByTestId('member-row'))
    expect(cell.textContent, 'a genuinely empty, LANDED roster must still read as "no roles"').toBe('—')
  })
})

describe('APPR-04-06 QA: every unlanded rolesState reads the same as loading, not just "loading" itself', () => {
  it('idle renders the empty cell too', () => {
    const m = member()
    render(
      <MembersTable
        ctx={ctxWith({ members: [m], rolesState: 'idle' })}
        rows={[m]}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const cell = roleCellOf(screen.getByTestId('member-row'))
    expect(cell.textContent).toBe('')
  })

  it('error renders the empty cell too, not the em-dash', () => {
    const m = member()
    render(
      <MembersTable
        ctx={ctxWith({ members: [m], rolesState: 'error' })}
        rows={[m]}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const cell = roleCellOf(screen.getByTestId('member-row'))
    expect(cell.textContent).toBe('')
  })
})

// ============================================================================
// APPR-09-06 (task-510) — AC-2, pinned rather than changed
// ============================================================================
// The subtask's Steps named this file as a `ctx.policies` reader needing a status gate. Stage 1
// shrank that scope, and Stage 4 re-verified it at source: `stepsForMember` answers `null` at
// zero BY CONTRACT (lib/roles.ts:170-178), `blocked` collapses that to 0, and the strip is
// `{blocked > 0 && …}` (MembersTable.tsx:292). An unlanded policies fetch therefore renders
// NOTHING — a fail-safe omission, never the false claim RolesView and RoleModal carried.
// Nothing pinned that here, so a later edit to `stepsForMember` could un-fail-safe it with this
// file's suite still green. This is that pin.

describe('APPR-09-06 AC-2: an unlanded policies fetch renders no blocked-steps strip', () => {
  const CFO: Role = { key: 'cfo', title: 'CFO', desc: '', members: ['u9'] }
  const NAMING_CFO: Policy = {
    id: 'p1',
    name: 'Test policy',
    scope: 'All invoices',
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false }],
  }

  function renderTable(policies: Policy[]) {
    const m = member({ id: 'u9', name: 'Cy Person', initials: 'CP', status: 'suspended' })
    render(
      <MembersTable
        ctx={ctxWith({ members: [m], rolesState: 'ready' })}
        rows={[m]}
        policies={policies}
        roles={[CFO]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )
  }

  it('the strip renders once the fetch lands — the population floor under the absence below', () => {
    renderTable([NAMING_CFO])

    expect(screen.getByTestId('member-steps-warning').textContent, 'the landed strip never rendered').toContain(
      'Named in 1 approval step',
    )
  })

  it('a never-landed policies fetch renders no strip at all, not a zero-step one', () => {
    renderTable([])

    expect(screen.getByTestId('member-row'), 'the row did not render, so the absence below is vacuous').toBeTruthy()
    expect(screen.queryByTestId('member-steps-warning'), 'an unlanded policies fetch claimed a blocked-step count').toBeNull()
    expect(screen.queryByText(/Named in 0 approval steps/)).toBeNull()
  })
})
