// @vitest-environment jsdom
//
// RED specs for RoleModal's write path (AC-5 through AC-10): save()/remove() go async, the
// modal renders a rejected write's server sentence instead of closing on it, and no key is
// composed client-side any more.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
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

/** One approval step naming `role()`'s default key — the landed answer the gate must let through. */
function policy(over: Partial<Policy> = {}): Policy {
  return {
    id: 'p1',
    name: 'Test policy',
    scope: 'All invoices',
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

    const err = screen.getByTestId('role-modal-error')
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
// APPR-09-06 (task-510) — the delete confirm's usage claim
// ============================================================================
// The confirm used to read `ctx.policies` with no status gate, and `roleUsage` returns the
// literal 'not used in any policy' at zero (lib/roles.ts:207) — so an unlanded fetch printed
// that sentence immediately above a Delete button, on a role that IS used. The fork now runs
// through `policiesLanded` (RoleModal.tsx:97) into `deleteRoleConfirmUnknownUsage`. Asserted by
// what the block must NOT say rather than by the new string, so a fourth branch cannot satisfy
// these by naming itself something else.

describe('APPR-09-06 AC-1/AC-3: the delete confirmation claims usage only off a landed policies fetch', () => {
  function confirmText(): string {
    return screen.getByTestId('role-delete-confirm').textContent ?? ''
  }

  it('the delete confirmation withholds its usage claim while policies are still loading', () => {
    renderModal({ mode: 'edit', role: role() }, { policies: [], policiesState: 'loading' })
    fireEvent.click(screen.getByTestId('role-delete'))

    const text = confirmText()
    // Needle under the absence: the block must still name the role it is about to delete, or a
    // confirm that rendered nothing at all would satisfy the assertion below.
    expect(text, 'the confirm block rendered no sentence, so the absence below is vacuous').toContain('CFO')
    expect(text, 'an unlanded policies fetch reads as "not used in any policy" above a Delete button').not.toContain(
      'not used in any policy',
    )
    // Nothing is BLOCKED here — the consequence merely cannot be narrated, and the server's own
    // refusal still lands in `role-modal-error` if the delete is declined.
    expect((screen.getByTestId('role-delete-confirmed') as HTMLButtonElement).disabled, 'the gate blocked the delete instead of the claim').toBe(false)
  })

  it('the delete confirmation withholds its usage claim over an errored policies fetch', () => {
    // `policies: []` PINNED, not incidental: App.tsx keeps the last landed rows across an error
    // (App.tsx:294-298), so an errored fetch holding stale rows would not exercise this path at
    // all. The live defect is the NEVER-LANDED one.
    renderModal({ mode: 'edit', role: role() }, { policies: [], policiesState: 'error' })
    fireEvent.click(screen.getByTestId('role-delete'))

    const text = confirmText()
    expect(text, 'the confirm block rendered no sentence, so the absence below is vacuous').toContain('CFO')
    expect(text).not.toContain('not used in any policy')
  })

  // The over-widening guard (WorkflowBuilder.test.tsx:104's posture), green before the gate
  // landed and green after: it pins that the gate does not swallow a genuinely landed-empty
  // answer. Killed by gating on `policies.length` instead of on the status.
  it('a landed-empty policy list still says the role is not used in any policy', () => {
    renderModal({ mode: 'edit', role: role() }, { policies: [], policiesState: 'empty' })
    fireEvent.click(screen.getByTestId('role-delete'))

    expect(confirmText(), 'the guard swallowed a genuinely landed-empty answer').toContain('It is not used in any policy.')
  })

  // ------------------------------------------------------------------------
  // QA (Stage 4) — adversarial coverage the RED set did not carry
  // ------------------------------------------------------------------------

  it('a landed policy that names the role states the real usage — the confirm CAN make a claim', () => {
    // The population floor under the two absences above: a fork that withheld the clause in
    // EVERY state would satisfy them both, and the landed-empty spec above cannot see it
    // (its landed sentence is the zero copy, which the withheld branch could also fake).
    renderModal({ mode: 'edit', role: role() }, { policies: [policy()], policiesState: 'ready' })
    fireEvent.click(screen.getByTestId('role-delete'))

    expect(confirmText()).toContain('1 approval step · 1 policy')
    expect(confirmText()).toContain('Those steps will block until you point them somewhere else.')
  })

  it("'idle' — no gateway configured — is the LANDED side, matching the Workflows screen", () => {
    // `membersSurface` folds 'idle' into 'empty' (lib/members.ts:586). A gate written as
    // `surface === 'roster'` would withhold here and disagree with WorkflowsView.tsx:65-68,
    // which renders its own no-policies-yet card on that same build.
    renderModal({ mode: 'edit', role: role() }, { policies: [], policiesState: 'idle' })
    fireEvent.click(screen.getByTestId('role-delete'))

    expect(confirmText()).toContain('It is not used in any policy.')
  })

  it('the claim appears the moment the fetch lands under an already-open confirm', () => {
    // The confirm is not remounted by the arriving status: `confirming` is component state and
    // the fork is computed per render. A guard that latched the withheld copy at open time —
    // or that closed the confirm on the status change — would fail here.
    const { rerender } = renderModalRerenderable({ policies: [], policiesState: 'loading' })
    fireEvent.click(screen.getByTestId('role-delete'))
    expect(confirmText(), 'the confirm did not open, so the flip below is vacuous').not.toContain('approval step')

    rerender({ policies: [policy()], policiesState: 'ready' })

    expect(screen.getByTestId('role-delete-confirm'), 'the arriving status closed the confirm').toBeTruthy()
    expect(confirmText(), 'the withheld copy latched at open time instead of re-forking on the landed status').toContain(
      '1 approval step · 1 policy',
    )
  })

  it('the withheld CLAIM is not a withheld ACTION — Delete still reaches the gateway', () => {
    // AC-1 gates the sentence, never the verb. `role-modal-error` still carries the server's own
    // refusal if the delete is declined, so nothing is lost by letting the click through.
    const deleteRole = vi.fn().mockResolvedValue(undefined)
    renderModal({ mode: 'edit', role: role() }, { policies: [], policiesState: 'loading', deleteRole })
    fireEvent.click(screen.getByTestId('role-delete'))
    fireEvent.click(screen.getByTestId('role-delete-confirmed'))

    expect(deleteRole, 'the unlanded gate swallowed the delete itself').toHaveBeenCalledWith('cfo')
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
  const boxOf = (row: HTMLElement) => row.querySelector('input')!.previousElementSibling as HTMLElement
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

    const err = screen.getByTestId('role-modal-error')
    expect(err.textContent).toBe('only an admin can change workflow roles')
    expect(err.style.borderRadius).toBe('var(--radius-md)')
    expect(footer().contains(err)).toBe(true)
    expect(err.nextElementSibling, 'the button row follows').toBe(screen.getByTestId('role-modal-save').parentElement)
    const save = screen.getByTestId('role-modal-save') as HTMLButtonElement
    expect(save.textContent).toBe('Save role')
    expect(save.disabled).toBe(false)
    expect(recipeOf(save), 'the dimming lifts with the lock').toEqual(['', '', ''])
  })

  it('the close button is the 30px bg-3 square and closes when idle', () => {
    const { onClose } = renderModal(editSubject, { members: crew })
    const close = screen.getByTestId('role-modal-close')
    expect([close.style.width, close.style.height, close.style.background]).toEqual(['30px', '30px', 'var(--bg-3)'])
    fireEvent.click(close)
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
