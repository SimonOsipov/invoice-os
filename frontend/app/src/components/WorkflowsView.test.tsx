// @vitest-environment jsdom
//
// WorkflowsView's oracle: the four-surface ladder and its gates, each row's two claims,
// and the two write-error slots.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { WorkflowsView } from './WorkflowsView'

function policy(over: Partial<Policy> = {}): Policy {
  return {
    id: 'p1',
    name: 'Standard approval policy',
    scope: 'All invoices',
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false }],
    ...over,
  }
}

// `over` widens the fake the way `builderCtx(over)` does (WorkflowBuilder.test.tsx:28): the
// double cast already disables property checking, so one shape serves every surface.
function listCtx(policies: Policy[], over: Record<string, unknown> = {}): PlatformCtx {
  return {
    mode: 'firm',
    active: { short: 'Lagos Freight' },
    policies,
    policiesState: 'ready',
    policiesError: null,
    refetchPolicies: vi.fn(),
    refetchRoles: vi.fn(),
    editingPolicyId: null,
    createPolicy: vi.fn(async () => {}),
    deletePolicy: vi.fn(async () => {}),
    openPolicy: vi.fn(),
    closePolicy: vi.fn(),
    // The builder's own ctx fields (WorkflowBuilder.test.tsx:28-50). Present because the
    // ladder's placement is only observable by rendering WorkflowsView with
    // `editingPolicyId` set, which mounts the builder for real.
    roles: [],
    members: [],
    rolesState: 'ready',
    rolesError: null,
    savePolicy: vi.fn(async (p: Policy) => p),
    publishPolicy: vi.fn(async () => policy()),
    setSettingsTab: vi.fn(),
    nav: vi.fn(),
    ...over,
  } as unknown as PlatformCtx
}

const EMPTY_TITLE = 'No approval policies yet'
const EMPTY_MESSAGE = 'Every invoice transmits as soon as it validates. Create one to require sign-off first.'
const DELETE_REFUSAL = 'only an admin can change approval policies'
const CREATE_REFUSAL = 'this workspace has reached its approval-policy limit'
const DOWN = () => new ApiError('http', 'gateway is down', 503)

afterEach(cleanup)

describe('APPR-09-03 QA: each row states its VERSION standing, not an edit time', () => {
  it('renders the standing computed from version/activeVersion for every row', () => {
    const rows = [
      policy({ id: 'polA', name: 'In force', status: 'published', version: 3, activeVersion: 3 }),
      policy({ id: 'polB', name: 'Edited draft', status: 'draft', version: 4, activeVersion: 3 }),
      policy({ id: 'polC', name: 'Lost the slot', status: 'published', version: 2, activeVersion: null }),
      policy({ id: 'polD', name: 'Brand new', status: 'draft', version: 1, activeVersion: null }),
    ]
    render(<WorkflowsView ctx={listCtx(rows)} />)

    // Control: the list really rendered all four rows, so the standings below are read off
    // a populated screen rather than agreeing vacuously with an empty one.
    expect(screen.getByText('4 POLICIES')).toBeTruthy()
    for (const p of rows) expect(screen.getByText(p.name), `${p.id} did not render`).toBeTruthy()

    // Four DISTINCT strings, each a function of that row's own version pair — a literal
    // cannot satisfy all four, and nor can a constant read off the first row.
    expect(screen.getByText('v3 in force')).toBeTruthy()
    expect(screen.getByText('v3 in force · v4 draft')).toBeTruthy()
    expect(screen.getByText('Not in force')).toBeTruthy()
    expect(screen.getByText('Never published')).toBeTruthy()
  })

  it('no row claims an edit time — `Policy.updated` is gone from the type', () => {
    render(<WorkflowsView ctx={listCtx([policy({ status: 'published', version: 1, activeVersion: 1 })])} />)

    expect(screen.getByText('v1 in force'), 'the standing cell must still render').toBeTruthy()
    expect(screen.queryByText(/^Updated /), 'a row still renders an "Updated …" cell').toBeNull()
  })
})

// ============================================================================
// APPR-09-04 (task-508) — the four surfaces, the count's gate, the two error slots
// ============================================================================
// The ladder lives INSIDE `PolicyList`, below WorkflowsView's `editing ? Builder : List`
// branch. A 'loading' arm above that branch would tear an open builder off screen the
// moment subtask 05's Publish fires its refetch, so every spec here renders the list.
//
// Exact-string matching throughout for the standings: 'Not in force' CONTAINS 'in force',
// so a regex self-collides across all four branches of `policyStanding`.

describe('APPR-09-04 AC-1/AC-2/AC-3: the surface is chosen by policiesState, never by policies.length', () => {
  it('renders the shared Loading surface, not an empty list, while the policies fetch is in flight', () => {
    render(<WorkflowsView ctx={listCtx([], { policiesState: 'loading' })} />)

    expect(document.querySelector('.apic-loading-spin'), 'the shared Loading surface must render').toBeTruthy()
    // Regex ON PURPOSE here, unlike the standings: it has to catch the title both as the
    // shipped one-node sentence and as `EmptyState`'s split heading.
    expect(screen.queryByText(new RegExp(EMPTY_TITLE)), 'an unlanded fetch claimed an empty workspace').toBeNull()
    expect(screen.queryByTestId('policies-empty')).toBeNull()
  })

  it('an errored fetch renders the error surface, never the empty state', () => {
    render(<WorkflowsView ctx={listCtx([], { policiesState: 'error', policiesError: DOWN() })} />)

    expect(screen.getByText('gateway is down'), "the gateway's own sentence must reach the screen").toBeTruthy()
    // `ErrorState` is the FETCH surface, and its heading is hardcoded (ErrorState.tsx:45).
    // AC-5 and AC-7 assert that heading is ABSENT at the write controls, which is exactly
    // why they never share a render with this spec.
    expect(screen.getByText('Something went wrong')).toBeTruthy()
    expect(screen.queryByText(new RegExp(EMPTY_TITLE)), 'a failed fetch rendered the empty copy').toBeNull()
  })

  it('Retry re-runs only the policies fetch', () => {
    const refetchPolicies = vi.fn()
    const refetchRoles = vi.fn()
    render(<WorkflowsView ctx={listCtx([], { policiesState: 'error', policiesError: DOWN(), refetchPolicies, refetchRoles })} />)

    fireEvent.click(screen.getByText('Retry'))

    expect(refetchPolicies).toHaveBeenCalledTimes(1)
    expect(refetchRoles, 'a policies failure re-kicked a healthy roster fetch').not.toHaveBeenCalled()
  })

  it('a landed empty list renders the shared empty state with the shipped sentence', () => {
    render(<WorkflowsView ctx={listCtx([], { policiesState: 'empty' })} />)

    expect(screen.getByTestId('policies-empty')).toBeTruthy()
    // TWO nodes, not one: `EmptyState` takes {title, message}, so the shipped sentence splits
    // at its em dash and no assertion on the joined string could ever match.
    expect(screen.getByText(EMPTY_TITLE)).toBeTruthy()
    expect(screen.getByText(EMPTY_MESSAGE)).toBeTruthy()
  })

  it('a landed-empty state renders the empty surface even when the stale mirror still holds rows', () => {
    // NOT a reachable state once App.tsx's mirror clears on a landed-empty fetch (D1). It
    // earns its place as the mutation-killer for AC-1: a `policies.length === 0 ? empty :
    // list` implementation passes every other spec in this file and fails only here. The
    // screen must not depend on App's guard being correct.
    const stale = [policy({ id: 'polA', name: 'Stale one' }), policy({ id: 'polB', name: 'Stale two' })]
    render(<WorkflowsView ctx={listCtx(stale, { policiesState: 'empty' })} />)

    expect(screen.getByTestId('policies-empty'), 'the control needle for the absences below').toBeTruthy()
    for (const p of stale) expect(screen.queryByText(p.name), `${p.id} rendered over a landed-empty fetch`).toBeNull()
  })

  it('the POLICIES count renders only over a landed roster, never over an unlanded or failed fetch', () => {
    // `{policies.length} POLICIES` says `0 POLICIES` beside a spinner and beside ErrorState —
    // the same "an errored fetch reads as an empty workspace" claim AC-1 exists to kill, one
    // node over.
    for (const state of ['loading', 'error'] as const) {
      render(<WorkflowsView ctx={listCtx([], { policiesState: state, policiesError: DOWN() })} />)
      // Control: the toolbar renders over every surface, so a blank screen is not what makes
      // the count absent.
      expect(screen.getByText('Approval policies'), `the header must render over '${state}'`).toBeTruthy()
      expect(screen.queryAllByText(/POLICIES/), `the count claimed a roster over '${state}'`).toHaveLength(0)
      cleanup()
    }

    // AC-6's half: on a landed roster the count is the live list's length, not a constant.
    render(<WorkflowsView ctx={listCtx([policy({ id: 'p1' }), policy({ id: 'p2' }), policy({ id: 'p3' })])} />)
    expect(screen.getByText('3 POLICIES')).toBeTruthy()
  })
})

describe('APPR-09-04 AC-4: the pill and the standing report different facts', () => {
  it('each row carries its own status pill beside its standing', () => {
    const list = [
      policy({ id: 'polA', name: 'In force', status: 'published', version: 3, activeVersion: 3 }),
      policy({ id: 'polB', name: 'Edited draft', status: 'draft', version: 4, activeVersion: 3 }),
    ]
    render(<WorkflowsView ctx={listCtx(list)} />)

    const rows = Array.from(document.querySelectorAll('.pf-row'))
    expect(rows, 'no rows rendered, so the pill counts below would agree vacuously').toHaveLength(2)
    // One pill per row, each reading its OWN row's status — not the first row's, twice.
    expect(screen.getAllByText('PUBLISHED')).toHaveLength(1)
    expect(screen.getAllByText('DRAFT')).toHaveLength(1)
    expect(rows[0].textContent).toContain('PUBLISHED')
    expect(rows[1].textContent).toContain('DRAFT')
  })

  it('a PUBLISHED policy that lost the slot carries both claims in one row without contradiction', () => {
    render(<WorkflowsView ctx={listCtx([policy({ id: 'polA', status: 'published', version: 2, activeVersion: null })])} />)

    const rows = Array.from(document.querySelectorAll('.pf-row'))
    expect(rows, 'no row rendered at all').toHaveLength(1)
    // `PolicyStatusPill` reads `status`; `policyStanding` reads version/activeVersion. The
    // pairing is correct, not contradictory — nothing may reconcile them into one claim.
    expect(rows[0].textContent).toContain('PUBLISHED')
    expect(rows[0].textContent).toContain('Not in force')
  })
})

describe('APPR-09-04 AC-5: a refused create says why, at the control', () => {
  it("a refused create renders the server's own sentence at the control", async () => {
    const createPolicy = vi.fn(() => Promise.reject(new ApiError('http', CREATE_REFUSAL, 403)))
    render(<WorkflowsView ctx={listCtx([policy()], { createPolicy })} />)

    fireEvent.click(screen.getByRole('button', { name: 'New policy' }))

    expect(createPolicy, 'the control is wired to nothing at all').toHaveBeenCalledTimes(1)
    expect(await screen.findByText(CREATE_REFUSAL)).toBeTruthy()
    expect(screen.getByTestId('policy-create-error')).toBeTruthy()
    // `ErrorState` hardcodes 'Something went wrong' and is the fetch surface. A write refusal
    // that borrowed it would bury the one sentence that says what to do about the refusal.
    expect(screen.queryByText(/something went wrong/i), 'generic client copy stood in for the server reason').toBeNull()
  })
})

describe('APPR-09-04 AC-7: a refused delete says why, at the row it failed on', () => {
  const two = () => [policy({ id: 'polA', name: 'First policy' }), policy({ id: 'polB', name: 'Second policy' })]
  const refusing = () => vi.fn(() => Promise.reject(new ApiError('http', DELETE_REFUSAL, 403)))

  it("a refused delete renders the server's own sentence at the row that failed", async () => {
    const deletePolicy = refusing()
    render(<WorkflowsView ctx={listCtx(two(), { deletePolicy })} />)

    fireEvent.click(screen.getByRole('button', { name: 'Delete First policy' }))

    expect(deletePolicy, 'the delete control is wired to nothing at all').toHaveBeenCalledWith('polA')
    expect(await screen.findByText(DELETE_REFUSAL)).toBeTruthy()
    expect(screen.queryByText(/something went wrong/i), 'generic client copy stood in for the server reason').toBeNull()
  })

  it('the delete reason is anchored to the failed row, not to the create control', async () => {
    const deletePolicy = refusing()
    const openPolicy = vi.fn()
    render(<WorkflowsView ctx={listCtx(two(), { deletePolicy, openPolicy })} />)

    fireEvent.click(screen.getByRole('button', { name: 'Delete Second policy' }))
    const shown = await screen.findByTestId('policy-delete-error')

    // ONE slot, keyed to the row that failed — not the same message repeated under every row.
    expect(screen.getAllByTestId('policy-delete-error')).toHaveLength(1)
    expect(screen.queryByTestId('policy-create-error'), 'a delete failure lit the create control').toBeNull()

    // OUTSIDE `.pf-row`: the row's whole box carries `onClick={onEdit}`, so a message nested
    // inside it would open the builder when the user clicks the sentence explaining the
    // failure.
    fireEvent.click(shown)
    expect(openPolicy, 'clicking the failure message opened the builder').not.toHaveBeenCalled()

    // Control: the row itself still opens, so the absence above is not vacuous.
    const rows = Array.from(document.querySelectorAll('.pf-row'))
    expect(rows).toHaveLength(2)
    fireEvent.click(rows[1])
    expect(openPolicy).toHaveBeenCalledWith('polB')
  })

  it('a refused create leaves a standing delete reason alone — two slots, not one', async () => {
    const deletePolicy = refusing()
    const createPolicy = vi.fn(() => Promise.reject(new ApiError('http', CREATE_REFUSAL, 403)))
    render(<WorkflowsView ctx={listCtx(two(), { deletePolicy, createPolicy })} />)

    fireEvent.click(screen.getByRole('button', { name: 'Delete First policy' }))
    expect(await screen.findByText(DELETE_REFUSAL)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'New policy' }))
    expect(await screen.findByText(CREATE_REFUSAL)).toBeTruthy()

    // One shared slot would have blanked the row's reason to show the header's. Two distinct
    // failures are two distinct facts and the screen must hold both.
    expect(screen.getByText(DELETE_REFUSAL), 'the create failure cleared the row-scoped delete reason').toBeTruthy()
  })
})

// ============================================================================
// APPR-09-04 (task-508) Stage 4 QA — the invariants the AC rows leave open
// ============================================================================
// The rows above each pin one AC in isolation. These pin what only appears when the
// surfaces are considered together: that exactly one of the four renders over every status
// the fetch can report, that the ladder never reaches an open builder, and that the two
// error slots survive each other in BOTH orders.

const STATES = ['idle', 'loading', 'error', 'empty', 'ready'] as const

describe('APPR-09-04 QA: exactly one surface, over every status the fetch can report', () => {
  /** Which of the four surfaces are lit, by their own hooks. */
  function lit(): string[] {
    const shown = {
      loading: document.querySelector('.apic-loading-spin') != null,
      error: screen.queryByText('Something went wrong') != null,
      empty: screen.queryByTestId('policies-empty') != null,
      list: screen.queryByTestId('policies-list') != null,
    }
    return Object.entries(shown)
      .filter(([, on]) => on)
      .map(([name]) => name)
  }

  it('every status lights exactly one surface, and the one its own reading justifies', () => {
    // 'idle' is the no-gateway build, folded into the empty card exactly as Members and
    // Roles fold it (D4). Recorded, not repaired: on that build the card's sentence is
    // false — nothing was ever asked — and that is an app-wide reading, not this screen's.
    const expected: Record<(typeof STATES)[number], string> = {
      idle: 'empty',
      loading: 'loading',
      error: 'error',
      empty: 'empty',
      ready: 'list',
    }

    for (const state of STATES) {
      // A populated mirror throughout: a surface chosen off `policies.length` would agree
      // with the roster arm on every row of this table and be caught by none of them.
      render(<WorkflowsView ctx={listCtx([policy({ id: 'polA', name: 'Standard' })], { policiesState: state, policiesError: DOWN() })} />)
      expect(lit(), `'${state}' did not render exactly its own surface`).toEqual([expected[state]])
      cleanup()
    }
  })

  it('the POLICIES count renders on the roster arm and on no other, over all five statuses', () => {
    for (const state of STATES) {
      render(<WorkflowsView ctx={listCtx([policy({ id: 'polA' })], { policiesState: state, policiesError: DOWN() })} />)

      // Control: the toolbar renders over every surface, so an absent count below is the
      // gate doing its job rather than a screen that rendered nothing at all.
      expect(screen.getByText('Approval policies'), `the header must render over '${state}'`).toBeTruthy()

      const counts = screen.queryAllByText(/POLICIES/).map((n) => n.textContent)
      if (state === 'ready') expect(counts, 'the roster arm lost its count').toEqual(['1 POLICIES'])
      else expect(counts, `the count claimed a roster over '${state}'`).toEqual([])
      cleanup()
    }
  })

  it('nothing refetches during a render — Retry is the only trigger this screen carries', () => {
    const refetchPolicies = vi.fn()

    for (const state of STATES) {
      render(<WorkflowsView ctx={listCtx([policy({ id: 'polA' })], { policiesState: state, policiesError: DOWN(), refetchPolicies })} />)
      expect(refetchPolicies, `rendering '${state}' kicked a fetch`).not.toHaveBeenCalled()
      cleanup()
    }

    // The floor under that absence: the one trigger that SHOULD fire still does.
    render(<WorkflowsView ctx={listCtx([], { policiesState: 'error', policiesError: DOWN(), refetchPolicies })} />)
    fireEvent.click(screen.getByText('Retry'))
    expect(refetchPolicies).toHaveBeenCalledTimes(1)
  })

  it('the empty card reads its title as the heading and its message as the body, not the reverse', () => {
    render(<WorkflowsView ctx={listCtx([], { policiesState: 'empty' })} />)

    // `EmptyState` renders the title in a <div> and the message in a <p> (EmptyState.tsx:41-42).
    // Both strings being present is not enough — swapping the two props keeps both on screen.
    expect(screen.getByText(EMPTY_TITLE).tagName, 'the title is rendered as the body copy').toBe('DIV')
    expect(screen.getByText(EMPTY_MESSAGE).tagName, 'the message is rendered as the heading').toBe('P')
  })
})

describe('APPR-09-04 QA: the ladder belongs to the LIST and never reaches an open builder', () => {
  const one = () => [policy({ id: 'polA', name: 'Standard approval policy' })]

  it('an in-flight refetch leaves an open builder standing', () => {
    // The state subtask 05's Publish produces on every click, and the state App's create
    // now produces too: status 'loading' while the mirror still holds the edited policy.
    render(<WorkflowsView ctx={listCtx(one(), { editingPolicyId: 'polA', policiesState: 'loading' })} />)

    expect(screen.getByLabelText('Policy name'), 'a background refetch tore the open builder off screen').toBeTruthy()
    expect(document.querySelector('.apic-loading-spin'), "the list's spinner rendered over the builder").toBeNull()
    expect(screen.queryByText('Approval policies'), 'the list header rendered over the builder').toBeNull()
  })

  it('a fetch that fails while the builder is open leaves it standing, and the error waits on the list', () => {
    const { unmount } = render(<WorkflowsView ctx={listCtx(one(), { editingPolicyId: 'polA', policiesState: 'error', policiesError: DOWN() })} />)

    expect(screen.getByLabelText('Policy name'), 'a failed background fetch tore the open builder off screen').toBeTruthy()
    expect(screen.queryByText('gateway is down'), 'F4: the error surfaces on RETURN to the list, not over the builder').toBeNull()
    unmount()

    // The other half of that accepted consequence, so the absence above is not read as the
    // error having been swallowed outright.
    render(<WorkflowsView ctx={listCtx(one(), { policiesState: 'error', policiesError: DOWN() })} />)
    expect(screen.getByText('gateway is down')).toBeTruthy()
  })

  it('a policy deleted out from under an open builder falls back to the list rather than crashing', () => {
    // App.tsx clears `editingPolicyId` on its own delete; this is the OTHER route — the row
    // vanished from a refetch because someone else deleted it (F4, third bullet).
    render(<WorkflowsView ctx={listCtx(one(), { editingPolicyId: 'polGone' })} />)

    expect(screen.getByText('Approval policies'), 'a stale editingPolicyId blanked the screen').toBeTruthy()
    expect(screen.getByText('1 POLICIES')).toBeTruthy()
    expect(screen.queryByLabelText('Policy name'), 'the builder opened for a policy the list does not hold').toBeNull()
  })
})

describe('APPR-09-04 QA: the two error slots survive each other, in both orders', () => {
  const two = () => [policy({ id: 'polA', name: 'First policy' }), policy({ id: 'polB', name: 'Second policy' })]
  const refusing = (message: string) => vi.fn(() => Promise.reject(new ApiError('http', message, 403)))

  it('a refused delete leaves a standing create reason alone — the reverse of the AC-7 order', async () => {
    const createPolicy = refusing(CREATE_REFUSAL)
    const deletePolicy = refusing(DELETE_REFUSAL)
    render(<WorkflowsView ctx={listCtx(two(), { createPolicy, deletePolicy })} />)

    fireEvent.click(screen.getByRole('button', { name: 'New policy' }))
    expect(await screen.findByText(CREATE_REFUSAL)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Delete First policy' }))
    expect(await screen.findByText(DELETE_REFUSAL)).toBeTruthy()

    expect(screen.getByText(CREATE_REFUSAL), "the delete failure cleared the create control's reason").toBeTruthy()
  })

  it('the reason sits immediately after the row that failed, and MOVES when another row fails', async () => {
    const deletePolicy = refusing(DELETE_REFUSAL)
    render(<WorkflowsView ctx={listCtx(two(), { deletePolicy })} />)

    fireEvent.click(screen.getByRole('button', { name: 'Delete Second policy' }))
    const first = await screen.findByTestId('policy-delete-error')
    // Sibling ORDER inside the column, not merely "somewhere on screen": the Fragment puts
    // the message directly after its own row, which is the whole anchoring claim.
    expect(first.previousElementSibling?.textContent, 'the reason is anchored to the wrong row').toContain('Second policy')

    fireEvent.click(screen.getByRole('button', { name: 'Delete First policy' }))
    await waitFor(() => {
      const shown = screen.getAllByTestId('policy-delete-error')
      expect(shown, 'both rows carry a reason at once — the slot became a per-row map').toHaveLength(1)
      expect(shown[0].previousElementSibling?.textContent, 'the reason stayed on the row that failed first').toContain('First policy')
    })
  })

  it('a retried create clears its own stale reason rather than leaving a refusal over a success', async () => {
    let refuse = true
    const createPolicy = vi.fn(() => (refuse ? Promise.reject(new ApiError('http', CREATE_REFUSAL, 403)) : Promise.resolve()))
    render(<WorkflowsView ctx={listCtx([policy()], { createPolicy })} />)

    fireEvent.click(screen.getByRole('button', { name: 'New policy' }))
    expect(await screen.findByText(CREATE_REFUSAL)).toBeTruthy()

    refuse = false
    fireEvent.click(screen.getByRole('button', { name: 'New policy' }))
    await waitFor(() => expect(screen.queryByTestId('policy-create-error'), 'a refusal outlived the retry that succeeded').toBeNull())
    // The floor: the absence above would also hold if the second click did nothing at all.
    expect(createPolicy).toHaveBeenCalledTimes(2)
  })
})

describe('APPR-09-04 QA: the intro states what publishing does today', () => {
  it('carries the interim second sentence and drops the claim the flag does not honour', () => {
    render(<WorkflowsView ctx={listCtx([policy()])} />)

    expect(
      screen.getByText(/Publishing a policy opens an approval on every matching invoice\. Transmission is not held for approval yet\./),
      'the intro lost the interim sentence APPR-14 removes when it flips the flag',
    ).toBeTruthy()
    expect(screen.queryByText(/applies it to every matching invoice/), 'the superseded claim is still on screen').toBeNull()
  })
})
