// @vitest-environment jsdom
//
// APPR-09-03 QA (task-507). The screen had NO test file before this: `Updated
// {policy.updated}` swapped to `policyStanding(policy)` at WorkflowsView.tsx:109 with no
// oracle at all — verified by mutation, a hardcoded 'Updated recently' literal (with the
// now-unused import deleted, so `noUnusedLocals` stays quiet) passed all 2026 app tests
// and a clean tsc.
//
// Deliberately SMALL. Subtask 04 owns this screen's four-surface ladder, its EmptyState,
// its testid wrapper and the INTRO copy, and adds nine more specs to this file; this one
// pins only the cell the live swap moved.

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
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
