// @vitest-environment jsdom
//
// RED specs for APPR-04-06 AC3/AC4: `toggleWorkflowRole` currently fires ctx.staffRole and
// forgets about it -- no await, no .catch, no in-flight guard. Every spec below fails on its
// own target assertion (a call count that is still 2, a sentence that never renders, a pill
// that a second click still reaches) -- never on import/typecheck.
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import { drawerRoleHelper, stepsNamedLine, SUSPENDED_STEPS_NOTE, type Role } from '../lib/roles'
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

function role(over: Partial<Role> = {}): Role {
  return { key: 'cfo', title: 'CFO', desc: '', members: [], ...over }
}

function drawerCtx(over: Record<string, unknown> = {}): PlatformCtx {
  return {
    members: [member(), member({ id: 'u2', name: 'Bo Person', initials: 'BP', email: 'bo@x.ng' })],
    roles: [],
    policies: [],
    mode: 'firm',
    staffRole: vi.fn().mockResolvedValue(undefined),
    renameRole: vi.fn(),
    setSettingsTab: vi.fn(),
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
      updated: 'now',
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
