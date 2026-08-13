// @vitest-environment jsdom
//
// RED specs for RoleModal's write path (AC-5 through AC-10): save()/remove() go async, the
// modal renders a rejected write's server sentence instead of closing on it, and no key is
// composed client-side any more.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import type { PlatformCtx } from '../types'
import { RoleModal, type RoleModalSubject } from './RoleModal'

function member(over: Partial<Member> = {}): Member {
  return {
    id: 'u1',
    name: 'Ada Person',
    initials: 'AP',
    email: 'ada@x.ng',
    role: 'admin',
    status: 'active',
    isYou: false,
    ...over,
  }
}

function role(over: Partial<Role> = {}): Role {
  return { key: 'cfo', title: 'CFO', desc: 'D', members: ['u1'], ...over }
}

function ctxWith(over: Record<string, unknown> = {}) {
  return {
    roles: [role()],
    members: [member(), member({ id: 'u2', name: 'Bo Person', initials: 'BP', email: 'bo@x.ng' })],
    policies: [],
    policiesState: 'ready',
    policiesError: null,
    refetchPolicies: vi.fn(),
    publishPolicy: vi.fn(),
    createRole: vi.fn().mockResolvedValue(role()),
    renameRole: vi.fn().mockResolvedValue(role()),
    staffRole: vi.fn().mockResolvedValue(role()),
    deleteRole: vi.fn().mockResolvedValue(undefined),
    refetchRoles: vi.fn(),
    ...over,
  } as unknown as PlatformCtx
}

/** Reads back a mock's own settled promise and attaches a no-op catch, so a deliberately
 * rejected write does not surface as vitest's global unhandled-rejection failure on top of
 * the real assertions below. Never used to weaken an assertion.
 *
 * The extra macrotask tick is load-bearing: React 19 commits a `setState` made from a promise
 * continuation (outside any React event or `act()`) via its scheduler's `MessageChannel`
 * queue, one tick past the microtask this awaits — proven by an isolated repro (3x chained
 * `await Promise.resolve()` still observes the pre-update DOM; one `setTimeout(0)` does not).
 * ClientsView.test.tsx's `waitFor`/`findBy*` calls around EntityFormModal's identical
 * rejected-submit path are this same wait, just via a polling helper instead of a fixed tick. */
async function drain(fn: ReturnType<typeof vi.fn>) {
  await fn.mock.results[0]?.value?.catch(() => {})
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function renderModal(subject: RoleModalSubject, ctxOver: Record<string, unknown> = {}, onClose = vi.fn(), onFlash = vi.fn()) {
  const ctx = ctxWith(ctxOver)
  render(<RoleModal ctx={ctx} subject={subject} onClose={onClose} onFlash={onFlash} />)
  return { ctx, onClose, onFlash }
}

afterEach(cleanup)

describe('AC-5: RoleModal.save() calls the server-minted-key create verb, arguments only', () => {
  it('TestRoleModal_UsesServerMintedKey', () => {
    const createRole = vi.fn().mockResolvedValue({ key: 'seat-7', title: 'Seat', desc: '', members: [] })
    renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))

    // Exactly (title, desc, members) -- no fourth, client-derived slug argument anywhere.
    expect(createRole).toHaveBeenCalledWith('Seat', '', [])
  })
})

describe('AC-5: edit writes split on what actually changed', () => {
  it('an edit that only renames does not restaff', () => {
    const renameRole = vi.fn().mockResolvedValue(role())
    const staffRole = vi.fn().mockResolvedValue(role())
    renderModal({ mode: 'edit', role: role() }, { renameRole, staffRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Chief' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect(renameRole).toHaveBeenCalledWith('cfo', 'Chief', 'D')
    expect(staffRole).not.toHaveBeenCalled()
  })

  it('an edit that only restaffs does not rename', () => {
    const renameRole = vi.fn().mockResolvedValue(role())
    const staffRole = vi.fn().mockResolvedValue(role())
    renderModal({ mode: 'edit', role: role() }, { renameRole, staffRole })

    const rows = screen.getAllByTestId('role-modal-member')
    const bo = rows.find((r) => within(r).queryByText('Bo Person'))!
    fireEvent.click(within(bo).getByRole('checkbox'))
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect(staffRole).toHaveBeenCalledWith('cfo', ['u1', 'u2'])
    expect(renameRole).not.toHaveBeenCalled()
  })

  // QA: mutation-tested gap. `membersChanged` swapped for array-equality (join(',') compare)
  // stayed green under the existing suite -- nothing exercised a reorder alone. The picker's
  // own tick order can differ from role.members' stored order (untick+retick, or a seed whose
  // members array isn't insertion-ordered), so order-sensitivity here is a false restaff.
  it('a re-tick that reproduces the same member set in a different order does not restaff', () => {
    const renameRole = vi.fn().mockResolvedValue(role())
    const staffRole = vi.fn().mockResolvedValue(role())
    const subject = role({ members: ['u1', 'u2'] })
    renderModal({ mode: 'edit', role: subject }, { renameRole, staffRole })

    const rows = screen.getAllByTestId('role-modal-member')
    const ada = rows.find((r) => within(r).queryByText('Ada Person'))!
    const bo = rows.find((r) => within(r).queryByText('Bo Person'))!
    // untick both, retick Bo then Ada -- same SET, reversed order.
    fireEvent.click(within(ada).getByRole('checkbox'))
    fireEvent.click(within(bo).getByRole('checkbox'))
    fireEvent.click(within(bo).getByRole('checkbox'))
    fireEvent.click(within(ada).getByRole('checkbox'))
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect(staffRole).not.toHaveBeenCalled()
    expect(renameRole).not.toHaveBeenCalled()
  })

  it('an edit that both renames and restaffs fires both verbs', async () => {
    const renameRole = vi.fn().mockResolvedValue(role())
    const staffRole = vi.fn().mockResolvedValue(role())
    renderModal({ mode: 'edit', role: role() }, { renameRole, staffRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Chief' } })
    const rows = screen.getAllByTestId('role-modal-member')
    const bo = rows.find((r) => within(r).queryByText('Bo Person'))!
    fireEvent.click(within(bo).getByRole('checkbox'))
    fireEvent.click(screen.getByTestId('role-modal-save'))
    // renameRole is awaited BEFORE staffRole is even called (save()'s sequential branches) --
    // staffRole needs that first microtask to clear.
    await renameRole.mock.results[0]?.value

    expect(renameRole).toHaveBeenCalledWith('cfo', 'Chief', 'D')
    expect(staffRole).toHaveBeenCalledWith('cfo', ['u1', 'u2'])
  })
})

describe('AC-6/AC-10: remove() awaits ctx.deleteRole and does not close on rejection', () => {
  it('a rejected delete keeps the modal open and shows the reason', async () => {
    const deleteRole = vi.fn().mockRejectedValue(new ApiError('http', 'workflow role not found', 404))
    const { onClose, onFlash } = renderModal({ mode: 'edit', role: role() }, { deleteRole })

    fireEvent.click(screen.getByTestId('role-delete'))
    fireEvent.click(screen.getByTestId('role-delete-confirmed'))
    await drain(deleteRole)

    expect(screen.getByTestId('role-modal')).toBeTruthy()
    expect(screen.getByTestId('role-modal-error').textContent).toBe('workflow role not found')
    expect(onClose).not.toHaveBeenCalled()
    expect(onFlash).not.toHaveBeenCalled()
  })

  it('a successful delete does not close the modal before ctx.deleteRole resolves', () => {
    const deleteRole = vi.fn(() => new Promise<void>(() => {}))
    const { onClose } = renderModal({ mode: 'edit', role: role() }, { deleteRole })

    fireEvent.click(screen.getByTestId('role-delete'))
    fireEvent.click(screen.getByTestId('role-delete-confirmed'))

    expect(onClose).not.toHaveBeenCalled()
  })
})

describe('AC-10: a rejected save keeps the modal open and shows the server sentence', () => {
  it('a rejected save keeps the modal open and shows the server sentence', async () => {
    const renameRole = vi.fn().mockRejectedValue(new ApiError('http', 'only an admin can change workflow roles', 403))
    const { onClose, onFlash } = renderModal({ mode: 'edit', role: role() }, { renameRole })

    fireEvent.change(screen.getByTestId('role-modal-desc'), { target: { value: 'D2' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await drain(renameRole)

    expect(screen.getByTestId('role-modal')).toBeTruthy()
    expect(screen.getByTestId('role-modal-error').textContent).toBe('only an admin can change workflow roles')
    expect(onClose).not.toHaveBeenCalled()
    expect(onFlash).not.toHaveBeenCalled()
  })
})

describe('AC-11 [D-PARTIAL-CREATE]: a partially-failed create shows the staffing reason and refetches', () => {
  it('a partially-failed create shows the staffing reason and refetches', async () => {
    const createRole = vi.fn().mockRejectedValue(new ApiError('http', 'invalid request', 400))
    const refetchRoles = vi.fn()
    const { onClose } = renderModal({ mode: 'create' }, { createRole, refetchRoles })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await drain(createRole)

    expect(screen.getByTestId('role-modal-error').textContent).toBe('invalid request')
    expect(refetchRoles).toHaveBeenCalledOnce()
    expect(onClose).not.toHaveBeenCalled()
  })
})

describe('AC-7: the EntityFormModal in-flight idiom', () => {
  it('Save is inert while a write is in flight', () => {
    const createRole = vi.fn(() => new Promise<Role>(() => {}))
    renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect(createRole).toHaveBeenCalledOnce()
    expect((screen.getByTestId('role-modal-save') as HTMLButtonElement).disabled).toBe(true)
  })

  it('the modal does not close until the write resolves', () => {
    const createRole = vi.fn(() => new Promise<Role>(() => {}))
    const { onClose } = renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect(onClose).not.toHaveBeenCalled()
  })

  it('every other control is disabled while a write is in flight', () => {
    const createRole = vi.fn(() => new Promise<Role>(() => {}))
    renderModal({ mode: 'edit', role: role() }, { createRole, renameRole: createRole })

    fireEvent.change(screen.getByTestId('role-modal-desc'), { target: { value: 'D2' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect((screen.getByTestId('role-modal-cancel') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('role-delete') as HTMLButtonElement).disabled).toBe(true)
    const firstRow = screen.getAllByTestId('role-modal-member')[0]
    expect((within(firstRow).getByRole('checkbox') as HTMLInputElement).disabled).toBe(true)
  })

  it('the save button label swaps to Saving… while a write is in flight', () => {
    const createRole = vi.fn(() => new Promise<Role>(() => {}))
    renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect(screen.getByTestId('role-modal-save').textContent).toBe('Saving…')
  })

  it('backdrop click does not close the modal while a write is in flight', () => {
    const createRole = vi.fn(() => new Promise<Role>(() => {}))
    const { onClose } = renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    const closedSoFar = onClose.mock.calls.length
    fireEvent.click(screen.getByRole('dialog').parentElement!)

    expect(onClose.mock.calls.length).toBe(closedSoFar)
  })
})

describe('AC-12: onFlash fires only after the write resolves', () => {
  it('the flash fires only after the write resolves', async () => {
    let settle!: (r: Role) => void
    const pending = new Promise<Role>((resolve) => {
      settle = resolve
    })
    const createRole = vi.fn(() => pending)
    const { onFlash } = renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))

    expect(onFlash).not.toHaveBeenCalled()

    settle(role({ key: 'seat-7', title: 'Seat', desc: '', members: [] }))
    await pending

    expect(onFlash).toHaveBeenCalledWith('Seat saved')
  })
})
