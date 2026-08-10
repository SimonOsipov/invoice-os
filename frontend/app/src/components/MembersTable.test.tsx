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
    const m = member()
    render(
      <MembersTable
        ctx={ctxWith({ members: [m], rolesState: 'ready' })}
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
