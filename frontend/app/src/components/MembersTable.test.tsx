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
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { MEMBER_UNBACKED, PROTECTED_ADMIN_NOTE, type Member } from '../lib/members'
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

function renderRows(rows: Member[], over: Record<string, unknown> = {}) {
  render(
    <MembersTable
      ctx={ctxWith({ members: rows, ...over })}
      rows={rows}
      policies={[]}
      roles={[]}
      onOpen={vi.fn()}
      onStatus={vi.fn()}
      statusError={null}
    />,
  )
}

const ROSTER = (): Member[] => [
  member({ id: 'me', name: 'Me Admin', initials: 'MA', role: 'admin', isYou: true }),
  member({ id: 'u2', name: 'Bo Active', initials: 'BA' }),
  member({ id: 'u3', name: 'Cy Invited', initials: 'CI', status: 'invited' }),
  member({ id: 'u4', name: 'Di Suspended', initials: 'DS', status: 'suspended' }),
]

const rowOf = (name: string) => within(screen.getByText(name).closest('[data-testid="member-row"]') as HTMLElement)

describe('the row atoms', () => {
  it('each status pill is a childless radius-4 mono 8.5/600 label', () => {
    renderRows(ROSTER())

    const pills = screen.getAllByText(/^(ACTIVE|INVITED|SUSPENDED)$/)
    expect(pills.map((p) => p.textContent)).toEqual(['ACTIVE', 'ACTIVE', 'INVITED', 'SUSPENDED'])
    for (const pill of pills) {
      expect(pill.childElementCount, `${pill.textContent} pill drew a dot or other child`).toBe(0)
      expect(pill.className).toContain('mono')
      expect(pill.style.borderRadius).toBe('4px')
      expect(pill.style.fontSize).toBe('8.5px')
      expect(pill.style.fontWeight).toBe('600')
    }
  })

  it('the YOU chip is a bare YOU span, radius 4, outlined in --action', () => {
    renderRows(ROSTER())

    const chips = screen.getAllByText('YOU', { exact: true })
    expect(chips).toHaveLength(1)
    expect(chips[0].style.borderRadius).toBe('4px')
    expect(chips[0].style.border).toBe('1px solid var(--action)')
    expect(rowOf('Me Admin').getByText('YOU', { exact: true })).toBe(chips[0])
  })

  it('every initials chip is a circle, whatever the status', () => {
    renderRows(ROSTER())

    const chips = screen.getAllByTestId('member-row').map((row) => row.querySelector('[aria-hidden="true"]') as HTMLElement)
    expect(chips).toHaveLength(4)
    for (const chip of chips) expect(chip.style.borderRadius).toBe('50%')
  })

  it('the menu trigger draws the prototype 15px / 2.6 dots', () => {
    renderRows(ROSTER())

    const svg = screen.getAllByTestId('member-menu-trigger')[0].querySelector('svg')!
    expect(svg.getAttribute('width')).toBe('15')
    expect(svg.getAttribute('stroke-width')).toBe('2.6')
  })

  it('the menu trigger keeps its resting paint while its menu is open', () => {
    renderRows(ROSTER())

    const trigger = screen.getAllByTestId('member-menu-trigger')[1]
    fireEvent.click(trigger)

    expect(screen.getByTestId('member-menu')).toBeTruthy()
    expect(trigger.style.background).toBe('transparent')
    expect(trigger.style.color).toBe('var(--fg-3)')
  })
})

describe('the row menu', () => {
  it('is a 280px card on the card shadow whose items pad 8px 12px at weight 400', () => {
    renderRows(ROSTER())

    fireEvent.click(rowOf('Bo Active').getByTestId('member-menu-trigger'))

    const menu = screen.getByTestId('member-menu')
    expect(menu.style.width).toBe('280px')
    expect(menu.style.boxShadow).toBe('var(--shadow-card)')
    expect(menu.style.top).toBe('calc(100% + 4px)')
    const edit = within(menu).getByRole('button', { name: 'Edit' })
    expect(edit.style.padding).toBe('8px 12px')
    expect(edit.style.fontWeight).toBe('400')
    expect(edit.style.opacity, 'an enabled item must not carry the disabled paint').toBe('')
  })

  it('a disabled item keeps its paint, dims to .45, shows not-allowed, and points at its reason', () => {
    renderRows(ROSTER())

    fireEvent.click(rowOf('Bo Active').getByTestId('member-menu-trigger'))

    const menu = screen.getByTestId('member-menu')
    const remove = within(menu).getByRole('button', { name: 'Remove' }) as HTMLButtonElement
    expect(remove.disabled).toBe(true)
    expect(remove.style.opacity).toBe('0.45')
    expect(remove.style.cursor).toBe('not-allowed')
    expect(remove.style.filter).toBe('none')
    expect(remove.style.color, 'the enabled paint (red) must survive the dim').toBe('var(--status-red-text)')
    const reason = within(menu).getByTestId('member-menu-reason')
    expect(reason.textContent).toBe(MEMBER_UNBACKED.remove)
    expect(remove.getAttribute('aria-describedby')).toBe(reason.id)
    expect(reason.style.fontSize).toBe('11px')
  })

  it('an invited row offers three disabled items and states both distinct reasons', () => {
    renderRows(ROSTER())

    fireEvent.click(rowOf('Cy Invited').getByTestId('member-menu-trigger'))

    const menu = screen.getByTestId('member-menu')
    const items = within(menu).getAllByRole('button') as HTMLButtonElement[]
    expect(items.map((i) => i.textContent)).toEqual(['Resend invite', 'Copy invite link', 'Revoke invite'])
    for (const item of items) expect(item.disabled, `${item.textContent} must be disabled`).toBe(true)
    expect(within(menu).getAllByTestId('member-menu-reason').map((r) => r.textContent)).toEqual([MEMBER_UNBACKED.invite, MEMBER_UNBACKED.remove])
  })

  it('your own menu has Edit and a locked Suspend, and no Remove at all', () => {
    renderRows(ROSTER())

    fireEvent.click(rowOf('Me Admin').getByTestId('member-menu-trigger'))

    const menu = screen.getByTestId('member-menu')
    expect(within(menu).queryByRole('button', { name: 'Remove' })).toBeNull()
    expect((within(menu).getByRole('button', { name: 'Suspend' }) as HTMLButtonElement).disabled).toBe(true)
    expect(within(menu).getByTestId('member-menu-reason').textContent).toBe(PROTECTED_ADMIN_NOTE)
  })

  it('opening a second row menu closes the first', () => {
    renderRows(ROSTER())

    fireEvent.click(rowOf('Bo Active').getByTestId('member-menu-trigger'))
    fireEvent.click(rowOf('Di Suspended').getByTestId('member-menu-trigger'))

    expect(screen.getAllByTestId('member-menu')).toHaveLength(1)
    expect(within(screen.getByTestId('member-menu')).getByRole('button', { name: 'Reactivate' })).toBeTruthy()
  })
})

describe('the status-error strip', () => {
  it('renders once, directly under the row it belongs to', () => {
    const rows = ROSTER()
    render(
      <MembersTable
        ctx={ctxWith({ members: rows })}
        rows={rows}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={{ id: 'u2', message: 'refused' }}
      />,
    )

    const strips = screen.getAllByTestId('member-status-error')
    expect(strips).toHaveLength(1)
    expect(strips[0].textContent).toBe('refused')
    expect(strips[0].previousElementSibling).toBe(screen.getByText('Bo Active').closest('[data-testid="member-row"]'))
  })
})
