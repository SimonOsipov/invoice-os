// @vitest-environment jsdom
//
// RED specs for APPR-04-06 AC3/AC4: `toggleWorkflowRole` currently fires ctx.staffRole and
// forgets about it -- no await, no .catch, no in-flight guard. Every spec below fails on its
// own target assertion (a call count that is still 2, a sentence that never renders, a pill
// that a second click still reaches) -- never on import/typecheck.
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import {
  ADMIN_ALL_CLIENTS,
  CLIENTS_UNREADABLE,
  MEMBER_ADMIN_ONLY,
  MEMBER_CLIENTS_ADMIN_ONLY,
  PROTECTED_ADMIN_NOTE,
  removeConfirmQuestion,
  roleChangeQuestion,
  ROLE_ADMIN_ONLY,
  SELF_DEMOTION_WARNING,
  type Member,
} from '../lib/members'
import { approverGapNotice, APPROVERS_UNCHECKED, drawerRoleHelper, stepsNamedLine, SUSPENDED_STEPS_NOTE, type Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { MemberDrawer } from './MemberDrawer'

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

const adminViewer = () => member({ id: 'me', name: 'Me Admin', role: 'admin', isYou: true })

function role(over: Partial<Role> = {}): Role {
  return { key: 'cfo', title: 'CFO', desc: '', members: [], ...over }
}

function entity(id: string, name: string, status = 'active') {
  return { id, name, status, tin: null, registration: null, sector: null, address: null, created_at: '' }
}

function drawerCtx(over: Record<string, unknown> = {}): PlatformCtx {
  return {
    members: [member(), member({ id: 'u2', name: 'Bo Person', initials: 'BP', email: 'bo@x.ng' })],
    roles: [],
    rolesState: 'ready',
    policies: [],
    policiesState: 'ready',
    policiesError: null,
    refetchPolicies: vi.fn(),
    publishPolicy: vi.fn(),
    mode: 'firm',
    staffRole: vi.fn().mockResolvedValue(undefined),
    renameRole: vi.fn(),
    setSettingsTab: vi.fn(),
    entities: [entity('e1', 'Alpha Ltd'), entity('e2', 'Beta Ltd')],
    entitiesState: 'ready',
    entitiesError: null,
    refetchEntities: vi.fn(),
    setMemberClients: vi.fn().mockResolvedValue(undefined),
    ...over,
  } as unknown as PlatformCtx
}

/** RoleModal.test.tsx's own drain: attaches a .catch to the mock's OWN promise before the
 * test's assertions run, so a deliberately rejected write never surfaces as vitest's global
 * unhandled-rejection failure on top of the real assertion below. The extra tick matches
 * React 19 committing a setState made from a promise continuation one macrotask late. */
async function drain(fn: ReturnType<typeof vi.fn>) {
  await fn.mock.results[0]?.value?.catch(() => {})
  await new Promise((resolve) => setTimeout(resolve, 0))
}

afterEach(cleanup)

describe('APPR-04-06 AC3: a rejected workflow-role write renders the server sentence, not a false toggle', () => {
  it("renders the server's own sentence beside the pills and leaves the pill unset", async () => {
    const REASON = 'only an admin can change workflow roles'
    const staffRole = vi.fn().mockRejectedValue(new ApiError('http', REASON, 403))
    render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [role({ key: 'cfo', members: [] })], staffRole })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const pill = screen.getByTestId('drawer-wfrole-cfo')
    expect(pill.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(pill)
    await drain(staffRole)

    expect(screen.getByText(REASON), "the server's own 403 sentence must reach the drawer verbatim").toBeTruthy()
    expect(pill.getAttribute('aria-pressed'), 'a rejected write must never render as if the toggle succeeded').toBe('false')
  })

  it('sends only the members axis -- staffRole once, renameRole never', async () => {
    const staffRole = vi.fn().mockResolvedValue(undefined)
    const renameRole = vi.fn()
    render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [role({ key: 'cfo', members: ['u1'] })], staffRole, renameRole })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    fireEvent.click(screen.getByTestId('drawer-wfrole-cfo'))
    await drain(staffRole)

    expect(staffRole).toHaveBeenCalledTimes(1)
    expect(staffRole).toHaveBeenCalledWith('cfo', ['u1', 'u2'])
    expect(renameRole, 'the staffing write must never touch the rename verb').not.toHaveBeenCalled()
  })

  it('pills are inert while a staffing write is in flight -- two clicks, exactly one call', () => {
    const staffRole = vi.fn(() => new Promise(() => {})) // never settles
    render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [role({ key: 'cfo', members: [] })], staffRole })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const pill = screen.getByTestId('drawer-wfrole-cfo')
    fireEvent.click(pill)
    fireEvent.click(pill)

    expect(staffRole, 'a second click before the first write settles must be a no-op').toHaveBeenCalledTimes(1)
    // jsdom does not propagate a <fieldset disabled>'s state to descendant IDL `disabled`
    // properties, so the click-count assertion above is the real oracle. The fieldset's OWN
    // `disabled` attribute is still a valid, jsdom-safe check for the visual lock.
    const fieldset = pill.closest('fieldset')
    expect(fieldset?.hasAttribute('disabled'), 'the pills must sit inside a disabled fieldset while a write is in flight').toBe(true)
  })
})

describe('APPR-04-06 AC4: drawer copy this subtask does not own stays byte-unchanged', () => {
  it('drawerRoleHelper, SUSPENDED_STEPS_NOTE and stepsNamedLine still render verbatim', () => {
    const policy: Policy = {
      id: 'p1',
      name: 'Test policy',
      scope: 'All invoices',
      status: 'draft',
      version: 1,
      activeVersion: null,
      nodes: [{ id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false }],
    }
    render(
      <MemberDrawer
        ctx={drawerCtx({
          roles: [role({ key: 'cfo', members: ['u2'] })],
          policies: [policy],
          members: [member({ id: 'u2', name: 'Bo Person', initials: 'BP', status: 'suspended' })],
        })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    expect(screen.getByTestId('drawer-wfrole-helper').textContent).toBe(drawerRoleHelper('preparer'))
    expect(screen.getByText(SUSPENDED_STEPS_NOTE)).toBeTruthy()
    expect(screen.getByTestId('member-steps-named').textContent).toBe(stepsNamedLine(1))
  })
})

describe('APPR-04-06 QA: a rejected toggle followed by a successful retry', () => {
  it('the stale rejection sentence clears on the retry click, and the second write goes through', async () => {
    const REASON = 'only an admin can change workflow roles'
    const staffRole = vi
      .fn()
      .mockRejectedValueOnce(new ApiError('http', REASON, 403))
      .mockResolvedValueOnce({ key: 'cfo', title: 'CFO', desc: '', members: ['u1', 'u2'] })
    render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [role({ key: 'cfo', members: ['u1'] })], staffRole })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const pill = screen.getByTestId('drawer-wfrole-cfo')
    fireEvent.click(pill)
    await drain(staffRole)
    expect(screen.getByText(REASON)).toBeTruthy()

    fireEvent.click(pill) // the retry
    expect(screen.queryByText(REASON), 'setRoleError(null) fires synchronously on the next click').toBeNull()

    await waitFor(() => expect(pill.closest('fieldset')?.hasAttribute('disabled')).toBe(false))
    expect(staffRole).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(REASON), 'a successful retry must not leave the earlier rejection on screen').toBeNull()
  })
})

describe('APPR-04-06 QA: the in-flight lock is global, not scoped to the clicked pill', () => {
  it('a different pill clicked while one write is pending is also a no-op', () => {
    const staffRole = vi.fn(() => new Promise(() => {})) // never settles
    render(
      <MemberDrawer
        ctx={drawerCtx({
          roles: [role({ key: 'cfo', members: [] }), role({ key: 'coo', title: 'COO', members: [] })],
          staffRole,
        })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    fireEvent.click(screen.getByTestId('drawer-wfrole-cfo'))
    fireEvent.click(screen.getByTestId('drawer-wfrole-coo'))

    expect(staffRole, 'the pending flag covers every pill, not just the one that started the write').toHaveBeenCalledTimes(1)
    expect(staffRole).toHaveBeenCalledWith('cfo', expect.anything())
  })
})

describe('RALPH fix (appr-04-06): the drawer read side must not report zero roles over an unlanded fetch', () => {
  it('shows the shared Loading affordance, not an empty pill row, while ctx.rolesState is loading', () => {
    const policy: Policy = {
      id: 'p1',
      name: 'Test policy',
      scope: 'All invoices',
      status: 'draft',
      version: 1,
      activeVersion: null,
      nodes: [{ id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false }],
    }
    render(
      <MemberDrawer
        // roles:[] is the real shape App.tsx leaves ctx.roles in mid-refetch (setRoles(data ?? [])) --
        // not a stand-in for a landed-empty roster.
        ctx={drawerCtx({ roles: [], policies: [policy], rolesState: 'loading' })}
        memberId="u1"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    expect(screen.queryByTestId('drawer-wfrole-cfo'), 'an unlanded fetch must render no pill row, not an unset one').toBeNull()
    expect(screen.queryByTestId('member-steps-named'), 'the involvement line must not silently read as zero').toBeNull()
    expect(screen.getByText('Loading roles…'), 'the loading affordance must stand in for the suppressed claim').toBeTruthy()
  })

  it('shows the shared ErrorState, not an empty pill row, while ctx.rolesState is error', () => {
    render(
      <MemberDrawer
        ctx={drawerCtx({
          roles: [],
          rolesState: 'error',
          rolesError: new ApiError('http', 'roles gateway is down', 503),
          refetchRoles: vi.fn(),
        })}
        memberId="u1"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    expect(screen.queryByTestId('drawer-wfrole-cfo')).toBeNull()
    expect(screen.getByText('roles gateway is down'), "the server's own error must reach the drawer, not a silent blank").toBeTruthy()
  })

  it('a genuinely landed-empty roles fetch (rolesState "empty") renders the normal roleless drawer, not Loading', () => {
    render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [], rolesState: 'empty' })}
        memberId="u1"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    expect(screen.queryByText('Loading roles…'), "'empty' is a landed answer, not an unlanded one").toBeNull()
    expect(screen.getByTestId('drawer-wfrole-helper'), 'the rest of the drawer renders exactly as it does today').toBeTruthy()
  })

  it('a retry that lands recovers the pills -- the ErrorState swap is not sticky', () => {
    const roles = [role({ key: 'cfo', members: ['u1'] })]
    const { rerender } = render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [], rolesState: 'error', rolesError: new ApiError('http', 'down', 503) })}
        memberId="u1"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )
    expect(screen.queryByTestId('drawer-wfrole-cfo')).toBeNull()

    // The drawer stays mounted across the whole refetch lifecycle -- App.tsx never remounts
    // it on a status change, so a fresh ctx arrives as a rerender, not a fresh mount.
    rerender(
      <MemberDrawer
        ctx={drawerCtx({ roles, rolesState: 'ready' })}
        memberId="u1"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    const pill = screen.getByTestId('drawer-wfrole-cfo')
    expect(pill.getAttribute('aria-pressed'), 'the real held role must render once the retry lands, not stay stuck on the error swap').toBe('true')
  })
})

// ============================================================================
// APPR-09-06 (task-510) — AC-2, pinned rather than changed
// ============================================================================
// ALREADY GREEN on write, deliberately. AC-2 asks for "the same loading affordance" over an
// unlanded policies fetch, but its premise is false: `stepsForMember` answers `null` at zero BY
// CONTRACT (lib/roles.ts:170-173 — "THE GATE IS THE COUNT: holding a role a policy never names
// answers `null`, not a section reading 'Named in 0 approval steps' above an empty list"), and
// MemberDrawer.tsx's `steps &&` gate renders `{steps && …}`. An unlanded fetch renders NOTHING, which makes no
// false claim. An affordance here would contradict a shipped design note, so this pins the
// contract instead: the gate stays the null, never a count re-derived off `ctx.policies`.

describe('APPR-09-06 AC-2: an unlanded policies fetch renders no steps line at all', () => {
  it('the drawer renders no steps line, not a zero-step line, while policies are unlanded', () => {
    render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [role({ key: 'cfo', members: ['u2'] })], policies: [], policiesState: 'loading' })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    // Population floor: the panel that WOULD carry the steps line did render. The landed
    // counterpart — the same drawer printing its real count — is the spec at line 159 above.
    expect(screen.getByTestId('drawer-wfrole-helper').textContent).toBe(drawerRoleHelper('preparer'))
    expect(screen.queryByTestId('member-steps-named'), 'an unlanded policies fetch rendered a step count').toBeNull()
    expect(screen.queryByText(stepsNamedLine(0))).toBeNull()
  })

  // QA (Stage 4). The DELIBERATE DIVERGENCE from RolesView and RoleModal, pinned so it reads
  // as a decision rather than an oversight. Those two withhold their sentence whenever the
  // status is unlanded, INCLUDING a refetch that still holds the last landed rows — reachable
  // via createPolicy/deletePolicy, which dispatch 'start' while the mirror keeps its rows
  // (App.tsx:299-301,:1037,:1046). This drawer does not, and does not need to: its gate is
  // `stepsForMember`'s null-at-zero, so it can only ever be one round trip STALE, never
  // negatively wrong. Withholding a true count to avoid a claim it never makes would blank a
  // shipped section for nothing.
  it('a refetch that still holds its rows keeps printing the count — the gate is the null, not the status', () => {
    const policy: Policy = {
      id: 'p1',
      name: 'Test policy',
      scope: 'All invoices',
      status: 'draft',
      version: 1,
      activeVersion: null,
      nodes: [{ id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false }],
    }
    render(
      <MemberDrawer
        ctx={drawerCtx({ roles: [role({ key: 'cfo', members: ['u2'] })], policies: [policy], policiesState: 'loading' })}
        memberId="u2"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={null}
      />,
    )

    expect(screen.getByTestId('member-steps-named').textContent).toBe(stepsNamedLine(1))
  })
})

describe('the drawer status-error strip', () => {
  it('renders the server sentence in the red status triplet, as a top-bordered strip', () => {
    render(
      <MemberDrawer
        ctx={drawerCtx()}
        memberId="u1"
        onClose={vi.fn()}
        onStatus={vi.fn()}
        statusError={{ id: 'u1', message: 'the last active admin cannot be suspended' }}
      />,
    )

    const strip = screen.getByTestId('member-drawer-status-error')
    expect(strip.textContent).toBe('the last active admin cannot be suspended')
    expect(strip.style.background).toBe('var(--status-red-bg)')
    expect(strip.style.borderTop).toBe('1px solid var(--status-red-border)')
    expect(strip.style.color).toBe('var(--status-red-text)')
    expect(strip.style.fontSize).toBe('11.5px')
  })
})

describe('the drawer shell and bands', () => {
  function open(over: Record<string, unknown> = {}, id = 'u2', onClose = vi.fn()) {
    render(<MemberDrawer ctx={drawerCtx(over)} memberId={id} onClose={onClose} onStatus={vi.fn()} statusError={null} />)
    return onClose
  }

  it('the panel is 560px with a border and no shadow, behind a closing scrim', () => {
    const onClose = open()

    const panel = screen.getByTestId('member-drawer')
    expect(panel.getAttribute('role')).toBe('dialog')
    expect(panel.getAttribute('aria-modal')).toBe('true')
    expect(panel.style.width).toBe('560px')
    expect(panel.style.background).toBe('var(--bg-1)')
    expect(panel.style.borderLeft).toBe('1px solid var(--line-2)')
    expect(panel.style.boxShadow, 'the drawer carries a border, not a shadow').toBe('')

    const scrim = panel.previousElementSibling as HTMLElement
    expect(scrim.style.background).toContain('var(--surface)')
    expect(scrim.style.background).toContain('55%')
    fireEvent.click(scrim)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('the bands pad 18/24, the avatar is a 40px circle and Close is a 30px grey square', () => {
    open()

    const panel = screen.getByTestId('member-drawer')
    const [header, body] = Array.from(panel.children) as HTMLElement[]
    expect(header.style.padding).toBe('18px 24px 16px')
    expect(header.style.borderBottom).toBe('1px solid var(--line-1)')
    const avatar = header.querySelector('[aria-hidden="true"]') as HTMLElement
    expect(avatar.style.width).toBe('40px')
    expect(avatar.style.borderRadius).toBe('50%')
    expect(avatar.style.fontSize).toBe('13px')
    const close = screen.getByTestId('member-drawer-close')
    expect(close.style.width).toBe('30px')
    expect(close.style.height).toBe('30px')
    expect(close.style.background).toBe('var(--bg-3)')
    expect(body.style.padding).toBe('18px 24px 26px')
    const footer = screen.getByTestId('member-danger-zone')
    expect(footer.style.padding).toBe('14px 24px 16px')
    expect(footer.style.borderTop).toBe('1px solid var(--line-1)')
  })

  it('the three access-role cards show the real role, are all disabled for a non-admin viewer, and dim to .45', () => {
    const setMemberRole = vi.fn()
    open({ setMemberRole })

    const cards = ['admin', 'preparer', 'reviewer'].map((id) => screen.getByTestId(`drawer-role-${id}`))
    expect(cards).toHaveLength(3)
    const radios = cards.map((c) => c.querySelector('input') as HTMLInputElement)
    expect(radios.map((r) => r.checked)).toEqual([false, true, false])
    for (const [i, card] of cards.entries()) {
      expect(radios[i].disabled).toBe(true)
      expect(card.style.opacity).toBe('0.45')
      expect(card.style.cursor).toBe('not-allowed')
    }
    expect(cards[1].style.border).toBe('1px solid var(--action)')
    expect(cards[0].style.border).toBe('1px solid var(--line-2)')
    expect(screen.getByTestId('member-drawer').textContent).toContain(ROLE_ADMIN_ONLY)
    fireEvent.click(radios[2])
    expect(setMemberRole).not.toHaveBeenCalled()
  })

  it('the access-role radios are painted by the design, not by the browser default', () => {
    open()

    const radios = ['admin', 'preparer', 'reviewer'].map((id) => screen.getByTestId(`drawer-role-${id}`).querySelector('input') as HTMLInputElement)
    expect(radios.map((r) => r.checked)).toEqual([false, true, false])
    for (const radio of radios) {
      expect(radio.style.appearance).toBe('none')
      expect(radio.style.width).toBe('15px')
      expect(radio.style.height).toBe('15px')
    }
    expect(radios[1].style.border).toBe('1.5px solid var(--action)')
    expect(radios[1].style.background).toContain('radial-gradient(circle, var(--action)')
    expect(radios[0].style.border).toBe('1.5px solid var(--line-3)')
    expect(radios[0].style.background).toBe('transparent')
  })

  it('the Client access scope radios share that paint and sit flush in their cards', () => {
    open({ mode: 'firm', members: [member({ id: 'u2', clientAccess: 'all' })] })

    const all = screen.getByTestId('drawer-scope-all').querySelector('input') as HTMLInputElement
    const selected = screen.getByTestId('drawer-scope-selected').querySelector('input') as HTMLInputElement
    expect([all.checked, selected.checked]).toEqual([true, false])
    for (const radio of [all, selected]) {
      expect(radio.style.appearance).toBe('none')
      expect(radio.style.width).toBe('15px')
      expect(radio.style.marginLeft, 'the browser default 5px margin pushes the ring off the card padding').toBe('0px')
      expect(radio.style.marginRight).toBe('0px')
      expect(radio.style.marginTop).toBe('0px')
    }
    expect(all.style.border).toBe('1.5px solid var(--action)')
    expect(selected.style.border).toBe('1.5px solid var(--line-3)')
  })

  it('the workflow pills are 32px with the held one tinted', () => {
    open({ roles: [role({ key: 'cfo', title: 'CFO', members: ['u2'] }), role({ key: 'tax', title: 'Tax', members: [] })] })

    const held = screen.getByTestId('drawer-wfrole-cfo')
    const idle = screen.getByTestId('drawer-wfrole-tax')
    expect(held.getAttribute('aria-pressed')).toBe('true')
    expect(idle.getAttribute('aria-pressed')).toBe('false')
    for (const pill of [held, idle]) {
      expect(pill.className).toBe('pf-btn')
      expect(pill.style.height).toBe('32px')
      expect(pill.style.padding).toBe('0px 13px')
      expect(pill.style.fontSize).toBe('12.5px')
      expect(pill.style.fontWeight).toBe('500')
    }
    expect(held.style.background).toBe('var(--action-tint)')
    expect(held.style.color).toBe('var(--action)')
    expect(idle.style.background).toBe('var(--bg-2)')
  })

  it('an admin member dims the Client access pair', () => {
    open({ mode: 'firm', members: [member({ id: 'u2', role: 'admin', clientAccess: 'all' })] })

    const drawer = screen.getByTestId('member-drawer')
    const dims: number[] = []
    for (let el: HTMLElement | null = screen.getByTestId('drawer-scope-all'); el && el !== drawer; el = el.parentElement) {
      if (el.style.opacity) dims.push(Number(el.style.opacity))
    }
    expect(dims.length, 'nothing above the Client access pair dims it').toBeGreaterThan(0)
    expect(Math.min(...dims)).toBeGreaterThanOrEqual(0.45)
    expect(Math.max(...dims)).toBeLessThanOrEqual(0.7)
    const dimmed = screen.getByTestId('drawer-scope-all').closest('fieldset') as HTMLFieldSetElement
    expect(dimmed.style.opacity).toBe('0.45')
    expect(dimmed.style.cursor).toBe('not-allowed')
    expect(dimmed.style.filter).toBe('none')
    expect(dimmed.disabled).toBe(true)
    const noteId = dimmed.getAttribute('aria-describedby')
    expect(noteId, 'the fieldset lost layer 4').toBeTruthy()
    expect(document.getElementById(noteId!)?.textContent).toBe(ADMIN_ALL_CLIENTS)
    for (const input of dimmed.querySelectorAll('input')) {
      expect(input.disabled, 'the input carries its own disabled; the lock is the fieldset ancestor').toBe(false)
      expect(input.closest('fieldset[disabled]')).toBe(dimmed)
    }
  })

  it.each(['firm', 'inhouse'] as const)('the %s drawer renders no Department control', (mode) => {
    open({ mode })

    expect(screen.queryByRole('combobox', { name: 'Department' })).toBeNull()
    expect(screen.queryByLabelText('Department')).toBeNull()
    expect(screen.getByTestId('member-drawer').textContent).not.toMatch(/department/i)
    expect(screen.getByTestId('drawer-manage-roles')).toBeTruthy()
    expect(screen.getByTestId('drawer-role-admin')).toBeTruthy()
    expect(screen.getByText('Workflow roles')).toBeTruthy()
    expect(screen.getByTestId('member-danger-zone')).toBeTruthy()
  })

  it('a status error for another member renders no strip here', () => {
    render(
      <MemberDrawer ctx={drawerCtx()} memberId="u1" onClose={vi.fn()} onStatus={vi.fn()} statusError={{ id: 'u2', message: 'refused' }} />,
    )

    expect(screen.getByTestId('member-danger-zone'), 'the footer is the floor under the absence').toBeTruthy()
    expect(screen.queryByTestId('member-drawer-status-error')).toBeNull()
  })

  it('drawer: Remove is a red ghost, enabled, and your own drawer has none', () => {
    open({ members: [adminViewer(), member({ id: 'u2' })] })

    const remove = screen.getByTestId('member-remove') as HTMLButtonElement
    expect(remove.disabled).toBe(false)
    expect(remove.style.opacity).toBe('')
    expect(remove.style.color).toBe('var(--status-red-text)')

    cleanup()
    open({ members: [member({ id: 'u1', isYou: true }), member({ id: 'u2' })] }, 'u1')
    expect(screen.getByTestId('member-danger-zone')).toBeTruthy()
    expect(screen.queryByTestId('member-remove')).toBeNull()
  })
})

describe('FIRMS-02-05: the drawer edits client access', () => {
  const REFUSED = "only an admin can choose a member's clients"

  function ticks() {
    return screen.queryAllByTestId('drawer-client-row').map((r) => (r.querySelector('input') as HTMLInputElement).checked)
  }
  function tick(i: number) {
    fireEvent.click(screen.getAllByTestId('drawer-client-row')[i].querySelector('input') as HTMLInputElement)
  }
  function radio(scope: 'all' | 'selected') {
    return screen.getByTestId(`drawer-scope-${scope}`).querySelector('input') as HTMLInputElement
  }
  function mount(setMemberClients: unknown, access: 'all' | string[] = ['e1'], over: Record<string, unknown> = {}) {
    const ctx = drawerCtx({ members: [adminViewer(), member({ id: 'u2', clientAccess: access })], setMemberClients, ...over })
    const view = render(<MemberDrawer ctx={ctx} memberId="u2" onClose={vi.fn()} onStatus={vi.fn()} statusError={null} />)
    return { ctx, view }
  }

  it("a firm member's picker is live and each change writes once", async () => {
    const setMemberClients = vi.fn().mockResolvedValue(undefined)
    mount(setMemberClients)

    expect(radio('selected').checked).toBe(true)
    expect(ticks()).toEqual([true, false])
    expect(screen.getByTestId('drawer-scope-all').closest('fieldset')?.hasAttribute('disabled')).toBe(false)
    expect(screen.queryByRole('button', { name: /save/i })).toBeNull()

    tick(1)
    await waitFor(() => expect(setMemberClients).toHaveBeenCalledTimes(1))
    expect(setMemberClients).toHaveBeenCalledWith('u2', ['e1', 'e2'])

    fireEvent.click(radio('all'))
    await waitFor(() => expect(setMemberClients).toHaveBeenCalledTimes(2))
    expect(setMemberClients).toHaveBeenLastCalledWith('u2', 'all')
  })

  it('All clears the ticks, and Selected from All writes no clients', async () => {
    const setMemberClients = vi.fn()
    // The ctx member takes the server's answer before the write resolves, as App does.
    function Harness({ initial }: { initial: 'all' | string[] }) {
      const [access, setAccess] = useState(initial)
      setMemberClients.mockImplementation(async (_id: string, next: 'all' | string[]) => setAccess(next))
      const ctx = drawerCtx({ members: [adminViewer(), member({ id: 'u2', clientAccess: access })], setMemberClients })
      return <MemberDrawer ctx={ctx} memberId="u2" onClose={vi.fn()} onStatus={vi.fn()} statusError={null} />
    }
    render(<Harness initial={['e1']} />)

    fireEvent.click(radio('all'))
    await waitFor(() => expect(setMemberClients).toHaveBeenCalledWith('u2', 'all'))
    await waitFor(() => expect(screen.getByTestId('drawer-scope-all').closest('fieldset')?.hasAttribute('disabled')).toBe(false))
    fireEvent.click(radio('selected'))
    await waitFor(() => expect(setMemberClients).toHaveBeenLastCalledWith('u2', []))
    await waitFor(() => expect(radio('selected').checked).toBe(true))
    expect(ticks()).toEqual([false, false])

    cleanup()
    mount(vi.fn(), [])
    expect(radio('selected').checked).toBe(true)
    expect(ticks()).toEqual([false, false])
  })

  it('a second change while a write is in flight sends nothing', async () => {
    let release: () => void = () => {}
    const setMemberClients = vi.fn(() => new Promise<void>((resolve) => { release = resolve }))
    mount(setMemberClients, [])

    tick(1)
    tick(0)
    expect(setMemberClients).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('drawer-scope-all').closest('fieldset')?.hasAttribute('disabled')).toBe(true)

    release()
    await waitFor(() => expect(screen.getByTestId('drawer-scope-all').closest('fieldset')?.hasAttribute('disabled')).toBe(false))
    tick(0)
    expect(setMemberClients).toHaveBeenCalledTimes(2)
  })

  it('a change dropped while a write is in flight remounts the picker on the stored set', async () => {
    const entities = [entity('e1', 'Alpha Ltd'), entity('e2', 'Beta Ltd'), entity('e3', 'Gamma Ltd')]
    let release: () => void = () => {}
    const setMemberClients = vi.fn()
    function Harness() {
      const [access, setAccess] = useState<'all' | string[]>([])
      setMemberClients.mockImplementation(
        (_id: string, next: 'all' | string[]) => new Promise<void>((resolve) => { release = () => { setAccess(next); resolve() } }),
      )
      const ctx = drawerCtx({ members: [adminViewer(), member({ id: 'u2', clientAccess: access })], setMemberClients, entities })
      return <MemberDrawer ctx={ctx} memberId="u2" onClose={vi.fn()} onStatus={vi.fn()} statusError={null} />
    }
    render(<Harness />)

    tick(1)
    tick(0)
    expect(setMemberClients).toHaveBeenCalledTimes(1)
    release()
    await waitFor(() => expect(screen.getByTestId('drawer-scope-all').closest('fieldset')?.hasAttribute('disabled')).toBe(false))
    expect(ticks()).toEqual([false, true, false])

    tick(2)
    expect(setMemberClients).toHaveBeenLastCalledWith('u2', ['e2', 'e3'])
  })

  it("a refused write shows the server's reason and restores the stored set", async () => {
    const setMemberClients = vi.fn().mockRejectedValueOnce(new ApiError('http', REFUSED, 403)).mockResolvedValue(undefined)
    mount(setMemberClients)

    tick(1)
    await waitFor(() => expect(screen.getByTestId('member-drawer-clients-error').textContent).toBe(REFUSED))
    expect(ticks()).toEqual([true, false])

    tick(1)
    expect(screen.queryByTestId('member-drawer-clients-error')).toBeNull()
  })

  it('an admin row shows All clients locked with the reason', () => {
    const setMemberClients = vi.fn()
    mount(setMemberClients, 'all', { members: [member({ id: 'u2', role: 'admin', clientAccess: 'all' })] })

    expect(radio('all').checked).toBe(true)
    const fieldset = screen.getByTestId('drawer-scope-all').closest('fieldset') as HTMLFieldSetElement
    expect(fieldset.hasAttribute('disabled')).toBe(true)
    expect(fieldset.style.opacity).toBe('0.45')
    expect(screen.getByTestId('member-drawer').textContent).toContain('An admin sees every client.')
    fireEvent.click(radio('selected'))
    expect(setMemberClients).not.toHaveBeenCalled()
  })

  it('clients loading, failed and empty', () => {
    mount(vi.fn(), ['e1'], { entities: [], entitiesState: 'loading' })
    expect(screen.getByText('Loading clients…')).toBeTruthy()
    expect(screen.queryByTestId('drawer-scope-all')).toBeNull()
    cleanup()

    const refetchEntities = vi.fn()
    mount(vi.fn(), ['e1'], { entities: [], entitiesState: 'error', entitiesError: new ApiError('http', 'clients down', 500), refetchEntities })
    expect(screen.getByText(/clients down/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /retry|try again/i }))
    expect(refetchEntities).toHaveBeenCalledTimes(1)
    cleanup()

    mount(vi.fn(), [], { entities: [], entitiesState: 'empty' })
    expect(screen.getByTestId('drawer-client-empty').textContent).toBe('This workspace has no clients yet.')
  })

  it('a member whose access the viewer cannot read gets no picker that claims a set', () => {
    const setMemberClients = vi.fn()
    mount(setMemberClients, [], { members: [member({ id: 'u2', clientAccess: null })] })

    expect(screen.getByText('Client access')).toBeTruthy()
    expect(screen.queryByTestId('drawer-scope-selected')).toBeNull()
    expect(screen.queryAllByTestId('drawer-client-row')).toHaveLength(0)
    expect(setMemberClients).not.toHaveBeenCalled()
  })

  it('an unreadable access set shows the admin-only line', () => {
    mount(vi.fn(), [], { members: [member({ id: 'u2', clientAccess: null })] })
    expect(screen.getByTestId('member-drawer-clients-unreadable').textContent).toBe("Only an admin sees a member's clients.")
  })

  it("the picker's search narrows the shown rows only and the count reads against the roster", () => {
    const setMemberClients = vi.fn()
    mount(setMemberClients, ['e1'], { entities: [entity('e1', 'Alpha Ltd'), entity('e2', 'Beta Ltd'), entity('e3', 'Gamma Ltd')] })
    const search = screen.getByTestId('drawer-client-search') as HTMLInputElement
    const count = () => screen.getByTestId('drawer-client-count').textContent

    expect(screen.getAllByTestId('drawer-client-row')).toHaveLength(3)
    expect(count()).toBe('1 of 3 selected')

    fireEvent.change(search, { target: { value: 'beta' } })
    expect(screen.getAllByTestId('drawer-client-row').map((r) => r.textContent)).toEqual(['Beta Ltd'])
    expect(count()).toBe('1 of 3 selected')

    fireEvent.change(search, { target: { value: 'zzz' } })
    expect(screen.queryAllByTestId('drawer-client-row')).toHaveLength(0)
    expect(screen.getByTestId('drawer-client-empty').textContent).toBe('No clients match this search.')

    fireEvent.change(search, { target: { value: '' } })
    expect(ticks()).toEqual([true, false, false])
    expect(setMemberClients).not.toHaveBeenCalled()
  })

  it('lists an archived client the member already holds, ticked', () => {
    mount(vi.fn(), ['e3'], { entities: [entity('e1', 'Alpha Ltd'), entity('e3', 'Old Ltd', 'archived'), entity('e4', 'Gone Ltd', 'archived')] })
    expect(screen.getAllByTestId('drawer-client-row').map((r) => r.textContent)).toEqual(['Alpha Ltd', 'Old Ltd'])
    expect(ticks()).toEqual([false, true])
  })
})

describe('DEL-02-05: Remove in the drawer', () => {
  function mount(over: Record<string, unknown> = {}, id = 'u2', onRemove = vi.fn()) {
    const members = [adminViewer(), member({ id: 'u1' }), member({ id: 'u2', name: 'Bo Person', initials: 'BP', email: 'bo@x.ng' })]
    render(<MemberDrawer ctx={drawerCtx({ members, ...over })} memberId={id} onClose={vi.fn()} onStatus={vi.fn()} onRemove={onRemove} statusError={null} />)
    return onRemove
  }

  it('drawer: Remove opens the named confirm and Keep sends nothing', () => {
    const onRemove = mount()

    fireEvent.click(screen.getByTestId('member-remove'))
    expect(screen.getByTestId('member-remove-confirm').textContent).toContain(removeConfirmQuestion('Bo Person'))
    expect(document.activeElement).toBe(screen.getByTestId('member-remove-keep'))

    fireEvent.click(screen.getByTestId('member-remove-keep'))
    expect(screen.queryByTestId('member-remove-confirm')).toBeNull()
    expect(screen.getByTestId('member-remove')).toBeTruthy()
    expect(onRemove).not.toHaveBeenCalled()
  })

  it('drawer: the confirm click removes once', () => {
    const onRemove = mount()

    fireEvent.click(screen.getByTestId('member-remove'))
    const confirm = screen.getByTestId('member-remove-confirmed')
    fireEvent.click(confirm)
    fireEvent.click(confirm)

    expect(onRemove).toHaveBeenCalledTimes(1)
    expect(onRemove).toHaveBeenCalledWith('u2')
  })

  it("drawer: a non-admin viewer's Remove next to the sole active admin is disabled with the admin-only note", () => {
    const onRemove = mount({ members: [member({ id: 'me', isYou: true }), member({ id: 'u2', name: 'Bo Admin', role: 'admin' })] })

    const remove = screen.getByTestId('member-remove') as HTMLButtonElement
    expect(remove.disabled).toBe(true)
    expect(remove.title).toBe(MEMBER_ADMIN_ONLY)
    expect(screen.getByTestId('member-remove-note').textContent).toBe(MEMBER_ADMIN_ONLY)
    fireEvent.click(remove)
    expect(screen.queryByTestId('member-remove-confirm')).toBeNull()
    expect(onRemove).not.toHaveBeenCalled()
  })

  it('drawer: your own row has no Remove', () => {
    mount({ members: [member({ id: 'u1', isYou: true }), member({ id: 'u2' })] }, 'u1')

    expect(screen.getByTestId('member-danger-zone')).toBeTruthy()
    expect(screen.queryByTestId('member-remove')).toBeNull()
  })

  it('drawer: the involvement block stays beside the open strip', () => {
    const policy = (id: string) =>
      ({ id, name: `Policy ${id}`, nodes: [{ id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false }] }) as unknown as Policy
    mount({
      roles: [role({ key: 'cfo', members: ['u2'] })],
      policies: [policy('p1'), policy('p2')],
    })

    fireEvent.click(screen.getByTestId('member-remove'))
    expect(screen.getByTestId('member-remove-confirm')).toBeTruthy()
    expect(screen.getByTestId('member-steps-named').textContent).toBe(stepsNamedLine(2))
  })
})

describe('ACCESS-01: the drawer saves the access role', () => {
  const me = () => member({ id: 'me', name: 'Me Admin', role: 'admin', isYou: true })
  const other = () => member({ id: 'a2', name: 'Other Admin', role: 'admin' })
  const u2 = (over: Partial<Member> = {}) => member({ id: 'u2', name: 'Bo Person', role: 'preparer', ...over })
  const REFUSED = "only an admin can change a member's role"

  function mount(over: Record<string, unknown> = {}, id = 'u2') {
    const setMemberRole = (over.setMemberRole as ReturnType<typeof vi.fn>) ?? vi.fn().mockResolvedValue(undefined)
    const ctx = drawerCtx({ members: [me(), other(), u2()], ...over, setMemberRole })
    render(<MemberDrawer ctx={ctx} memberId={id} onClose={vi.fn()} onStatus={vi.fn()} statusError={null} />)
    return setMemberRole
  }
  const card = (id: string) => screen.getByTestId(`drawer-role-${id}`).querySelector('input') as HTMLInputElement
  const pick = (id: string) => fireEvent.click(card(id))
  const strip = () => screen.queryByTestId('member-role-confirm')
  const text = (id: string) => screen.getByTestId(id).textContent

  it("ACCESS-01: an admin viewer's role cards are live and a click writes once", async () => {
    const setMemberRole = mount()

    expect(['admin', 'preparer', 'reviewer'].map((id) => card(id).disabled)).toEqual([false, false, false])
    pick('reviewer')
    await waitFor(() => expect(setMemberRole).toHaveBeenCalledTimes(1))
    expect(setMemberRole).toHaveBeenCalledWith('u2', 'reviewer')
    expect(strip()).toBeNull()
  })

  it('ACCESS-01: clicking the current role sends nothing', () => {
    const setMemberRole = mount()

    pick('preparer')
    expect(setMemberRole).not.toHaveBeenCalled()
    expect(strip()).toBeNull()
  })

  it('ACCESS-01: a second click while a role write is in flight sends nothing', () => {
    const setMemberRole = mount({ setMemberRole: vi.fn(() => new Promise(() => {})) })

    pick('reviewer')
    pick('admin')
    expect(setMemberRole).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('drawer-role-admin').closest('fieldset')?.hasAttribute('disabled')).toBe(true)
  })

  it('ACCESS-01: a refused role write shows the server sentence and keeps the stored role', async () => {
    mount({ setMemberRole: vi.fn().mockRejectedValue(new ApiError('http', REFUSED, 403)) })

    pick('reviewer')
    await waitFor(() => expect(text('member-drawer-access-error')).toBe(REFUSED))
    expect(card('preparer').checked).toBe(true)
    expect(card('reviewer').checked).toBe(false)
  })

  it("ACCESS-01: the sole active admin's cards are locked with the protected note", () => {
    mount({ members: [me(), u2({ status: 'suspended' })] }, 'me')

    expect(['admin', 'preparer', 'reviewer'].map((id) => card(id).disabled)).toEqual([true, true, true])
    const describedBy = card('admin').getAttribute('aria-describedby') as string
    expect(document.getElementById(describedBy)?.textContent).toBe(PROTECTED_ADMIN_NOTE)
  })

  it('ACCESS-01: an unlanded roles fetch leaves the cards live and warns that approvers are unchecked', async () => {
    for (const rolesState of ['loading', 'error']) {
      cleanup()
      const setMemberRole = mount({ rolesState, roles: [], members: [me(), other(), u2({ role: 'reviewer' })] })

      expect(['admin', 'preparer', 'reviewer'].map((id) => card(id).disabled)).toEqual([false, false, false])
      pick('preparer')
      expect(text('member-role-gaps')).toBe(APPROVERS_UNCHECKED)
      expect(setMemberRole).not.toHaveBeenCalled()
      fireEvent.click(screen.getByTestId('member-role-confirmed'))
      await waitFor(() => expect(setMemberRole).toHaveBeenCalledTimes(1))
      expect(setMemberRole).toHaveBeenCalledWith('u2', 'preparer')
      await waitFor(() => expect(strip()).toBeNull())

      pick('admin')
      expect(strip()).toBeNull()
      await waitFor(() => expect(setMemberRole).toHaveBeenCalledTimes(2))
      expect(setMemberRole).toHaveBeenLastCalledWith('u2', 'admin')
    }
  })

  it('ACCESS-01: a confirmed self-demotion locks the viewer out of the cards', async () => {
    const setMemberRole = vi.fn()
    function Harness() {
      const [mine, setMine] = useState<Member['role']>('admin')
      setMemberRole.mockImplementation(async (_id: string, next: Member['role']) => setMine(next))
      const ctx = drawerCtx({ members: [{ ...me(), role: mine }, other()], setMemberRole })
      return <MemberDrawer ctx={ctx} memberId="me" onClose={vi.fn()} onStatus={vi.fn()} statusError={null} />
    }
    render(<Harness />)

    pick('preparer')
    fireEvent.click(screen.getByTestId('member-role-confirmed'))
    await waitFor(() => expect(strip()).toBeNull())
    expect(setMemberRole).toHaveBeenCalledWith('me', 'preparer')
    expect(screen.queryByTestId('member-drawer-access-error')).toBeNull()
    expect(['admin', 'preparer', 'reviewer'].map((id) => card(id).disabled)).toEqual([true, true, true])
    expect(screen.getByTestId('member-drawer').textContent).toContain(ROLE_ADMIN_ONLY)
  })

  it('ACCESS-01: a refused write from the confirm strip shows the server sentence and clears the strip', async () => {
    const setMemberRole = vi.fn().mockRejectedValue(new ApiError('http', 'membership not found', 404))
    mount({ setMemberRole, roles: [role({ key: 'cfo', title: 'CFO', members: ['u2'] })], members: [me(), other(), u2({ role: 'reviewer' })] })

    pick('preparer')
    fireEvent.click(screen.getByTestId('member-role-confirmed'))
    await waitFor(() => expect(text('member-drawer-access-error')).toBe('membership not found'))
    expect(card('reviewer').checked).toBe(true)
    expect(strip()).toBeNull()

    pick('preparer')
    expect(text('member-role-gaps')).toBe(approverGapNotice('Bo Person', ['CFO']))
    expect(screen.getByTestId('member-role-confirmed').classList.contains('v2-btn-primary')).toBe(true)
  })

  it('ACCESS-01: a refused role write then a successful one clears the error', async () => {
    const setMemberRole = vi.fn().mockRejectedValueOnce(new ApiError('http', REFUSED, 403)).mockResolvedValue(undefined)
    mount({ setMemberRole })

    pick('reviewer')
    await waitFor(() => expect(text('member-drawer-access-error')).toBe(REFUSED))
    pick('admin')
    await waitFor(() => expect(setMemberRole).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByTestId('member-drawer-access-error')).toBeNull())
  })

  it('ACCESS-01: an admin demoting themselves is warned first', async () => {
    const setMemberRole = mount({}, 'me')

    pick('preparer')
    expect(text('member-role-confirm')).toContain(roleChangeQuestion('Me Admin', 'Preparer', true))
    expect(text('member-role-self-warning')).toBe(SELF_DEMOTION_WARNING)
    expect(document.activeElement).toBe(screen.getByTestId('member-role-keep'))
    expect(setMemberRole).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId('member-role-keep'))
    expect(strip()).toBeNull()
    expect(setMemberRole).not.toHaveBeenCalled()
    expect(card('admin').checked).toBe(true)
    expect(card('preparer').checked).toBe(false)

    pick('preparer')
    fireEvent.click(screen.getByTestId('member-role-confirmed'))
    await waitFor(() => expect(setMemberRole).toHaveBeenCalledTimes(1))
    expect(setMemberRole).toHaveBeenCalledWith('me', 'preparer')
  })

  it('ACCESS-01: demoting the only approver of a role names the role first', async () => {
    const setMemberRole = mount({ roles: [role({ key: 'cfo', title: 'CFO', members: ['u2'] })], members: [me(), other(), u2({ role: 'reviewer' })] })

    pick('preparer')
    expect(text('member-role-gaps')).toBe(approverGapNotice('Bo Person', ['CFO']))
    expect(setMemberRole).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('member-role-confirmed'))
    await waitFor(() => expect(setMemberRole).toHaveBeenCalledTimes(1))
  })

  it('ACCESS-01: reviewer to admin opens no strip', async () => {
    const setMemberRole = mount({ roles: [role({ key: 'cfo', title: 'CFO', members: ['u2'] })], members: [me(), other(), u2({ role: 'reviewer' })] })

    pick('admin')
    expect(strip()).toBeNull()
    await waitFor(() => expect(setMemberRole).toHaveBeenCalledTimes(1))
  })
})

describe('MEMB-03: the drawer locks member actions for a non-admin viewer', () => {
  const viewer = () => member({ id: 'me', name: 'Me Preparer', isYou: true })
  const target = (over: Partial<Member> = {}) => member({ id: 'u2', name: 'Bo Person', ...over })
  function mount(members: Member[], id = 'u2', over: Record<string, unknown> = {}) {
    const onRemove = vi.fn()
    const onStatus = vi.fn()
    render(<MemberDrawer ctx={drawerCtx({ members, ...over })} memberId={id} onClose={vi.fn()} onStatus={onStatus} onRemove={onRemove} statusError={null} />)
    return { onRemove, onStatus }
  }
  const describedText = (btn: HTMLElement) => document.getElementById(btn.getAttribute('aria-describedby') as string)?.textContent

  it("a non-admin viewer: Suspend is disabled with the admin-only note", () => {
    const { onStatus } = mount([viewer(), target()])

    const suspend = screen.getByTestId('member-suspend') as HTMLButtonElement
    expect(suspend.disabled).toBe(true)
    expect(suspend.title).toBe(MEMBER_ADMIN_ONLY)
    expect(screen.getByTestId('member-danger-note').textContent).toBe(MEMBER_ADMIN_ONLY)
    expect(describedText(suspend)).toBe(MEMBER_ADMIN_ONLY)
    fireEvent.click(suspend)
    expect(onStatus).not.toHaveBeenCalled()
  })

  it('a non-admin viewer: Remove is disabled with its own note', () => {
    const { onRemove } = mount([viewer(), target()])

    const remove = screen.getByTestId('member-remove') as HTMLButtonElement
    expect(remove.disabled).toBe(true)
    expect(screen.getByTestId('member-remove-note').textContent).toBe(MEMBER_ADMIN_ONLY)
    expect(describedText(remove)).toBe(MEMBER_ADMIN_ONLY)
    fireEvent.click(remove)
    expect(screen.queryByTestId('member-remove-confirm')).toBeNull()
    expect(onRemove).not.toHaveBeenCalled()
  })

  it('a non-admin viewer: Reactivate is disabled', () => {
    mount([viewer(), target({ status: 'suspended' })])

    const btn = screen.getByTestId('member-suspend') as HTMLButtonElement
    expect(btn.textContent).toBe('Reactivate')
    expect(btn.disabled).toBe(true)
    expect(screen.getByTestId('member-danger-note').textContent).toBe(MEMBER_ADMIN_ONLY)
  })

  it('a non-admin viewer on an invited row: Remove note is still present', () => {
    mount([viewer(), target({ status: 'invited' })])

    expect(screen.queryByTestId('member-suspend')).toBeNull()
    expect(describedText(screen.getByTestId('member-remove'))).toBe(MEMBER_ADMIN_ONLY)
    expect(screen.getByTestId('member-remove-note').textContent).toBe(MEMBER_ADMIN_ONLY)
  })

  it("a non-admin viewer's own row: Suspend is disabled and Remove is absent", () => {
    mount([viewer()], 'me')

    expect((screen.getByTestId('member-suspend') as HTMLButtonElement).disabled).toBe(true)
    expect(screen.queryByTestId('member-remove')).toBeNull()
  })

  it('a non-admin viewer on their own row: the client picker is locked', () => {
    const setMemberClients = vi.fn()
    mount([member({ id: 'me', isYou: true, clientAccess: ['e1'] })], 'me', { setMemberClients })

    const fieldset = screen.getByTestId('drawer-scope-all').closest('fieldset') as HTMLFieldSetElement
    expect(fieldset.hasAttribute('disabled')).toBe(true)
    expect(document.getElementById(fieldset.getAttribute('aria-describedby') as string)?.textContent).toBe(MEMBER_CLIENTS_ADMIN_ONLY)
    expect(screen.getAllByTestId('drawer-client-row').map((r) => (r.querySelector('input') as HTMLInputElement).checked)).toEqual([true, false])
    fireEvent.click(screen.getAllByTestId('drawer-client-row')[1].querySelector('input') as HTMLInputElement)
    expect(setMemberClients).not.toHaveBeenCalled()
  })

  it('a non-admin viewer on another member: still the unreadable note', () => {
    mount([viewer(), target({ clientAccess: null })])

    expect(screen.getByTestId('member-drawer-clients-unreadable').textContent).toBe(CLIENTS_UNREADABLE)
    expect(screen.queryByTestId('drawer-scope-all')).toBeNull()
  })

  it('a non-admin viewer: clients not landed shows loading, not a lock', () => {
    mount([member({ id: 'me', isYou: true, clientAccess: ['e1'] })], 'me', { entities: [], entitiesState: 'loading' })

    expect(screen.getByText('Loading clients…')).toBeTruthy()
    expect(screen.getByTestId('member-drawer').textContent).not.toContain(MEMBER_CLIENTS_ADMIN_ONLY)
  })

  it('a non-admin viewer on the sole admin: the admin-only note wins', () => {
    mount([viewer(), target({ role: 'admin' })])

    expect(screen.getByTestId('member-danger-note').textContent).toBe(MEMBER_ADMIN_ONLY)
    expect(screen.getByTestId('member-drawer').textContent).not.toContain(PROTECTED_ADMIN_NOTE)
  })

  it('an admin viewer: Suspend, Remove and the picker are live, with no note', () => {
    mount([adminViewer(), target({ clientAccess: ['e1'] })])

    expect((screen.getByTestId('member-suspend') as HTMLButtonElement).disabled).toBe(false)
    expect((screen.getByTestId('member-remove') as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTestId('member-danger-note')).toBeNull()
    expect(screen.queryByTestId('member-remove-note')).toBeNull()
    expect(screen.getByTestId('drawer-scope-all').closest('fieldset')?.hasAttribute('disabled')).toBe(false)
  })

  it('an admin viewer on their own sole-admin drawer: Suspend keeps PROTECTED_ADMIN_NOTE, Remove is absent', () => {
    mount([adminViewer(), target({ status: 'suspended' })], 'me')

    const suspend = screen.getByTestId('member-suspend') as HTMLButtonElement
    expect(suspend.disabled).toBe(true)
    expect(suspend.title).toBe(PROTECTED_ADMIN_NOTE)
    expect(screen.getByTestId('member-danger-note').textContent).toBe(PROTECTED_ADMIN_NOTE)
    expect(screen.queryByTestId('member-remove')).toBeNull()
  })

  it('an admin viewer: Remove next to a second admin is live', () => {
    mount([adminViewer(), target({ role: 'admin' })])

    expect((screen.getByTestId('member-remove') as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTestId('member-remove-note')).toBeNull()
  })
})
