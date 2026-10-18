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

import { invitedMember, MEMBER_UNBACKED, PROTECTED_ADMIN_NOTE, type Member, type PendingInvite } from '../lib/members'
import type { Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { InitialsChip } from './MemberParts'
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
    entities: [],
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

const rowOf = (name: string) => within(screen.getByText(name).closest('[data-testid="member-row"], [data-testid="invite-row"]') as HTMLElement)

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

    const chips = screen.getAllByTestId(/^(member|invite)-row$/).map((row) => row.querySelector('[aria-hidden="true"]') as HTMLElement)
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

  it('an invited row offers Resend and Revoke, both enabled, with no reasons', () => {
    renderRows(ROSTER())

    fireEvent.click(rowOf('Cy Invited').getByTestId('member-menu-trigger'))

    const menu = screen.getByTestId('member-menu')
    const items = within(menu).getAllByRole('button') as HTMLButtonElement[]
    expect(items.map((i) => i.textContent)).toEqual(['Resend invite', 'Revoke invite'])
    expect(items.map((i) => i.disabled)).toEqual([false, false])
    expect(within(menu).queryByTestId('member-menu-reason')).toBeNull()
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

describe('the room reserved under an open menu', () => {
  // Layout is not observable in jsdom, so the reserved padding is the handle. The plain menu
  // overhangs the card by 205px (Chromium); the account note adds about 35px.
  const scroller = () => screen.getByTestId('members-table').parentElement as HTMLElement
  const reservedAfterOpening = (row: HTMLElement) => {
    expect(scroller().style.paddingBottom).toBe('0px')
    fireEvent.click(within(row).getByTestId('member-menu-trigger'))
    const reserved = parseFloat(scroller().style.paddingBottom)
    fireEvent.click(within(row).getByTestId('member-menu-trigger'))
    expect(scroller().style.paddingBottom).toBe('0px')
    return reserved
  }

  it('reserves the plain overhang plus a small margin for a menu without a note', () => {
    renderRows(ROSTER())
    for (const name of ['Bo Active', 'Cy Invited']) {
      const reserved = reservedAfterOpening(screen.getByText(name).closest('[data-testid="member-row"], [data-testid="invite-row"]') as HTMLElement)
      expect(reserved, `${name}: less than the menu needs`).toBeGreaterThanOrEqual(205)
      expect(reserved, `${name}: over-reserving leaves a visible gap under the card`).toBeLessThanOrEqual(225)
    }
  })

  it('reserves the noted menu\'s overhang while a menu with an account note is open', () => {
    for (const account of ['unconfirmed', 'confirmed'] as const) {
      cleanup()
      renderPending({ invites: [{ ...INVITE, account }] })
      const reserved = reservedAfterOpening(screen.getByTestId('invite-row'))
      expect(reserved, `${account}: less than the menu with its note needs`).toBeGreaterThanOrEqual(240)
      expect(reserved, `${account}: over-reserving leaves a visible gap under the card`).toBeLessThanOrEqual(260)
    }
  })

  it('reserves the plain overhang for a pending row whose account state adds no note', () => {
    for (const account of ['none', 'unknown', undefined] as const) {
      cleanup()
      renderPending({ invites: [{ ...INVITE, account }] })
      expect(reservedAfterOpening(screen.getByTestId('invite-row')), String(account)).toBeLessThanOrEqual(225)
    }
  })

  it('releases the room when a filter removes the row whose menu was open', () => {
    const all = ROSTER()
    const { rerender } = render(
      <MembersTable ctx={ctxWith({ members: all })} rows={all} policies={[]} roles={[]} onOpen={vi.fn()} onStatus={vi.fn()} statusError={null} />,
    )
    fireEvent.click(rowOf('Cy Invited').getByTestId('member-menu-trigger'))
    expect(parseFloat(scroller().style.paddingBottom)).toBeGreaterThan(0)

    rerender(
      <MembersTable ctx={ctxWith({ members: all })} rows={all.slice(0, 2)} policies={[]} roles={[]} onOpen={vi.fn()} onStatus={vi.fn()} statusError={null} />,
    )

    expect(screen.queryByText('Cy Invited')).toBeNull()
    expect(scroller().style.paddingBottom).toBe('0px')
  })
})

describe('InitialsChip type scale and ring', () => {
  const chipAt = (size: number | undefined, extra: { fontSize?: number; ring?: boolean; status?: Member['status'] } = {}) => {
    cleanup()
    const { container } = render(<InitialsChip initials="AP" status={extra.status ?? 'active'} size={size} fontSize={extra.fontSize} ring={extra.ring} />)
    return container.firstElementChild as HTMLElement
  }

  it('the glyph is 9px up to 26, 10.5px between, 13px from 40; the roster default stays 10.5px', () => {
    const scale = [24, 26, 27, undefined, 39, 40, 44].map((size) => [size, chipAt(size).style.fontSize])
    expect(scale).toEqual([
      [24, '9px'],
      [26, '9px'],
      [27, '10.5px'],
      [undefined, '10.5px'],
      [39, '10.5px'],
      [40, '13px'],
      [44, '13px'],
    ])
  })

  it('an explicit fontSize outranks the scale', () => {
    expect(chipAt(26, { fontSize: 9.5 }).style.fontSize).toBe('9.5px')
    expect(chipAt(40, { fontSize: 9 }).style.fontSize).toBe('9px')
  })

  it('ring swaps the tone border for a 2px card-ground one on active and suspended, never on invited', () => {
    const plain = chipAt(26).style.border
    expect(plain, 'control: the plain chip keeps its tone border').toBe('1px solid transparent')
    expect(chipAt(26, { ring: true }).style.border).toBe('2px solid var(--bg-2)')
    expect(chipAt(26, { ring: true, status: 'suspended' }).style.border).toBe('2px solid var(--bg-2)')
    expect(chipAt(26, { ring: true, status: 'invited' }).style.border, 'invited stays dashed over its transparent ground').toBe('1px dashed var(--line-3)')
  })
})

// ============================================================================
// RESEND-07-04 — the pending row (D7, D9, D19, N5)
// ============================================================================

const INVITE: PendingInvite = {
  id: 'inv-1',
  email: 'zed@x.ng',
  role: 'reviewer',
  expiresAt: new Date(Date.now() + 7 * 86_400_000).toISOString(),
  delivery: 'sent',
}

function renderPending(over: { invites?: PendingInvite[]; onResend?: (id: string) => void; resending?: ReadonlySet<string>; onRevoke?: (id: string) => void; revoking?: ReadonlySet<string>; onOpen?: (id: string) => void; extra?: Member[] } = {}) {
  const pending = invitedMember(over.invites?.[0] ?? INVITE)
  const rows = [...(over.extra ?? []), pending]
  render(
    <MembersTable
      ctx={ctxWith({ members: rows })}
      rows={rows}
      policies={[]}
      roles={[]}
      onOpen={over.onOpen ?? vi.fn()}
      onStatus={vi.fn()}
      statusError={null}
      invites={over.invites ?? [INVITE]}
      onResend={over.onResend ?? vi.fn()}
      resending={over.resending}
      onRevoke={over.onRevoke}
      revoking={over.revoking}
    />,
  )
  return pending
}

function openMenuOf(row: HTMLElement): HTMLElement {
  fireEvent.click(within(row).getByTestId('member-menu-trigger'))
  return screen.getByTestId('member-menu')
}

describe('RESEND-07-04: the pending row status cell', () => {
  it('the expiry line sits under the INVITED pill, in a column', () => {
    renderPending()
    const cell = screen.getByTestId('invite-row').children[3] as HTMLElement
    expect(getComputedStyle(cell).flexDirection, 'pill and line stack, they do not sit side by side').toBe('column')
    const pill = within(cell).getByText('INVITED')
    const line = within(cell).getByText(/^Expires in/)
    expect(pill.compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

describe('RESEND-07-04: the pending row menu', () => {
  it("MembersTable: an invited row's menu is Resend invite and Revoke invite, both enabled, no reasons", () => {
    renderPending()
    const menu = openMenuOf(screen.getByTestId('invite-row'))

    const items = within(menu).getAllByRole('button') as HTMLButtonElement[]
    expect(items.map((i) => i.textContent)).toEqual(['Resend invite', 'Revoke invite'])
    expect(items.map((i) => i.disabled)).toEqual([false, false])
    expect(within(menu).queryByTestId('member-menu-reason')).toBeNull()
    expect(within(menu).queryByText('Copy invite link')).toBeNull()
  })

  it('MembersTable: Revoke invite opens the confirm strip under its row and calls nothing', () => {
    const onRevoke = vi.fn()
    renderPending({ onRevoke })
    const row = screen.getByTestId('invite-row')
    fireEvent.click(within(openMenuOf(row)).getByRole('button', { name: 'Revoke invite' }))

    const strip = screen.getByTestId('invite-revoke-confirm')
    expect(row.nextElementSibling).toBe(strip)
    expect(strip.textContent).toContain('Revoke this invite? The link in the email stops working at once.')
    expect(within(strip).getByTestId('invite-revoke-keep').textContent).toBe('Keep invite')
    expect(within(strip).getByTestId('invite-revoke-confirmed').textContent).toBe('Revoke invite')
    expect(strip.style.minWidth).toBe('716px')
    expect(onRevoke).not.toHaveBeenCalled()
  })

  it('MembersTable: Keep invite closes the strip and calls nothing', () => {
    const onRevoke = vi.fn()
    renderPending({ onRevoke })
    fireEvent.click(within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Revoke invite' }))

    fireEvent.click(screen.getByTestId('invite-revoke-keep'))

    expect(screen.queryByTestId('invite-revoke-confirm')).toBeNull()
    expect(screen.getByTestId('invite-row')).toBeTruthy()
    expect(onRevoke).not.toHaveBeenCalled()
  })

  it('MembersTable: opening the strip focuses Keep invite', () => {
    renderPending({ onRevoke: vi.fn() })
    fireEvent.click(within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Revoke invite' }))

    expect(document.activeElement).toBe(screen.getByTestId('invite-revoke-keep'))
  })

  it('MembersTable: Escape closes the strip and calls nothing', () => {
    const onRevoke = vi.fn()
    renderPending({ onRevoke })
    fireEvent.click(within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Revoke invite' }))

    fireEvent.keyDown(window, { key: 'Escape' })

    expect(screen.queryByTestId('invite-revoke-confirm')).toBeNull()
    expect(onRevoke).not.toHaveBeenCalled()
  })

  it('MembersTable: a strip opened for a row that leaves the list does not return with the row', () => {
    const inv = invitedMember(INVITE)
    const base = { ctx: ctxWith({ members: [inv] }), policies: [], roles: [], onOpen: vi.fn(), onStatus: vi.fn(), statusError: null, invites: [INVITE], onResend: vi.fn(), onRevoke: vi.fn() }
    const { rerender } = render(<MembersTable {...base} rows={[inv]} />)
    fireEvent.click(within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Revoke invite' }))
    expect(screen.getByTestId('invite-revoke-confirm')).toBeTruthy()

    rerender(<MembersTable {...base} rows={[]} />)
    rerender(<MembersTable {...base} rows={[inv]} />)

    expect(screen.getByTestId('invite-row')).toBeTruthy()
    expect(screen.queryByTestId('invite-revoke-confirm')).toBeNull()
  })

  it('MembersTable: the strip\'s Revoke invite calls onRevoke with the id and closes the strip', () => {
    const onRevoke = vi.fn()
    renderPending({ onRevoke })
    fireEvent.click(within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Revoke invite' }))

    fireEvent.click(screen.getByTestId('invite-revoke-confirmed'))

    expect(onRevoke).toHaveBeenCalledExactlyOnceWith('inv-1')
    expect(screen.queryByTestId('invite-revoke-confirm')).toBeNull()
  })

  it('MembersTable: Revoke is disabled while that id revokes or resends, and Resend while it revokes', () => {
    renderPending({ revoking: new Set(['inv-1']) })
    let items = within(openMenuOf(screen.getByTestId('invite-row'))).getAllByRole('button') as HTMLButtonElement[]
    expect(items.map((i) => i.disabled)).toEqual([true, true])
    cleanup()
    renderPending({ resending: new Set(['inv-1']) })
    items = within(openMenuOf(screen.getByTestId('invite-row'))).getAllByRole('button') as HTMLButtonElement[]
    expect(items.map((i) => i.disabled)).toEqual([true, true])
  })

  it("MembersTable: an open strip's Revoke invite is disabled while that id resends or revokes", () => {
    const onRevoke = vi.fn()
    const base = { ctx: ctxWith({ members: [invitedMember(INVITE)] }), rows: [invitedMember(INVITE)], policies: [], roles: [], onOpen: vi.fn(), onStatus: vi.fn(), statusError: null, invites: [INVITE], onResend: vi.fn(), onRevoke }
    const { rerender } = render(<MembersTable {...base} />)
    fireEvent.click(within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Revoke invite' }))
    const confirm = () => screen.getByTestId('invite-revoke-confirmed') as HTMLButtonElement
    expect(confirm().disabled, 'control: enabled when nothing is in flight').toBe(false)

    rerender(<MembersTable {...base} resending={new Set(['inv-1'])} />)
    expect(confirm().disabled).toBe(true)
    fireEvent.click(confirm())
    rerender(<MembersTable {...base} revoking={new Set(['inv-1'])} />)
    expect(confirm().disabled).toBe(true)
    fireEvent.click(confirm())
    expect(onRevoke).not.toHaveBeenCalled()
  })

  it('MembersTable: Resend calls onResend with the invitation id, and is disabled while that id is resending', () => {
    const onResend = vi.fn()
    renderPending({ onResend })
    fireEvent.click(within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Resend invite' }))
    expect(onResend).toHaveBeenCalledTimes(1)
    expect(onResend).toHaveBeenCalledWith('inv-1')

    cleanup()
    const again = vi.fn()
    renderPending({ onResend: again, resending: new Set(['inv-1']) })
    const resend = within(openMenuOf(screen.getByTestId('invite-row'))).getByRole('button', { name: 'Resend invite' }) as HTMLButtonElement
    expect(resend.disabled).toBe(true)
    fireEvent.click(resend)
    expect(again).not.toHaveBeenCalled()
  })

  it("MembersTable: a pending row's click does not open the drawer", () => {
    const onOpen = vi.fn()
    const ada = member({ id: 'u2', name: 'Ada Person' })
    renderPending({ onOpen, extra: [ada] })

    // Pair: a membership row still opens it, so the silence below is the pending row's.
    fireEvent.click(screen.getByTestId('member-row'))
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen).toHaveBeenCalledWith('u2')

    fireEvent.click(screen.getByTestId('invite-row'))
    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('MembersTable: the scroll container holds the table and pads only while a menu is open', () => {
    renderPending()
    const scroll = screen.getByTestId('members-table-scroll')
    expect(scroll.contains(screen.getByTestId('members-table')), 'L2 reads the menu against this container').toBe(true)
    const closed = scroll.style.paddingBottom

    openMenuOf(screen.getByTestId('invite-row'))
    expect(scroll.style.paddingBottom, 'an open menu gets clearance the closed table does not').not.toBe(closed)
  })

  it('MembersTable: an invited membership without an invitation shows the pill only', () => {
    const lone = member({ id: 'u9', name: 'Lone Invitee', email: 'lone@x.ng', status: 'invited' })
    render(
      <MembersTable
        ctx={ctxWith({ members: [lone] })}
        rows={[lone]}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
        invites={[]}
        onResend={vi.fn()}
      />,
    )

    const row = screen.getByTestId('invite-row')
    expect(within(row).getByText('INVITED')).toBeTruthy()
    expect(within(row).queryByText(/Expires/)).toBeNull()
    expect(within(row).queryByText('Email not sent')).toBeNull()

    let menu!: HTMLElement
    expect(() => {
      menu = openMenuOf(row)
    }).not.toThrow()
    expect(within(menu).getByRole('button', { name: 'Resend invite' })).toBeTruthy()
  })
})

describe('LOGFIX-05-03: the invitee account state on the pending row', () => {
  const withState = (over: Partial<PendingInvite>) => renderPending({ invites: [{ ...INVITE, ...over }] })
  const stateOf = () => screen.getByTestId('invite-account-state')
  const FOLLOWS = Node.DOCUMENT_POSITION_FOLLOWING

  it('MembersTable: the status cell names an unconfirmed and a confirmed invitee', () => {
    for (const [account, label] of [
      ['unconfirmed', 'Account created, email not confirmed'],
      ['confirmed', 'Confirmed, not joined'],
    ] as const) {
      cleanup()
      withState({ account })
      const state = stateOf()
      expect(state.textContent).toBe(label)
      expect(within(screen.getByTestId('invite-row')).getByText('INVITED').compareDocumentPosition(state) & FOLLOWS, 'pill precedes').toBeTruthy()
      expect(state.compareDocumentPosition(screen.getByText(/^Expires in/)) & FOLLOWS, 'expiry line follows').toBeTruthy()
    }
  })

  it('MembersTable: none, unknown and absent keep today\'s cell', () => {
    for (const account of ['none', 'unknown', undefined] as const) {
      cleanup()
      withState({ account })
      expect(screen.queryByTestId('invite-account-state'), String(account)).toBeNull()
      expect(screen.getByText('INVITED')).toBeTruthy()
      expect(screen.getByText(/^Expires in/)).toBeTruthy()
    }
  })

  it('MembersTable: an expired or unsent invite still names the state', () => {
    withState({ account: 'confirmed', expiresAt: new Date(Date.now() - 86_400_000).toISOString() })
    expect(stateOf().textContent).toBe('Confirmed, not joined')
    expect(stateOf().compareDocumentPosition(screen.getByText('Expired')) & FOLLOWS, 'state line first').toBeTruthy()

    cleanup()
    withState({ account: 'unconfirmed', delivery: 'failed' })
    expect(stateOf().textContent).toBe('Account created, email not confirmed')
    expect(stateOf().compareDocumentPosition(screen.getByText('Email not sent')) & FOLLOWS, 'state line first').toBeTruthy()
  })

  it("MembersTable: the state line matches the expiry line's type", () => {
    withState({ account: 'unconfirmed' })
    const state = getComputedStyle(stateOf())
    const expiry = getComputedStyle(screen.getByText(/^Expires in/))
    expect(state.fontSize).toBe(expiry.fontSize)
    expect(state.color).toBe(expiry.color)
    expect(state.fontSize, 'control: the size is set, not empty').toBe('11px')
  })

  it('MembersTable: the pending menu shows the state above its items', () => {
    for (const [account, label] of [
      ['unconfirmed', 'Account created, email not confirmed'],
      ['confirmed', 'Confirmed, not joined'],
    ] as const) {
      cleanup()
      withState({ account })
      const menu = openMenuOf(screen.getByTestId('invite-row'))
      const note = within(menu).getByTestId('member-menu-state')
      expect(note.textContent).toBe(label)
      expect(note.compareDocumentPosition(within(menu).getByRole('button', { name: 'Resend invite' })) & FOLLOWS).toBeTruthy()
    }
  })

  it('MembersTable: no state note for none, unknown or a member row', () => {
    for (const account of ['none', 'unknown', undefined] as const) {
      cleanup()
      withState({ account })
      expect(within(openMenuOf(screen.getByTestId('invite-row'))).queryByTestId('member-menu-state'), String(account)).toBeNull()
    }
    cleanup()
    renderPending({ extra: [member({ id: 'u2', name: 'Ada Person' })] })
    expect(within(openMenuOf(screen.getByTestId('member-row'))).queryByTestId('member-menu-state')).toBeNull()
  })

  it("MembersTable: a confirmed-account pending row's menu is Resend invite and Revoke invite, both enabled, no reasons", () => {
    withState({ account: 'confirmed' })
    const menu = openMenuOf(screen.getByTestId('invite-row'))
    const items = within(menu).getAllByRole('button') as HTMLButtonElement[]
    expect(items.map((i) => i.textContent)).toEqual(['Resend invite', 'Revoke invite'])
    expect(items.map((i) => i.disabled)).toEqual([false, false])
    expect(within(menu).queryByTestId('member-menu-reason')).toBeNull()
    expect(within(menu).queryByText('Copy invite link')).toBeNull()
  })
})

describe('FIRMS-02-05: the roster shows client access under the access role', () => {
  function renderRows(mode: 'firm' | 'inhouse', rows: Member[], invites: PendingInvite[] = [], entities: unknown[] = []) {
    render(
      <MembersTable
        ctx={ctxWith({ mode, members: rows, entities })}
        rows={rows}
        policies={[]}
        roles={[]}
        onOpen={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
        invites={invites}
      />,
    )
  }

  it('each firm member row shows its client access under the role; an invited row shows none', () => {
    const pending: PendingInvite = { id: 'i1', email: 'new@x.ng', role: 'preparer', expiresAt: '2099-01-01T00:00:00Z', delivery: 'sent' }
    renderRows(
      'firm',
      [
        member({ id: 'a', name: 'Adm', role: 'admin', clientAccess: 'all' }),
        member({ id: 'p', name: 'Prep', role: 'preparer', clientAccess: [] }),
        member({ id: 'r', name: 'Rev', role: 'reviewer', clientAccess: ['a', 'b'] }),
        invitedMember(pending),
      ],
      [pending],
    )

    const rows = screen.getAllByTestId('member-row')
    expect(rows.map((r) => within(r).getByTestId('member-client-access').textContent)).toEqual(['All clients', 'No clients', '2 clients'])
    for (const row of rows) {
      const cell = row.children[1] as HTMLElement
      const line = within(row).getByTestId('member-client-access')
      expect(cell.contains(line)).toBe(true)
      expect(cell.textContent?.endsWith(line.textContent as string)).toBe(true)
    }
    expect(within(screen.getByTestId('invite-row')).queryByTestId('member-client-access')).toBeNull()
  })

  it('in-house rows show no client access line', () => {
    renderRows('inhouse', [member({ clientAccess: null }), member({ id: 'u9', clientAccess: 'all' })])
    expect(screen.queryByTestId('member-client-access')).toBeNull()
  })

  it('a row whose access is unknown to the viewer shows no line', () => {
    renderRows('firm', [member({ clientAccess: null })])
    expect(screen.queryByTestId('member-client-access')).toBeNull()
  })

  it('a client set gives the line a title naming the clients in list order; unresolvable ids are omitted', () => {
    const ent = (id: string, name: string, status = 'active') => ({ id, name, status })
    renderRows(
      'firm',
      [
        member({ id: 'r', clientAccess: ['z', 'b', 'a'] }),
        member({ id: 'g', clientAccess: ['ghost'] }),
        member({ id: 'm', clientAccess: ['a', 'ghost'] }),
      ],
      [],
      [ent('a', 'Acme'), ent('b', 'Beta', 'archived'), ent('z', 'Zed')],
    )
    const titles = screen.getAllByTestId('member-client-access').map((l) => l.getAttribute('title'))
    expect(titles).toEqual(['Acme, Beta, Zed', null, 'Acme'])
  })

  it('"All clients" and "No clients" carry no title', () => {
    renderRows('firm', [member({ id: 'a', clientAccess: 'all' }), member({ id: 'p', clientAccess: [] })], [], [
      { id: 'a', name: 'Acme', status: 'active' },
    ])
    for (const l of screen.getAllByTestId('member-client-access')) expect(l.hasAttribute('title')).toBe(false)
  })
})
