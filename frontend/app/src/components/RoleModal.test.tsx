// @vitest-environment jsdom
//
// RED specs for RoleModal's write path (AC-5 through AC-10): save()/remove() go async, the
// modal renders a rejected write's server sentence instead of closing on it, and no key is
// composed client-side any more.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'

// React's scheduler captures setImmediate at load, so wrap it before the imports below run.
const commit = vi.hoisted(() => {
  const real = globalThis.setImmediate
  const hold = { ms: 0 }
  globalThis.setImmediate = ((fn: (...a: unknown[]) => void, ...args: unknown[]) =>
    hold.ms ? real(() => setTimeout(fn, hold.ms, ...args)) : real(fn, ...args)) as typeof setImmediate
  return hold
})

import { ApiError } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import { CATCH_ALL_SCOPES, type Policy } from '../lib/workflows'
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
    rolesInForce: {},
    policiesError: null,
    refetchPolicies: vi.fn(),
    publishPolicy: vi.fn(),
    createRole: vi.fn().mockResolvedValue(role()),
    renameRole: vi.fn().mockResolvedValue(role()),
    staffRole: vi.fn().mockResolvedValue(role()),
    deleteRole: vi.fn().mockResolvedValue(undefined),
    refetchRoles: vi.fn(),
    refetchRolesInForce: vi.fn(),
    ...over,
  } as unknown as PlatformCtx
}

/** Attaches a no-op catch to a mock's rejected promise so it is not an unhandled rejection.
 * Callers then wait for the DOM with findBy or waitFor: React's commit lands a variable time later. */
async function drain(fn: ReturnType<typeof vi.fn>) {
  await fn.mock.results[0]?.value?.catch(() => {})
}

function renderModal(subject: RoleModalSubject, ctxOver: Record<string, unknown> = {}, onClose = vi.fn(), onFlash = vi.fn()) {
  const ctx = ctxWith(ctxOver)
  render(<RoleModal ctx={ctx} subject={subject} onClose={onClose} onFlash={onFlash} />)
  return { ctx, onClose, onFlash }
}

/** One approval step naming `role()`'s default key — the landed answer the gate must let through. */
function policy(over: Partial<Policy> = {}): Policy {
  return {
    id: 'p1',
    name: 'Test policy',
    position: 0,
    scopes: CATCH_ALL_SCOPES,
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false }],
    ...over,
  }
}

/** Swaps `ctx` under the SAME mount, so component state (`confirming`) survives the arriving status. */
function renderModalRerenderable(ctxOver: Record<string, unknown> = {}) {
  const subject: RoleModalSubject = { mode: 'edit', role: role() }
  const el = (over: Record<string, unknown>) => (
    <RoleModal ctx={ctxWith(over)} subject={subject} onClose={vi.fn()} onFlash={vi.fn()} />
  )
  const view = render(el(ctxOver))
  return { rerender: (next: Record<string, unknown>) => view.rerender(el(next)) }
}

afterEach(async () => {
  // Let a held commit land before unmount, or it leaks into the next test.
  if (commit.ms) await new Promise((resolve) => setTimeout(resolve, commit.ms + 10))
  commit.ms = 0
  await new Promise((resolve) => setTimeout(resolve, 10))
  cleanup()
})

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

describe('the delete confirm', () => {
  it('scrolls into view and disables Save while it is pending', () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    renderModal({ mode: 'edit', role: role() })
    const save = screen.getByTestId('role-modal-save') as HTMLButtonElement
    expect(save.disabled).toBe(false)
    fireEvent.click(screen.getByTestId('role-delete'))
    expect(scroll).toHaveBeenCalledWith({ block: 'nearest' })
    expect([save.disabled, save.style.opacity, save.style.cursor, save.style.filter]).toEqual([true, '0.45', 'not-allowed', 'none'])
    fireEvent.click(screen.getByTestId('role-delete-cancel'))
    expect(save.disabled).toBe(false)
  })
})

describe('AC-6/AC-10: remove() awaits ctx.deleteRole and does not close on rejection', () => {
  // A loaded CI box lands React's commit later than one tick; held 25ms here to force that deterministically.
  it.each([0, 25])('a rejected delete keeps the modal open and shows the reason (commit held %ims)', async (heldMs) => {
    // The scheduler skips setImmediate while a message loop is already pending, so let it go idle first.
    await new Promise((resolve) => setTimeout(resolve, 10))
    commit.ms = heldMs
    const deleteRole = vi.fn().mockRejectedValue(new ApiError('http', 'workflow role not found', 404))
    const { onClose, onFlash } = renderModal({ mode: 'edit', role: role() }, { deleteRole })

    fireEvent.click(screen.getByTestId('role-delete'))
    fireEvent.click(screen.getByTestId('role-delete-confirmed'))
    await drain(deleteRole)

    expect((await screen.findByTestId('role-modal-error')).textContent).toBe('workflow role not found')
    expect(screen.getByTestId('role-modal')).toBeTruthy()
    expect(onClose).not.toHaveBeenCalled()
    expect(onFlash).not.toHaveBeenCalled()
  })

  const S409 = 'this workflow role is named by a policy in force'
  const conflict = (names: string[]) =>
    new ApiError('http', S409, 409, { error: S409, policies: names.map((name, i) => ({ id: `p${i}`, name })) })

  async function refuseDelete(deleteRole: ReturnType<typeof vi.fn>) {
    fireEvent.click(screen.getByTestId('role-delete'))
    fireEvent.click(screen.getByTestId('role-delete-confirmed'))
    await drain(deleteRole)
    await screen.findByTestId('role-modal-error')
  }

  it('a 409 delete refusal lists the policies under the server sentence', async () => {
    const deleteRole = vi.fn().mockRejectedValue(conflict(['Credit notes', 'Big invoices']))
    const { onClose, onFlash } = renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)

    expect(screen.getByTestId('role-modal')).toBeTruthy()
    expect(screen.getByTestId('role-modal-error').textContent?.startsWith(S409)).toBe(true)
    const items = within(screen.getByTestId('role-modal-error-policies')).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual(['Credit notes', 'Big invoices'])
    expect(onClose).not.toHaveBeenCalled()
    expect(onFlash).not.toHaveBeenCalled()
  })

  it('a 409 refusal re-reads the usage; any other refusal does not', async () => {
    const refetchRolesInForce = vi.fn()
    const deleteRole = vi.fn().mockRejectedValue(conflict(['Credit notes']))
    renderModal({ mode: 'edit', role: role() }, { deleteRole, refetchRolesInForce })
    await refuseDelete(deleteRole)
    expect(refetchRolesInForce).toHaveBeenCalledOnce()
    cleanup()

    const other = vi.fn()
    const notFound = vi.fn().mockRejectedValue(new ApiError('http', 'workflow role not found', 404))
    renderModal({ mode: 'edit', role: role() }, { deleteRole: notFound, refetchRolesInForce: other })
    await refuseDelete(notFound)
    expect(other).not.toHaveBeenCalled()
  })

  it('an open-run-only 409 does not re-read the usage; one that names policies does', async () => {
    const runOnly = vi.fn()
    const deleteRoleRun = vi.fn().mockRejectedValue(runConflict(['INV-1']))
    renderModal({ mode: 'edit', role: role() }, { deleteRole: deleteRoleRun, refetchRolesInForce: runOnly })
    await refuseDelete(deleteRoleRun)
    expect(runOnly).not.toHaveBeenCalled()
    cleanup()

    const both = vi.fn()
    const deleteRoleBoth = vi.fn().mockRejectedValue(runConflict(['INV-1'], 1, ['Credit notes']))
    renderModal({ mode: 'edit', role: role() }, { deleteRole: deleteRoleBoth, refetchRolesInForce: both })
    await refuseDelete(deleteRoleBoth)
    expect(both).toHaveBeenCalledOnce()
  })

  it('a 409 refusal with many policies renders every name as text', async () => {
    const long = 'L'.repeat(120)
    const deleteRole = vi.fn().mockRejectedValue(conflict(['One', '<b>x</b>', long, 'Four']))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)

    const list = screen.getByTestId('role-modal-error-policies')
    const items = within(list).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual(['One', '<b>x</b>', long, 'Four'])
    expect(list.querySelector('b')).toBeNull()
  })

  it('a save attempt clears the policy list a delete refusal left', async () => {
    const deleteRole = vi.fn().mockRejectedValue(conflict(['Credit notes']))
    const renameRole = vi.fn().mockRejectedValue(new ApiError('http', 'only an admin can change workflow roles', 403))
    renderModal({ mode: 'edit', role: role() }, { deleteRole, renameRole })
    await refuseDelete(deleteRole)
    expect(screen.getByTestId('role-modal-error-policies')).toBeTruthy()

    fireEvent.click(screen.getByTestId('role-delete-cancel'))
    fireEvent.change(screen.getByTestId('role-modal-desc'), { target: { value: 'D2' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    expect(screen.queryByTestId('role-modal-error-policies'), 'cleared while the save is in flight').toBeNull()
    await drain(renameRole)

    await waitFor(() =>
      expect(screen.getByTestId('role-modal-error').textContent).toBe('only an admin can change workflow roles'),
    )
    expect(screen.queryByTestId('role-modal-error-policies')).toBeNull()
  })

  it('a 404 delete refusal renders no policy list', async () => {
    const deleteRole = vi.fn().mockRejectedValue(new ApiError('http', 'workflow role not found', 404, { error: 'workflow role not found' }))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)

    expect(screen.queryByTestId('role-modal-error-policies')).toBeNull()
    expect(screen.getByTestId('role-modal-error').textContent).toBe('workflow role not found')
  })

  it('a second attempt clears the previous policy list', async () => {
    const deleteRole = vi
      .fn()
      .mockRejectedValueOnce(conflict(['Credit notes']))
      .mockRejectedValueOnce(new ApiError('http', 'workflow role not found', 404))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)
    expect(screen.getByTestId('role-modal-error-policies')).toBeTruthy()

    fireEvent.click(screen.getByTestId('role-delete-confirmed'))
    await new Promise((resolve) => setTimeout(resolve, 0))
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(screen.queryByTestId('role-modal-error-policies')).toBeNull()
    expect(screen.getByTestId('role-modal-error').textContent).toBe('workflow role not found')
  })

  const SRun = 'this workflow role is awaited by an open approval run'
  const runConflict = (numbers: string[], total = numbers.length, policies: string[] = []) =>
    new ApiError('http', SRun, 409, {
      error: SRun,
      policies: policies.map((name, i) => ({ id: `p${i}`, name })),
      invoices: numbers.map((invoice_number, i) => ({ id: `i${i}`, invoice_number })),
      invoices_total: total,
    })

  it('a 409 run refusal lists the invoices under the server sentence', async () => {
    const deleteRole = vi.fn().mockRejectedValue(runConflict(['INV-1', 'INV-2']))
    const { onClose, onFlash } = renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)

    expect(screen.getByTestId('role-modal')).toBeTruthy()
    expect(screen.getByTestId('role-modal-error').textContent?.startsWith(SRun)).toBe(true)
    const items = within(screen.getByTestId('role-modal-error-invoices')).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual(['INV-1', 'INV-2'])
    expect(onClose).not.toHaveBeenCalled()
    expect(onFlash).not.toHaveBeenCalled()
  })

  it('an invoice number renders as text', async () => {
    const deleteRole = vi.fn().mockRejectedValue(runConflict(['<b>x</b>']))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)

    const list = screen.getByTestId('role-modal-error-invoices')
    expect(within(list).getAllByRole('listitem')[0].textContent).toBe('<b>x</b>')
    expect(list.querySelector('b')).toBeNull()
  })

  it('a refusal that names policies and invoices renders both lists, policies first', async () => {
    const deleteRole = vi.fn().mockRejectedValue(runConflict(['INV-1', 'INV-2'], 2, ['Credit notes', 'Big invoices']))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)

    const policies = screen.getByTestId('role-modal-error-policies')
    const invoices = screen.getByTestId('role-modal-error-invoices')
    expect(policies.compareDocumentPosition(invoices) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(policies).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Credit notes', 'Big invoices'])
    expect(within(invoices).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['INV-1', 'INV-2'])
  })

  it('the invoice list says how many more the server did not list', async () => {
    const deleteRole = vi.fn().mockRejectedValue(runConflict(['INV-1', 'INV-2'], 5))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)

    const items = within(screen.getByTestId('role-modal-error-invoices')).getAllByRole('listitem')
    expect(items.at(-1)?.getAttribute('data-testid')).toBe('role-modal-error-invoices-more')
    expect(items.at(-1)?.textContent).toBe('and 3 more')
    cleanup()

    const exact = vi.fn().mockRejectedValue(runConflict(['INV-1', 'INV-2'], 2))
    renderModal({ mode: 'edit', role: role() }, { deleteRole: exact })
    await refuseDelete(exact)
    expect(screen.queryByTestId('role-modal-error-invoices-more')).toBeNull()
  })

  it('a policy-only refusal and a 404 render no invoice list', async () => {
    const deleteRole = vi.fn().mockRejectedValue(conflict(['Credit notes']))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)
    expect(screen.queryByTestId('role-modal-error-invoices')).toBeNull()
    expect(screen.getByTestId('role-modal-error-policies')).toBeTruthy()
    cleanup()

    const missing = vi.fn().mockRejectedValue(new ApiError('http', 'workflow role not found', 404, { error: 'workflow role not found' }))
    renderModal({ mode: 'edit', role: role() }, { deleteRole: missing })
    await refuseDelete(missing)
    expect(screen.queryByTestId('role-modal-error-invoices')).toBeNull()
    expect(screen.getByTestId('role-modal-error').textContent).toBe('workflow role not found')
  })

  it('a second attempt clears the previous invoice list', async () => {
    const deleteRole = vi
      .fn()
      .mockRejectedValueOnce(runConflict(['INV-1']))
      .mockRejectedValueOnce(new ApiError('http', 'workflow role not found', 404))
    renderModal({ mode: 'edit', role: role() }, { deleteRole })
    await refuseDelete(deleteRole)
    expect(screen.getByTestId('role-modal-error-invoices')).toBeTruthy()

    fireEvent.click(screen.getByTestId('role-delete-confirmed'))
    await waitFor(() => expect(screen.getByTestId('role-modal-error').textContent).toBe('workflow role not found'))
    expect(screen.queryByTestId('role-modal-error-invoices')).toBeNull()
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

    expect((await screen.findByTestId('role-modal-error')).textContent).toBe('only an admin can change workflow roles')
    expect(screen.getByTestId('role-modal')).toBeTruthy()
    expect(onClose).not.toHaveBeenCalled()
    expect(onFlash).not.toHaveBeenCalled()
  })
})

describe('APSC-11: a refused staffing save', () => {
  const S = 'the last signer of this role cannot be removed while an open run waits on it'
  const refusal = (numbers: string[], total = numbers.length) =>
    new ApiError('http', S, 409, {
      error: S,
      policies: [],
      invoices: numbers.map((invoice_number, i) => ({ id: `i${i}`, invoice_number })),
      invoices_total: total,
    })

  async function refuseSave(over: Record<string, unknown>) {
    const staffRole = vi.fn().mockRejectedValue(over.err)
    const r = renderModal({ mode: 'edit', role: role() }, { ...over.ctx as object, staffRole })
    fireEvent.click(within(screen.getAllByTestId('role-modal-member')[0]).getByRole('checkbox'))
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await drain(staffRole)
    await screen.findByTestId('role-modal-error')
    return { ...r, staffRole }
  }
  const invoiceItems = () => within(screen.getByTestId('role-modal-error-invoices')).getAllByRole('listitem')

  it('a refused staffing save lists the waiting invoices under the server sentence', async () => {
    const { onClose, onFlash } = await refuseSave({ err: refusal(['INV-1', 'INV-2']) })

    expect(screen.getByTestId('role-modal')).toBeTruthy()
    expect(screen.getByTestId('role-modal-error').textContent?.startsWith(S)).toBe(true)
    expect(invoiceItems().map((li) => li.textContent)).toEqual(['INV-1', 'INV-2'])
    expect(onClose).not.toHaveBeenCalled()
    expect(onFlash).not.toHaveBeenCalled()
  })

  it('an invoice number in a refused save renders as text', async () => {
    await refuseSave({ err: refusal(['<b>x</b>']) })
    expect(invoiceItems()[0].textContent).toBe('<b>x</b>')
    expect(screen.getByTestId('role-modal-error-invoices').querySelector('b')).toBeNull()
  })

  it('a refused staffing save says how many more the server did not list', async () => {
    await refuseSave({ err: refusal(['INV-1', 'INV-2'], 5) })
    expect(invoiceItems().at(-1)?.getAttribute('data-testid')).toBe('role-modal-error-invoices-more')
    expect(invoiceItems().at(-1)?.textContent).toBe('and 3 more')
    cleanup()

    await refuseSave({ err: refusal(['INV-1', 'INV-2'], 2) })
    expect(screen.queryByTestId('role-modal-error-invoices-more')).toBeNull()
  })

  it('after a refused staffing save the picker is editable and Save is usable', async () => {
    const { staffRole, onClose } = await refuseSave({ err: refusal(['INV-1']) })

    expect((screen.getByTestId('role-modal-save') as HTMLButtonElement).disabled).toBe(false)
    for (const row of screen.getAllByTestId('role-modal-member')) {
      expect((within(row).getByRole('checkbox') as HTMLInputElement).disabled).toBe(false)
    }
    expect((screen.getByTestId('role-modal-search') as HTMLInputElement).disabled).toBe(false)
    expect(screen.queryByTestId('role-modal-error-policies')).toBeNull()

    staffRole.mockResolvedValueOnce(role())
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await drain(staffRole)
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
    expect(screen.queryByTestId('role-modal-error')).toBeNull()
  })

  it('a second save clears the previous invoice list', async () => {
    const { staffRole } = await refuseSave({ err: refusal(['INV-1']) })
    expect(screen.getByTestId('role-modal-error-invoices')).toBeTruthy()

    let failSecond: (e: unknown) => void = () => {}
    staffRole.mockReturnValueOnce(new Promise((_, reject) => { failSecond = reject }))
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.queryByTestId('role-modal-error-invoices'), 'cleared as the next save starts').toBeNull()
    failSecond(new ApiError('http', 'workflow role not found', 404))

    await waitFor(() => expect(screen.getByTestId('role-modal-error').textContent).toBe('workflow role not found'))
    expect(screen.queryByTestId('role-modal-error-invoices')).toBeNull()
  })

  it('a 403 or a 400 on save renders no invoice list', async () => {
    await refuseSave({ err: new ApiError('http', 'only an admin can change workflow roles', 403) })
    expect(screen.queryByTestId('role-modal-error-invoices')).toBeNull()
    expect(screen.getByTestId('role-modal-error').textContent).toBe('only an admin can change workflow roles')
    cleanup()

    await refuseSave({ err: new ApiError('http', 'invalid request', 400) })
    expect(screen.queryByTestId('role-modal-error-invoices')).toBeNull()
    expect(screen.getByTestId('role-modal-error').textContent).toBe('invalid request')
  })

  it('a refused staffing save after a successful rename keeps the modal open', async () => {
    const renameRole = vi.fn().mockResolvedValue(role())
    const staffRole = vi.fn().mockRejectedValue(refusal(['INV-1', 'INV-2']))
    const { onClose, onFlash } = renderModal({ mode: 'edit', role: role() }, { renameRole, staffRole })
    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Chief' } })
    fireEvent.click(within(screen.getAllByTestId('role-modal-member')[0]).getByRole('checkbox'))
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await drain(staffRole)

    expect((await screen.findByTestId('role-modal-error')).textContent?.startsWith(S)).toBe(true)
    expect(renameRole).toHaveBeenCalledOnce()
    expect(invoiceItems().map((li) => li.textContent)).toEqual(['INV-1', 'INV-2'])
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

    expect((await screen.findByTestId('role-modal-error')).textContent).toBe('invalid request')
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

  it('Saving… keeps the primary fill and dims at the disabled recipe', () => {
    const createRole = vi.fn(() => new Promise<Role>(() => {}))
    renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))

    const save = screen.getByTestId('role-modal-save') as HTMLButtonElement
    expect(save.disabled).toBe(true)
    expect(save.className, 'the primary fill is the class, not an inline override').toContain('v2-btn-primary')
    expect(save.style.background).toBe('')
    expect([save.style.opacity, save.style.cursor, save.style.filter]).toEqual(['0.45', 'not-allowed', 'none'])
  })

  it('a rejected write renders the server sentence in the red block', async () => {
    const createRole = vi.fn().mockRejectedValue(new ApiError('http', 'invalid request', 400))
    renderModal({ mode: 'create' }, { createRole })

    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await drain(createRole)

    const err = await screen.findByTestId('role-modal-error')
    expect(err.style.background).toBe('var(--status-red-bg)')
    expect(err.style.border).toContain('var(--status-red-border)')
    expect(err.style.color).toBe('var(--status-red-text)')
    expect(err.style.padding).toBe('12px 14px')
    expect(err.style.fontSize).toBe('13px')
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

// ============================================================================
// APSC-08 -- the delete confirm reads `ctx.rolesInForce`; `ctx.policies` only feeds the draft tail
// ============================================================================

describe('APSC-08: the delete confirmation claims usage only off a known rolesInForce', () => {
  const RULE = 'The delete is refused while a policy in force names it in a scope or an approval step.'
  const TAIL = 'A draft that names it keeps it, and those steps cannot publish until you point them somewhere else.'
  const stepUse = { id: 'p1', name: 'Test policy', steps: 1, scope: false }

  function confirmText(): string {
    return screen.getByTestId('role-delete-confirm').querySelector('p')?.textContent ?? ''
  }
  function openConfirm(ctxOver: Record<string, unknown>) {
    renderModal({ mode: 'edit', role: role() }, ctxOver)
    fireEvent.click(screen.getByTestId('role-delete'))
  }

  it('an unknown usage renders the unknown-usage confirm, never "not used in any policy"', () => {
    openConfirm({ rolesInForce: null })

    expect(confirmText(), 'the confirm block rendered no sentence, so the absence below is vacuous').toContain('CFO')
    expect(confirmText()).toContain('Its policy usage could not be loaded')
    expect(confirmText()).not.toContain('not used in any policy')
    expect((screen.getByTestId('role-delete-confirmed') as HTMLButtonElement).disabled, 'the gate blocked the delete instead of the claim').toBe(false)
  })

  it('the unknown-usage confirm carries no draft tail even over a draft naming the role', () => {
    openConfirm({ rolesInForce: null, policies: [policy()] })

    expect(confirmText()).toContain('Its policy usage could not be loaded')
    expect(confirmText()).not.toContain(TAIL)
  })

  it('a known empty usage states the in-force rule and claims no usage', () => {
    openConfirm({ rolesInForce: {} })

    expect(confirmText()).toBe(`Delete CFO? ${RULE}`)
  })

  it('the open confirm agrees with the card', () => {
    openConfirm({ rolesInForce: { cfo: [stepUse] }, policies: [policy({ status: 'published', activeVersion: 1 })] })

    expect(confirmText()).toContain('1 approval step · 1 policy')
    expect(confirmText()).toContain(RULE)
    expect(confirmText()).not.toContain(TAIL)
  })

  it('a role only an active scope names: the confirm states the count and the scope', () => {
    openConfirm({ rolesInForce: { cfo: [{ id: 'p1', name: 'Scoped', steps: 0, scope: true }] } })

    expect(confirmText()).toContain('In force: 0 approval steps · 1 policy. 1 policy names it in a creator scope.')
    expect(confirmText()).not.toContain('not used in any policy')
  })

  it('a draft that names the role adds the tail at zero in-force policies', () => {
    openConfirm({ rolesInForce: {}, policies: [policy()], policiesState: 'ready' })

    expect(confirmText()).toBe(`Delete CFO? ${RULE} ${TAIL}`)
  })

  it('a draft that names the role adds the tail beside an in-force count', () => {
    openConfirm({ rolesInForce: { cfo: [stepUse] }, policies: [policy()], policiesState: 'ready' })

    expect(confirmText()).toBe(`Delete CFO? In force: 1 approval step · 1 policy. ${RULE} ${TAIL}`)
  })

  it('no tail while the policies fetch has not landed', () => {
    openConfirm({ rolesInForce: {}, policies: [policy()], policiesState: 'loading' })

    expect(confirmText()).not.toContain(TAIL)
  })

  it('no tail when the draft names the role only in a scope', () => {
    const scopeOnly = policy({ nodes: [], scopes: { ...CATCH_ALL_SCOPES, workflow_role_keys: ['cfo'] } })
    openConfirm({ rolesInForce: {}, policies: [scopeOnly], policiesState: 'ready' })

    expect(confirmText()).not.toContain(TAIL)
  })

  it("'idle' -- no gateway configured -- still adds the tail (the landed side)", () => {
    openConfirm({ rolesInForce: {}, policies: [policy()], policiesState: 'idle' })

    expect(confirmText()).toContain(TAIL)
  })

  it('the claim appears the moment the usage lands under an already-open confirm', () => {
    const { rerender } = renderModalRerenderable({ rolesInForce: null })
    fireEvent.click(screen.getByTestId('role-delete'))
    expect(confirmText(), 'the confirm did not open, so the flip below is vacuous').not.toContain('1 approval step')

    rerender({ rolesInForce: { cfo: [stepUse] } })

    expect(screen.getByTestId('role-delete-confirm'), 'the arriving usage closed the confirm').toBeTruthy()
    expect(confirmText()).toContain('1 approval step · 1 policy')
  })

  it('the withheld CLAIM is not a withheld ACTION -- Delete still reaches the gateway', () => {
    const deleteRole = vi.fn().mockResolvedValue(undefined)
    renderModal({ mode: 'edit', role: role() }, { rolesInForce: null, deleteRole })
    fireEvent.click(screen.getByTestId('role-delete'))
    fireEvent.click(screen.getByTestId('role-delete-confirmed'))

    expect(deleteRole, 'the unknown-usage gate swallowed the delete itself').toHaveBeenCalledWith('cfo')
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

// ============================================================================
// v2 paint: the role modal against the prototype's resolved values
// ============================================================================

describe('Role modal v2 paint', () => {
  const RECIPE = ['0.45', 'not-allowed', 'none']
  const recipeOf = (el: HTMLElement) => [el.style.opacity, el.style.cursor, el.style.filter]
  const crew = [
    member({ id: 'u1', name: 'Ada Person', initials: 'AP' }),
    member({ id: 'u2', name: 'Bo Person', initials: 'BP', status: 'suspended' }),
    member({ id: 'u3', name: 'Cy Person', initials: 'CP', status: 'invited' }),
  ]
  const editSubject: RoleModalSubject = { mode: 'edit', role: role({ members: ['u1'] }) }
  const panel = () => screen.getByTestId('role-modal')
  const rowOf = (name: string) => screen.getAllByTestId('role-modal-member').find((r) => r.textContent?.includes(name)) as HTMLElement
  const boxOf = (row: HTMLElement) => row.querySelector('input')!.nextElementSibling as HTMLElement
  const footer = () => screen.getByTestId('role-modal-save').parentElement!.parentElement as HTMLElement

  it('the panel is bg-1 on a line-2 border, radius-lg, shadow-card, 560 wide', () => {
    renderModal(editSubject, { members: crew })
    const p = panel()
    expect([p.style.background, p.style.borderRadius, p.style.boxShadow, p.style.width, p.style.maxHeight]).toEqual([
      'var(--bg-1)',
      'var(--radius-lg)',
      'var(--shadow-card)',
      '560px',
      '86vh',
    ])
    expect(p.style.border).toContain('var(--line-2)')
  })

  it('the scrim is the token mix with both blur properties, and no oklch anywhere', () => {
    renderModal(editSubject, { members: crew })
    expect((panel().parentElement as HTMLElement).style.background).toBe('color-mix(in srgb, var(--surface) 55%, transparent)')
    const html = renderToStaticMarkup(<RoleModal ctx={ctxWith({ members: crew })} subject={editSubject} onClose={() => {}} onFlash={() => {}} />)
    const style = /^<div style="([^"]*)"/.exec(html)?.[1]
    expect(style, 'the outermost element is the scrim').toBeTruthy()
    const decls = new Map(style!.split(';').filter(Boolean).map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1).trim()]))
    expect(decls.get('position'), 'control: the scrim declarations are read').toBe('fixed')
    expect(decls.get('backdrop-filter')).toBe('blur(6px)')
    expect(decls.get('-webkit-backdrop-filter')).toBe('blur(6px)')
    expect(html.length).toBeGreaterThan(500)
    expect(html).not.toContain('oklch')
  })

  it('header, body and footer carry the 18/20/14, 16/20/20 and 14/20 bands', () => {
    renderModal(editSubject, { members: crew })
    const title = screen.getByText('Edit role', { exact: true })
    expect([title.style.fontSize, title.style.fontWeight]).toEqual(['16px', '700'])
    const head = title.parentElement!.parentElement as HTMLElement
    expect([head.style.padding, head.style.borderBottom]).toEqual(['18px 20px 14px', '1px solid var(--line-1)'])
    const sub = title.nextElementSibling as HTMLElement
    expect([sub.style.fontSize, sub.style.lineHeight, sub.style.marginTop, sub.style.color]).toEqual(['12.5px', '1.5', '3px', 'var(--fg-3)'])
    expect((screen.getByTestId('role-modal-name').parentElement as HTMLElement).style.padding).toBe('16px 20px 20px')
    const f = footer()
    expect([f.style.padding, f.style.borderTop]).toEqual(['14px 20px', '1px solid var(--line-1)'])
    expect((f.lastElementChild as HTMLElement).style.gap).toBe('10px')
  })

  it('the picker is a bg-2 box padded 10/10/6 scrolling at 268, rows 7/8 gapped 10', () => {
    renderModal(editSubject, { members: crew })
    const row = rowOf('Ada Person')
    const list = row.parentElement as HTMLElement
    expect(list.style.maxHeight).toBe('268px')
    const box = list.parentElement as HTMLElement
    expect([box.style.background, box.style.padding, box.style.borderRadius]).toEqual(['var(--bg-2)', '10px 10px 6px', 'var(--radius-md)'])
    expect(box.style.border).toContain('var(--line-2)')
    expect([row.style.padding, row.style.gap, row.style.borderRadius]).toEqual(['7px 8px', '10px', 'var(--radius-md)'])
    const mail = within(row).getByText('ada@x.ng')
    expect([mail.className, mail.style.fontSize]).toEqual(['mono', '10px'])
  })

  it('the painted box is 16 square, radius 4: filled and ticked when held, bg-2 and empty when not', () => {
    renderModal(editSubject, { members: crew })
    const on = boxOf(rowOf('Ada Person'))
    expect(on.style.border).toBe('1px solid var(--action)')
    expect([on.style.background, on.style.borderRadius]).toEqual(['var(--action)', '4px'])
    expect(on.querySelector('svg'), 'the tick').toBeTruthy()
    expect((on.parentElement as HTMLElement).style.width).toBe('16px')
    expect((on.parentElement as HTMLElement).style.height).toBe('16px')
    const off = boxOf(rowOf('Bo Person'))
    expect(off.style.border).toBe('1px solid var(--line-2)')
    expect(off.style.background).toBe('var(--bg-2)')
    expect(off.querySelector('svg')).toBeNull()
    expect(rowOf('Ada Person').style.background).toBe('var(--action-tint)')
    expect(rowOf('Bo Person').style.background).toBe('transparent')
  })

  it('the native checkbox is the toggle: clicking it or its row flips the held set', () => {
    const staffRole = vi.fn().mockResolvedValue(role())
    renderModal(editSubject, { members: crew, staffRole })
    const input = within(rowOf('Bo Person')).getByRole('checkbox') as HTMLInputElement
    expect(input.style.opacity, 'transparent over the painted box').toBe('0')
    expect(input.style.zIndex, 'above the painted box, so it takes the pointer').toBe('1')
    expect(input.className, 'the focus-ring rule keys on this class').toBe('pf-check')
    expect(input.nextElementSibling, 'the focus-ring rule rings the next sibling').toBe(boxOf(rowOf('Bo Person')))
    expect(input.checked).toBe(false)
    fireEvent.click(input)
    expect(input.checked).toBe(true)
    expect(boxOf(rowOf('Bo Person')).querySelector('svg')).toBeTruthy()
    fireEvent.click(rowOf('Bo Person'))
    expect(input.checked, 'the row is a label, so a click on it toggles back').toBe(false)
    fireEvent.click(screen.getByTestId('role-modal-save'))
    expect(staffRole, 'back to the original set, so nothing to write').not.toHaveBeenCalled()
  })

  it('every picker avatar is the dark person chip; a suspended person is told by the red word, an invited one is absent', () => {
    renderModal(editSubject, { members: crew })
    const rows = screen.getAllByTestId('role-modal-member')
    expect(rows.length, 'the invited person has no row').toBe(2)
    expect(screen.queryByText('Cy Person')).toBeNull()
    for (const r of rows) {
      const chip = within(r).getByText(/^[A-Z]P$/)
      expect([chip.style.background, chip.style.fontSize, chip.style.width, chip.style.borderRadius]).toEqual(['var(--slate-800)', '9.5px', '26px', '50%'])
    }
    const word = within(rowOf('Bo Person')).getByText(/suspended/)
    expect(word.style.color).toBe('var(--status-red-text)')
    expect(within(rowOf('Ada Person')).queryByText(/suspended/)).toBeNull()
    const note = screen.getByTestId('role-modal-hidden')
    expect([note.style.borderTop, note.style.fontSize, note.style.padding]).toEqual(['1px solid var(--line-1)', '11.5px', '8px 4px 6px'])
  })

  it('RoleModal: removed rows raise no hidden footnote', () => {
    const gone = member({ id: 'u9', name: 'Dee Person', initials: 'DP', status: 'removed', email: null })
    renderModal(editSubject, { members: [crew[0], gone] })
    expect(screen.queryByTestId('role-modal-hidden')).toBeNull()
    expect(screen.queryByText('Dee Person')).toBeNull()

    cleanup()
    renderModal(editSubject, { members: [crew[0], gone, crew[2]] })
    expect(screen.getByTestId('role-modal-hidden').textContent).toBe('1 invited person is hidden until they accept the invite.')
  })

  it('Save, blank: primary class, dimmed at the recipe, no fill override; typing a name lifts it', () => {
    renderModal({ mode: 'create' }, { members: crew })
    const save = screen.getByTestId('role-modal-save') as HTMLButtonElement
    expect(save.disabled).toBe(true)
    expect(save.className).toContain('v2-btn-primary')
    expect(save.style.height).toBe('36px')
    expect(recipeOf(save)).toEqual(RECIPE)
    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: '   ' } })
    expect(recipeOf(save), 'a name of spaces is still blank').toEqual(RECIPE)
    fireEvent.change(screen.getByTestId('role-modal-name'), { target: { value: 'Seat' } })
    expect(save.disabled).toBe(false)
    expect(recipeOf(save), 'enabled Save is not dimmed').toEqual(['', '', ''])
  })

  it('Cancel is the 36px ghost; Delete role is the 36px red ghost, edit mode only', () => {
    renderModal(editSubject, { members: crew })
    const cancel = screen.getByTestId('role-modal-cancel')
    expect(cancel.className).toContain('v2-btn-ghost')
    expect(cancel.style.height).toBe('36px')
    const del = screen.getByTestId('role-delete')
    expect(del.className).toContain('v2-btn-ghost')
    expect([del.style.height, del.style.color, del.style.borderColor]).toEqual(['36px', 'var(--status-red-text)', 'var(--status-red-border)'])
    expect(del.style.background, 'a ghost, not a red fill').toBe('')
    cleanup()
    renderModal({ mode: 'create' }, { members: crew })
    expect(screen.queryByTestId('role-delete')).toBeNull()
  })

  it('the delete confirm is a red block in the body; the footer keeps Cancel and Save and drops the footer Delete', () => {
    renderModal(editSubject, { members: crew })
    fireEvent.click(screen.getByTestId('role-delete'))
    const block = screen.getByTestId('role-delete-confirm')
    expect([block.style.padding, block.style.borderRadius, block.style.background, block.style.marginTop]).toEqual(['12px 14px', 'var(--radius-md)', 'var(--status-red-bg)', '14px'])
    expect(block.style.border).toContain('var(--status-red-border)')
    expect(footer().contains(block), 'the block is not in the footer').toBe(false)
    expect(panel().contains(block)).toBe(true)
    const text = block.querySelector('p') as HTMLElement
    expect([text.style.fontSize, text.style.lineHeight, text.style.color]).toEqual(['12.5px', '1.5', 'var(--status-red-text)'])
    expect(footer().contains(screen.getByTestId('role-modal-cancel'))).toBe(true)
    expect(footer().contains(screen.getByTestId('role-modal-save'))).toBe(true)
    expect(screen.queryByTestId('role-delete'), 'the footer Delete gives way to the block').toBeNull()
    const keep = screen.getByTestId('role-delete-cancel')
    const gone = screen.getByTestId('role-delete-confirmed')
    expect(block.contains(keep) && block.contains(gone)).toBe(true)
    expect(keep.className).toContain('v2-btn-ghost')
    expect([keep.style.height, keep.style.fontSize, gone.style.height, gone.style.fontSize]).toEqual(['32px', '12.5px', '32px', '12.5px'])
    expect([gone.style.background, gone.style.color]).toEqual(['var(--status-red-text)', 'var(--primary-foreground)'])
    expect([keep.textContent, gone.textContent]).toEqual(['Keep role', 'Delete role'])
    fireEvent.click(keep)
    expect(screen.queryByTestId('role-delete-confirm')).toBeNull()
    expect(screen.getByTestId('role-delete')).toBeTruthy()
  })

  it('in flight, the picker box, the footer buttons and the confirm buttons all take the recipe', () => {
    const renameRole = vi.fn(() => new Promise<Role>(() => {}))
    renderModal(editSubject, { members: crew, renameRole })
    fireEvent.change(screen.getByTestId('role-modal-desc'), { target: { value: 'D2' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    expect(recipeOf(boxOf(rowOf('Ada Person')))).toEqual(RECIPE)
    expect(rowOf('Ada Person').style.cursor).toBe('not-allowed')
    for (const id of ['role-modal-cancel', 'role-delete']) {
      const b = screen.getByTestId(id)
      expect(recipeOf(b), id).toEqual(RECIPE)
      expect(b.style.background, `${id} keeps its hover neutraliser`).toBe('transparent')
    }
  })

  it('in flight with the confirm open, Keep role and Delete role take the recipe', () => {
    const deleteRole = vi.fn(() => new Promise<void>(() => {}))
    renderModal(editSubject, { members: crew, deleteRole })
    fireEvent.click(screen.getByTestId('role-delete'))
    fireEvent.click(screen.getByTestId('role-delete-confirmed'))
    const keep = screen.getByTestId('role-delete-cancel') as HTMLButtonElement
    const gone = screen.getByTestId('role-delete-confirmed') as HTMLButtonElement
    expect([keep.disabled, gone.disabled]).toEqual([true, true])
    expect(recipeOf(keep)).toEqual(RECIPE)
    expect(recipeOf(gone)).toEqual(RECIPE)
    expect(gone.style.background, 'dimmed, not recoloured').toBe('var(--status-red-text)')
  })

  it('a rejected write leaves the error above the buttons in the footer and Save usable again', async () => {
    const renameRole = vi.fn().mockRejectedValue(new ApiError('http', 'only an admin can change workflow roles', 403))
    renderModal(editSubject, { members: crew, renameRole })
    fireEvent.change(screen.getByTestId('role-modal-desc'), { target: { value: 'D2' } })
    fireEvent.click(screen.getByTestId('role-modal-save'))
    await drain(renameRole)

    const err = await screen.findByTestId('role-modal-error')
    expect(err.textContent).toBe('only an admin can change workflow roles')
    expect(err.style.borderRadius).toBe('var(--radius-md)')
    expect(footer().contains(err)).toBe(true)
    expect(err.nextElementSibling, 'the button row follows').toBe(screen.getByTestId('role-modal-save').parentElement)
    const save = screen.getByTestId('role-modal-save') as HTMLButtonElement
    expect(save.textContent).toBe('Save role')
    expect(save.disabled).toBe(false)
    expect(recipeOf(save), 'the dimming lifts with the lock').toEqual(['', '', ''])
  })

  // jsdom cannot evaluate :focus-visible, so the rule text is the oracle; a deployed Tab covers the paint.
  it('platform.css rings the painted box of a keyboard-focused picker checkbox', () => {
    const platformCss = readFileSync(join(process.cwd(), 'src/styles/platform.css'), 'utf8')
    const rule = /\.asc-app \.pf-check:focus-visible \+ span\s*\{([^}]*)\}/.exec(platformCss)
    expect(platformCss.length, 'the sheet loaded').toBeGreaterThan(1000)
    expect(rule, 'the rule exists').toBeTruthy()
    expect(rule![1]).toMatch(/box-shadow:\s*0 0 0 2px var\(--ring\)/)
  })

  it('the close button is the 30px bg-3 square and closes when idle', () => {
    const { onClose } = renderModal(editSubject, { members: crew })
    const close = screen.getByTestId('role-modal-close')
    expect([close.style.width, close.style.height, close.style.background]).toEqual(['30px', '30px', 'var(--bg-3)'])
    fireEvent.click(close)
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
