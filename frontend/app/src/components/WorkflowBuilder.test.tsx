// @vitest-environment jsdom
//
// RED specs for APPR-04-06 AC5/AC6: WorkflowBuilder does not read ctx.rolesState at all
// today, so an unlanded roles fetch renders every approval step as if its role were
// genuinely deleted -- roleOf's fallback (lib/roles.ts:63) can't tell "not fetched yet"
// from "fetched and gone". The guard belongs HERE, not in WorkflowsView (which forwards
// ctx whole and reads no role data) -- see the story's [D-BUILDER-GUARD].
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { WorkflowBuilder } from './WorkflowBuilder'

function policyWith(role: string): Policy {
  return {
    id: 'p1',
    name: 'Test policy',
    scope: 'All invoices',
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'n1', type: 'approval', role, sla: '24', delegate: false }],
  }
}

function builderCtx(over: Record<string, unknown> = {}): PlatformCtx {
  return {
    mode: 'firm',
    roles: [],
    members: [],
    rolesState: 'ready',
    rolesError: null,
    refetchRoles: vi.fn(),
    // The four policy fields the ctx gained in APPR-09-03. Present so a reader can never
    // see `undefined` — the double cast below disables property checking, so nothing else
    // would say they are missing.
    policies: [],
    policiesState: 'ready',
    policiesError: null,
    refetchPolicies: vi.fn(),
    savePolicy: vi.fn(async (p: Policy) => p),
    publishPolicy: vi.fn(async () => policyWith('fin_mgr')),
    closePolicy: vi.fn(),
    setSettingsTab: vi.fn(),
    nav: vi.fn(),
    ...over,
  } as unknown as PlatformCtx
}

/**
 * The draft-write control, matched on its exact label: `/^Save/` also passes the retired
 * 'Save & publish', so it would not notice a revert.
 */
function saveButton(): HTMLButtonElement {
  const buttons = Array.from(document.querySelectorAll('button')).filter((b) => (b.textContent ?? '').trim() === 'Save draft')
  expect(buttons, 'no Save draft control rendered').toHaveLength(1)
  return buttons[0]
}

afterEach(cleanup)

describe('APPR-04-06 AC5/AC6: the builder must not read an unlanded roles fetch as "deleted"', () => {
  it('renders the shared Loading surface, not the canvas, while ctx.rolesState is loading', () => {
    render(<WorkflowBuilder ctx={builderCtx({ rolesState: 'loading', roles: [] })} policy={policyWith('fin_mgr')} />)

    expect(document.querySelector('.apic-loading-spin'), 'the shared Loading surface must render').toBeTruthy()
    // No node tree at all while the fetch is unlanded, not merely a hidden warning -- the
    // guard must not thread an empty `roles` list down to the canvas/simulator.
    expect(screen.queryByText('Invoice submitted'), 'the canvas must not render while roles are unlanded').toBeNull()
    // Regex, not an exact string: the canvas joins the resolved sub-line with the SLA text
    // ("Role no longer exists · within 24h") in one text node -- a substring match is the
    // only thing that can see the sentence inside it either way.
    expect(screen.queryByText(/Role no longer exists/)).toBeNull()
    expect(screen.queryByText('Deleted role must approve')).toBeNull()
  })

  it('renders the shared ErrorState, not the canvas, when the roles fetch failed', () => {
    render(
      <WorkflowBuilder
        ctx={builderCtx({ rolesState: 'error', rolesError: new ApiError('http', 'roles gateway is down', 503), roles: [] })}
        policy={policyWith('fin_mgr')}
      />,
    )

    expect(screen.getByText('roles gateway is down'), 'the shared ErrorState must render the fetch failure').toBeTruthy()
    expect(screen.queryByText(/Role no longer exists/)).toBeNull()
    expect(screen.queryByText('Deleted role must approve')).toBeNull()
  })

  it('still names a genuinely deleted role once the roles fetch lands empty -- the guard must not swallow the real state', () => {
    render(<WorkflowBuilder ctx={builderCtx({ rolesState: 'ready', roles: [] })} policy={policyWith('fin_mgr')} />)

    expect(screen.getByText('Deleted role must approve')).toBeTruthy()
    expect(screen.getByText(/Role no longer exists/)).toBeTruthy()
  })
})

describe('APPR-04-06 QA: the guard must not over-widen to the genuinely-landed-empty status', () => {
  // The spec above uses rolesState:'ready' as its "landed empty" stand-in, but a REAL empty
  // roles fetch reports 'empty' (resolveStatus's isEmpty default, async-state.ts), never
  // 'ready'. Verified a guard mutated to also gate on 'empty' passes all 3 pre-existing
  // specs in this file untouched -- this is the spec that actually pins the guard's two
  // conditions (loading/idle only, not empty).
  it('rolesState "empty" renders the canvas, not Loading -- and still names the deleted role', () => {
    render(<WorkflowBuilder ctx={builderCtx({ rolesState: 'empty', roles: [] })} policy={policyWith('fin_mgr')} />)

    expect(document.querySelector('.apic-loading-spin'), 'a landed-empty fetch is not a loading fetch').toBeNull()
    expect(screen.getByText('Deleted role must approve')).toBeTruthy()
    expect(screen.getByText(/Role no longer exists/)).toBeTruthy()
  })
})

describe('APPR-04-06 QA: the error guard fires before any node-shape assumption', () => {
  it('a policy with zero nodes still renders ErrorState, not a crash, when the roles fetch failed', () => {
    const emptyPolicy: Policy = { id: 'p2', name: 'Empty policy', scope: 'All invoices', status: 'draft', version: 1, activeVersion: null, nodes: [] }

    expect(() =>
      render(
        <WorkflowBuilder
          ctx={builderCtx({ rolesState: 'error', rolesError: new ApiError('http', 'roles gateway is down', 503), roles: [] })}
          policy={emptyPolicy}
        />,
      ),
    ).not.toThrow()

    expect(screen.getByText('roles gateway is down')).toBeTruthy()
    expect(screen.queryByText(/Role no longer exists/)).toBeNull()
  })
})

// ============================================================================
// APPR-09-03 QA (task-507) — saving must not seal a version
// ============================================================================
// Publishing is now a separate server verb that SEALS a version and takes the tenant's
// single active slot. Wiring it into the one existing button would re-publish on every
// Save, silently overriding whichever policy is in force — [save-and-publish-are-two-verbs].
// Subtask 05 splits the control; until then the button must only save.
//
// Verified by mutation: `save()` changed to `void ctx.savePolicy(policy).then(() =>
// ctx.publishPolicy(policy.id))` passed all 2026 app tests and a clean tsc before this spec.

describe('APPR-09-03 QA: the save control writes a draft and nothing else', () => {
  it('clicking Save draft calls savePolicy once and publishPolicy never', () => {
    const savePolicy = vi.fn(async (p: Policy) => p)
    const publishPolicy = vi.fn(async () => policyWith('fin_mgr'))
    const policy = policyWith('fin_mgr')

    render(<WorkflowBuilder ctx={builderCtx({ savePolicy, publishPolicy })} policy={policy} />)
    fireEvent.click(saveButton())

    // Positive first: a spec that only asserted the absence would pass on a button that
    // was wired to nothing at all.
    expect(savePolicy).toHaveBeenCalledTimes(1)
    expect(savePolicy).toHaveBeenCalledWith(policy)
    expect(publishPolicy, 'Save sealed a version — it must not publish').not.toHaveBeenCalled()
  })

  // The companion spec here — 'an ordinary edit writes a draft too' — asserted the behaviour
  // AC-1 removes, and named subtask 05 as its own sunset. It is now
  // 'typing in the policy name makes no request' below; its published fixture and its
  // demotion guard moved with it, into the AC-1 and AC-2 specs.
})

// ============================================================================
// APPR-09-05 (task-509) — Save and Publish are two verbs
// ============================================================================
// RED, written before the split. Every pinned sentence below is a LITERAL: asserting a
// component's own exported const against itself proves nothing about the copy, and a named
// import of a not-yet-added export cannot even collect.
//
// Sunset here, per their own comments: APPR-09-04's 'no control in the builder claims to
// publish' (this subtask adds one, by design) — replaced by the two-write-controls spec —
// and APPR-09-03's edit-writes-a-draft spec, replaced by AC-1.

const PUBLISH_BLOCKED_REASON = 'Save your changes first — Publish seals the last saved draft.'
const PUBLISH_SEALED_REASON = 'This policy has no unpublished changes — edit and save a draft to publish again.'
const NO_POLICY_IN_FORCE = 'No policy is in force. Publishing puts this one in force.'
const DELEGATION_NOT_STORED = 'Delegation is not stored yet — this choice is not saved.'

const FIRM_ROLES: Role[] = [
  { key: 'fin_mgr', title: 'Engagement Manager', desc: 'First sign-off on a client invoice', members: [] },
  { key: 'cfo', title: 'Engagement Partner', desc: 'Signs off invoices above ₦1bn', members: [] },
]

/**
 * Either label: the control names the verb in flight, so a publish round trip reads
 * 'Publishing…'. Alternation rather than `/^Publish/`, which a future 'Publish anyway' would
 * also pass. `saveButton()` stays pinned to the resting label — no call site queries it mid-save,
 * and a save that renamed it mid-PUBLISH is exactly the false claim this split removes.
 */
function publishButton(): HTMLButtonElement {
  return screen.getByRole('button', { name: /^(Publish|Publishing…)$/ }) as HTMLButtonElement
}

/** The colour a hint carries, whether the string sits in the styled node or in a child span. */
function hintColor(el: HTMLElement): string {
  return el.style.color || (el.parentElement?.style.color ?? '')
}

/** A second policy on the tenant, holding the single active slot unless told otherwise. */
function otherPolicy(over: Partial<Policy> = {}): Policy {
  return { id: 'p2', name: 'Legacy approvals', scope: 'All invoices', status: 'published', version: 3, activeVersion: 3, nodes: [], ...over }
}

describe('APPR-09-05 AC-1: an edit is local until a write verb is clicked', () => {
  it('typing in the policy name makes no request', () => {
    const savePolicy = vi.fn(async (p: Policy) => p)
    const publishPolicy = vi.fn(async () => policyWith('fin_mgr'))

    // PUBLISHED on purpose, kept from the spec this replaces: the only guard against a
    // reducer re-introducing the demotion `touch` used to apply.
    const live: Policy = { ...policyWith('fin_mgr'), status: 'published', activeVersion: 1 }
    render(<WorkflowBuilder ctx={builderCtx({ savePolicy, publishPolicy })} policy={live} />)
    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })

    expect(savePolicy, 'a keystroke wrote to the server — this is the lost-update path, not just reordering').not.toHaveBeenCalled()
    expect(publishPolicy).not.toHaveBeenCalled()
    // The needle under both absences: the keystroke must reach the working tree, or a
    // component that dropped the edit entirely would satisfy them.
    expect((screen.getByLabelText('Policy name') as HTMLInputElement).value, 'the keystroke never reached the working tree').toBe('Renamed policy')
  })
})

describe('APPR-09-05 AC-2: Save draft writes the working tree and re-seeds from the answer', () => {
  it('saving hands the whole working policy to the write funnel exactly once', () => {
    const savePolicy = vi.fn(async (p: Policy) => p)
    const publishPolicy = vi.fn(async () => policyWith('fin_mgr'))
    const live: Policy = { ...policyWith('fin_mgr'), status: 'published', activeVersion: 1 }

    render(<WorkflowBuilder ctx={builderCtx({ savePolicy, publishPolicy })} policy={live} />)
    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })
    fireEvent.click(saveButton())

    expect(savePolicy).toHaveBeenCalledTimes(1)
    expect(savePolicy.mock.calls[0][0].name).toBe('Renamed policy')
    // The reducers do not demote: the server decides what a save does to a version.
    expect(savePolicy.mock.calls[0][0].status).toBe('published')
    expect(savePolicy.mock.calls[0][0].nodes, 'the write funnel took a partial tree').toEqual(live.nodes)
    expect(publishPolicy).not.toHaveBeenCalled()
  })

  it("a saved policy is re-seeded from the server's response, and the selection is cleared", async () => {
    const stored = policyWith('fin_mgr')
    // A different step id AND a different role: the id churn is what forces the selection to
    // clear (every id is re-minted on each PUT), the role is what proves the canvas re-rendered
    // from the RESPONSE rather than from the unchanged `policy` prop.
    const landed: Policy = { ...stored, name: 'Server name', nodes: [{ id: 'srv-9', type: 'approval', role: 'cfo', sla: '48', delegate: false }] }
    const savePolicy = vi.fn(async () => landed)

    render(<WorkflowBuilder ctx={builderCtx({ savePolicy, roles: FIRM_ROLES })} policy={stored} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    expect(screen.getByText('Approval step'), 'the click selected nothing, so the clearing below is vacuous').toBeTruthy()

    fireEvent.click(saveButton())

    expect(await screen.findByText('Engagement Partner must approve'), 'the canvas still renders the pre-save tree').toBeTruthy()
    expect(screen.queryByText('Engagement Manager must approve')).toBeNull()
    expect((screen.getByLabelText('Policy name') as HTMLInputElement).value).toBe('Server name')
    expect(screen.getByText('Select a step in the flow to edit who approves and when.'), 'a selection survived the id re-mint').toBeTruthy()
  })

  it('the selection and the armed step clear even when the ids come back unchanged', async () => {
    // [selection-clears-on-save] is a DECISION, and the spec above cannot tell it from an
    // accident: once the ids churn, `findNode(working, selId)` misses and the inspector shows
    // the no-selection card whether or not `selId` was ever cleared. Handing back the same ids
    // is the only fixture that separates the two.
    const stored = policyWith('fin_mgr')
    const savePolicy = vi.fn(async (p: Policy) => ({ ...p, name: 'Server name' }))
    render(<WorkflowBuilder ctx={builderCtx({ policies: [stored], savePolicy, roles: FIRM_ROLES })} policy={stored} />)

    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    fireEvent.click(screen.getByRole('button', { name: 'Move Engagement Manager must approve' }))
    expect(screen.getByText('Approval step'), 'nothing was selected, so the clearing below is vacuous').toBeTruthy()
    expect(screen.getByRole('button', { name: 'Move Engagement Manager must approve' }).getAttribute('aria-pressed'), 'nothing was armed').toBe('true')

    fireEvent.click(saveButton())

    expect(await screen.findByText('Select a step in the flow to edit who approves and when.'), 'the selection survived a save that returned the same ids').toBeTruthy()
    expect(screen.getByRole('button', { name: 'Move Engagement Manager must approve' }).getAttribute('aria-pressed'), 'the armed step survived the save, and would place into a tree the server has since rewritten').toBe('false')
  })
})

describe('APPR-09-05 AC-3: Publish is a separate verb, gated on a saved tree', () => {
  it('Publish is disabled with a stated reason while there are unsaved edits', () => {
    const publishPolicy = vi.fn(async () => policyWith('fin_mgr'))
    render(<WorkflowBuilder ctx={builderCtx({ publishPolicy })} policy={policyWith('fin_mgr')} />)

    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })

    const publish = publishButton()
    expect(publish.disabled, 'Publish would seal a tree the server has never seen').toBe(true)
    expect(screen.getByText(PUBLISH_BLOCKED_REASON), 'the control is dead with no reason on screen').toBeTruthy()
    fireEvent.click(publish)
    expect(publishPolicy).not.toHaveBeenCalled()
  })

  it('Publish is enabled once the working tree matches the last server response', () => {
    const publishPolicy = vi.fn(async () => policyWith('fin_mgr'))
    render(<WorkflowBuilder ctx={builderCtx({ publishPolicy })} policy={policyWith('fin_mgr')} />)

    const publish = publishButton()
    expect(publish.disabled).toBe(false)
    // The needle under the spec above: a reason that never leaves would satisfy it too.
    expect(screen.queryByText(PUBLISH_BLOCKED_REASON), 'the blocked reason shows on a clean tree').toBeNull()

    fireEvent.click(publish)
    expect(publishPolicy).toHaveBeenCalledTimes(1)
    expect(publishPolicy).toHaveBeenCalledWith('p1')
  })

  it('an edit that changes no content still counts as unsaved', () => {
    // The one case that separates `working !== server` from a structural compare, and the
    // reason the reference-equality idiom is pinned rather than merely preferred: `clearSteps`
    // returns a new object unconditionally (workflows.ts:269-271), so clearing an ALREADY
    // empty tree is dirty. Conservative on purpose — the cost is one redundant Save.
    //
    // NOTE for the reader who expects the architect's stated failure mode: a JSON compare does
    // NOT read "dirty forever" after a save, because `save()` assigns ONE object to both
    // states. It reads CLEAN too often, and this is where.
    const empty: Policy = { ...policyWith('fin_mgr'), nodes: [] }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [empty] })} policy={empty} />)
    expect(publishButton().disabled, 'the tree is already unsaved, so the flip below is vacuous').toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Clear steps' }))

    expect(publishButton().disabled, 'a no-content edit read as saved — `dirty` is not reference equality').toBe(true)
    expect(screen.getByText(PUBLISH_BLOCKED_REASON)).toBeTruthy()
  })

  it("Publish re-enables after a save whose answer is not what was sent", async () => {
    // The spec below resolves a tree structurally equal to the one it sent, so a `dirty` that
    // deep-compared would pass it. The real server re-mints every step id on every PUT draft
    // (policies.ts:18), so the landed tree NEVER equals the sent one — this is that save.
    const stored = policyWith('fin_mgr')
    const savePolicy = vi.fn(async (p: Policy) => ({ ...p, version: 2, nodes: [{ id: 'srv-remint', type: 'approval' as const, role: 'fin_mgr', sla: '24', delegate: false }] }))
    render(<WorkflowBuilder ctx={builderCtx({ policies: [stored], savePolicy })} policy={stored} />)

    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })
    expect(publishButton().disabled, 'the edit did not mark the tree dirty, so the re-enable below is vacuous').toBe(true)

    fireEvent.click(saveButton())

    await waitFor(() =>
      expect(publishButton().disabled, 'a re-minted answer read as dirty — `dirty` must be reference equality, not a structural compare').toBe(false),
    )
    expect(screen.queryByTestId('publish-blocked-reason')).toBeNull()
  })

  it('Publish re-enables after a save lands', async () => {
    // The one spec that catches a re-seed which CLONES. `dirty` is reference equality, so
    // `setServer({ ...saved })` leaves Publish permanently dead — and every other AC-3 spec
    // still passes, because they only exercise mount-clean and edit-dirty.
    const savePolicy = vi.fn(async (p: Policy) => ({ ...p, name: 'Renamed policy' }))
    render(<WorkflowBuilder ctx={builderCtx({ savePolicy })} policy={policyWith('fin_mgr')} />)

    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })
    expect(publishButton().disabled, 'the edit did not mark the tree dirty, so the re-enable below is vacuous').toBe(true)

    fireEvent.click(saveButton())

    await waitFor(() =>
      expect(publishButton().disabled, 'a landed save left Publish dead — the re-seed must assign ONE object to both states').toBe(false),
    )
    expect(screen.queryByText(PUBLISH_BLOCKED_REASON)).toBeNull()
  })
})

describe('APPR-09-05 AC-4: Publish states its consequence before the click', () => {
  it('Publish names the policy it is about to replace', () => {
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self, otherPolicy()] })} policy={self} />)

    expect(screen.getByText('Publishing replaces «Legacy approvals», which is in force now. There is no undo.')).toBeTruthy()
    expect(screen.queryByText(NO_POLICY_IN_FORCE)).toBeNull()
  })

  it("Publish says no policy is in force when the tenant's slot is empty", () => {
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self, otherPolicy({ status: 'draft', activeVersion: null })] })} policy={self} />)

    expect(screen.getByText(NO_POLICY_IN_FORCE)).toBeTruthy()
    expect(screen.queryByText(/Legacy approvals/), 'the note named a policy that holds nothing').toBeNull()
  })

  it('Publish names THIS policy when it holds the slot itself', () => {
    // policyInForce excludes self by design (policies.ts:193-195), so with only the story's two
    // branches the policy that IS in force renders the false 'No policy is in force'.
    //
    // 'v2 in force · v3 draft' — the ONLY shape in which this branch is publishable.
    // `approval_policy_versions_active_is_sealed` makes active imply sealed (engine.go:130-131),
    // so `activeVersion === version` would mean the top version is sealed, and a sealed policy
    // has nothing to publish at all (see the sealed-gate describe below).
    const self: Policy = { ...policyWith('fin_mgr'), status: 'draft', version: 3, activeVersion: 2 }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self] })} policy={self} />)

    expect(publishButton().disabled, 'the branch under test is only reachable on a publishable policy').toBe(false)
    expect(screen.getByText('Publishing replaces v2 of this policy, which is in force now. There is no undo.')).toBeTruthy()
    expect(screen.queryByText(NO_POLICY_IN_FORCE), 'the note claims nothing is in force while this policy holds the slot').toBeNull()
  })

  it('the note survives a ctx.policies that has not landed yet', () => {
    // Unreachable through the app — WorkflowsView renders the builder only for a policy it
    // FOUND in ctx.policies (WorkflowsView.tsx:30), so the list always holds at least this one.
    // Asserted anyway because the component takes the list as a prop and must not throw on [].
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [] })} policy={self} />)

    expect(screen.getByText(NO_POLICY_IN_FORCE)).toBeTruthy()
    expect(publishButton().disabled, 'an empty list must not read as a blocked publish').toBe(false)
  })
})

// ----------------------------------------------------------------------------
// APPR-09-05 Stage 4 QA — the second publish gate
// ----------------------------------------------------------------------------
// `dirty` alone left Publish LIVE on a policy with nothing to publish. `status === 'published'`
// is exactly "the top version is sealed" (policy_store.go:49-58), and the publish handler
// selects `WHERE policy_id = $1 AND NOT sealed` (:566-575) -> ErrPolicyNothingToPublish ->
// 409 'this policy has no unpublished changes' (policy.go:397-398). One click reached it:
// publish, then click Publish again.
//
// Two defects, not one: a live control for an act that cannot succeed, AND a consequence note
// that kept promising 'Publishing replaces v{n} of this policy' above it.

describe('APPR-09-05 QA AC-3: a sealed version is the second thing that blocks Publish', () => {
  it('a policy opened already published cannot be published again', () => {
    const publishPolicy = vi.fn(async () => policyWith('fin_mgr'))
    // activeVersion === version: 'v2 in force', the state the list renders after a publish.
    const sealed: Policy = { ...policyWith('fin_mgr'), status: 'published', version: 2, activeVersion: 2 }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [sealed], publishPolicy })} policy={sealed} />)

    const publish = publishButton()
    expect(publish.disabled, 'Publish is live on a policy whose every version is sealed — the click earns a 409').toBe(true)
    expect(screen.getByText(PUBLISH_SEALED_REASON), 'the control is dead with no reason on screen').toBeTruthy()
    expect(publish.title, 'title is an ADDITION to the visible node, and must carry the same string').toBe(PUBLISH_SEALED_REASON)

    fireEvent.click(publish)
    expect(publishPolicy, 'the click reached the gateway for a publish the server refuses').not.toHaveBeenCalled()
  })

  it('the consequence note stops promising a replacement it cannot perform', () => {
    const sealed: Policy = { ...policyWith('fin_mgr'), status: 'published', version: 2, activeVersion: 2 }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [sealed] })} policy={sealed} />)

    expect(screen.queryByTestId('publish-consequence'), 'the note promises an irreversible act that cannot run').toBeNull()
    expect(screen.queryByText(/There is no undo/), 'a no-undo warning survives on a control that cannot fire').toBeNull()
    expect(screen.queryByText(NO_POLICY_IN_FORCE), 'the sealed policy reads as if nothing governed').toBeNull()
  })

  it('a sealed policy that lost the tenant slot is still not publishable', () => {
    // The branch a `activeVersion !== null` gate would miss: sealed here, in force elsewhere.
    const sealed: Policy = { ...policyWith('fin_mgr'), status: 'published', version: 2, activeVersion: null }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [sealed, otherPolicy()] })} policy={sealed} />)

    expect(publishButton().disabled).toBe(true)
    expect(screen.getByText(PUBLISH_SEALED_REASON)).toBeTruthy()
    expect(screen.queryByText(/Publishing replaces «Legacy approvals»/), 'the note promises to displace a policy this control cannot displace').toBeNull()
  })

  it('an unsaved edit outranks the seal, because saving clears both', () => {
    // Ordering, not just presence: `PUT .../draft` always answers an unsealed top version
    // (policy_store.go:464-468), so Save is the single remedy and the reason must say so.
    const sealed: Policy = { ...policyWith('fin_mgr'), status: 'published', version: 2, activeVersion: 2 }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [sealed] })} policy={sealed} />)
    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })

    expect(screen.getByText(PUBLISH_BLOCKED_REASON)).toBeTruthy()
    expect(screen.queryByText(PUBLISH_SEALED_REASON), 'the reason names the seal while the remedy on screen is Save').toBeNull()
    expect(screen.queryAllByTestId('publish-blocked-reason'), 'two reasons render at once').toHaveLength(1)
  })

  it('saving a sealed policy re-opens Publish, because the save mints a new draft', async () => {
    const sealed: Policy = { ...policyWith('fin_mgr'), status: 'published', version: 2, activeVersion: 2 }
    // What `PUT .../draft` really answers: a fresh unsealed v3 over the still-active v2.
    const savePolicy = vi.fn(async (p: Policy) => ({ ...p, status: 'draft' as const, version: 3 }))
    render(<WorkflowBuilder ctx={builderCtx({ policies: [sealed], savePolicy })} policy={sealed} />)
    expect(publishButton().disabled, 'the policy is already publishable, so the re-open below is vacuous').toBe(true)

    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(publishButton().disabled, 'a landed draft left Publish sealed shut').toBe(false))
    expect(screen.queryByTestId('publish-blocked-reason')).toBeNull()
    expect(screen.getByText('Publishing replaces v2 of this policy, which is in force now. There is no undo.'), 'the note never came back').toBeTruthy()
  })
})

describe('APPR-09-05: a landed publish re-seeds the builder too', () => {
  it('the pill reports the seal and the note stops claiming nothing is in force', async () => {
    const self = policyWith('fin_mgr')
    const sealed: Policy = { ...self, status: 'published', version: 1, activeVersion: 1 }
    const publishPolicy = vi.fn(async () => sealed)

    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy })} policy={self} />)
    expect(screen.getByText(NO_POLICY_IN_FORCE), 'the pre-publish note is already wrong, so the flip below proves nothing').toBeTruthy()
    expect(screen.getByText('DRAFT')).toBeTruthy()

    fireEvent.click(publishButton())

    // The `policy` prop never changes here, deliberately: WorkflowsView keys the builder on the
    // policy id, so App's post-publish refetch re-renders it and never remounts it.
    expect(await screen.findByText('PUBLISHED'), 'the pill still reads DRAFT on a policy that is now sealed').toBeTruthy()
    expect(screen.queryByText(NO_POLICY_IN_FORCE)).toBeNull()
  })

  it('the selection SURVIVES a publish, because no step id churned', async () => {
    // The counterpart to [selection-clears-on-save], and the only guard on it: `POST
    // .../publish` seals a version and rewrites no step (policy_store.go:566-575), so the ids
    // the inspector holds are still live. Clearing here would be a gratuitous copy of save().
    const self = policyWith('fin_mgr')
    const publishPolicy = vi.fn(async () => ({ ...self, status: 'published' as const, version: 1, activeVersion: 1 }))
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy, roles: FIRM_ROLES })} policy={self} />)

    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    expect(screen.getByText('Approval step'), 'nothing was selected, so the survival below is vacuous').toBeTruthy()

    fireEvent.click(publishButton())
    await screen.findByText('PUBLISHED')

    expect(screen.getByText('Approval step'), 'the publish cleared a selection whose ids it never touched').toBeTruthy()
    expect(screen.queryByText('Select a step in the flow to edit who approves and when.')).toBeNull()
  })

  it('the second click is refused here, not by the gateway', async () => {
    // The whole defect in one flow. Before the seal gate, `dirty` was false after the re-seed,
    // so Publish stayed live for an act that answers 409 'this policy has no unpublished
    // changes' — under a note still promising 'Publishing replaces v1 of this policy'.
    const self = policyWith('fin_mgr')
    const publishPolicy = vi.fn(async () => ({ ...self, status: 'published' as const, version: 1, activeVersion: 1 }))

    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy })} policy={self} />)
    expect(publishButton().disabled, 'the policy was never publishable, so the seal below proves nothing').toBe(false)
    expect(screen.getByTestId('publish-consequence')).toBeTruthy()

    fireEvent.click(publishButton())
    await screen.findByText('PUBLISHED')

    expect(publishButton().disabled, 'Publish is still live one click after the version it would publish was sealed').toBe(true)
    expect(screen.getByText(PUBLISH_SEALED_REASON)).toBeTruthy()
    expect(screen.queryByTestId('publish-consequence'), 'the note still promises to replace a version the server will not replace').toBeNull()

    fireEvent.click(publishButton())
    expect(publishPolicy, 'the second click reached the gateway and earned a 409').toHaveBeenCalledTimes(1)
  })
})

describe('APPR-09-05 AC-5: a refused write states the server sentence at the control', () => {
  // The rejection carries its own catch: today's write is a bare `void ctx.savePolicy(...)`,
  // and an unhandled rejection would fail the whole FILE rather than this spec's assertion.
  function refusing(err: ApiError) {
    return vi.fn(() => {
      const p = Promise.reject(err)
      p.catch(() => {})
      return p
    })
  }

  it("a refused publish renders the server's 409 verbatim", async () => {
    const refusal = 'an approval step names a workflow role that no longer exists'
    render(<WorkflowBuilder ctx={builderCtx({ publishPolicy: refusing(new ApiError('http', refusal, 409)) })} policy={policyWith('fin_mgr')} />)

    fireEvent.click(publishButton())

    expect(await screen.findByText(refusal)).toBeTruthy()
    expect(screen.queryByText(/something went wrong/i), 'generic client copy stood in for the server sentence').toBeNull()
  })

  it("a refused save renders the server's 403 verbatim and never flashes Saved", async () => {
    const refusal = 'only an admin can change approval policies'
    render(<WorkflowBuilder ctx={builderCtx({ savePolicy: refusing(new ApiError('http', refusal, 403)) })} policy={policyWith('fin_mgr')} />)

    fireEvent.click(saveButton())

    expect(await screen.findByText(refusal)).toBeTruthy()
    expect(screen.queryByText('Saved'), 'a refused PUT still flashed Saved').toBeNull()
  })
})

describe('APPR-09-05 AC-6: a role with no active holder still blocks, loudly', () => {
  // REGRESSION GUARD, not a RED spec: the resolve threading already ships and node provenance
  // is invisible to the builder, so this passes the moment it is written. Its value is catching
  // a local working-tree refactor that drops the `line(inspectorResolve)` closure.
  it('the inspector names the blocked holder in amber once the step is selected', () => {
    const roles: Role[] = [{ key: 'fin_mgr', title: 'Engagement Manager', desc: 'First sign-off on a client invoice', members: ['m1'] }]
    const members: Member[] = [{ id: 'm1', name: 'Ada Obi', initials: 'AO', email: null, role: 'reviewer', status: 'suspended', isYou: false }]

    render(<WorkflowBuilder ctx={builderCtx({ roles, members })} policy={policyWith('fin_mgr')} />)
    // Selected first: the sentence comes from inspectorResolve, and the canvas's blocked arm
    // is the bare holder name in amber, not this sentence.
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    expect(hintColor(screen.getByText('Currently: Ada Obi — this step will block'))).toBe('var(--status-amber-text)')
  })
})

describe('APPR-09-05 AC-7: an off-vocabulary deadline stays visible', () => {
  it('the stored 36-hour deadline renders as the selected option, not a blank select', () => {
    const odd: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '36', delegate: false }] }

    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={odd} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const deadline = screen.getByLabelText('Deadline') as HTMLSelectElement
    const stored = Array.from(deadline.options).find((o) => o.value === '36')
    expect(stored, 'the stored deadline has no option, so the select renders blank').toBeTruthy()
    // Sibling register inside one dropdown: SLA_OPTIONS' own wording, not slaText's 'within 36h'.
    expect(stored?.textContent).toBe('Within 36 hours')
    expect(deadline.value, 'the stored deadline is not the selected one').toBe('36')
    expect(Array.from(deadline.options).filter((o) => o.value === '36'), 'the passthrough duplicated an option').toHaveLength(1)
  })
})

describe('APPR-09-05 AC-9: the delegation controls say the choice is not stored', () => {
  it('the note is visible with the toggle OFF, and the toggle stays interactive', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    // getByText never matches a title/aria attribute, so finding it IS the visible-node assertion.
    const note = screen.getByText(DELEGATION_NOT_STORED)
    expect(hintColor(note), 'a layer-3 reason renders grey, not amber').toBe('var(--fg-3)')

    const toggle = screen.getByRole('switch', { name: 'Allow delegation' }) as HTMLButtonElement
    expect(toggle.disabled, 'APPR-10 owns disabling this control, not APPR-09').toBe(false)
  })

  it('the note renders once, not twice, when delegation is on', () => {
    const on: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true }] }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={on} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    expect(screen.getAllByText(DELEGATION_NOT_STORED), 'the note sits both inside and outside the delegate guard').toHaveLength(1)
    // The picker's own eligibility note is a different sentence and must survive.
    expect(screen.getByText('Only members with the Reviewer access role can be a delegate.')).toBeTruthy()
  })
})

// ----------------------------------------------------------------------------
// APPR-09-05 Stage 4 QA — adversarial coverage
// ----------------------------------------------------------------------------

/** A promise the spec resolves by hand, so two writes can genuinely overlap. */
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('APPR-09-05 QA: two writes, one after the other', () => {
  // RE-AUTHORED by APPR-09-06 (task-510). This spec used to click Save while a publish was
  // still in flight, which AC-5's `submitting` guard makes impossible — the second click is
  // now inert by design, so the spec was unfixable-green. The property it exists for is
  // untouched: the gate reads `server`, it does not LATCH on the seal. The two answers now
  // land sequentially, which is the only order the guard leaves reachable.
  it('the gate follows the LAST landed answer, and does not latch on the seal', async () => {
    const self = policyWith('fin_mgr')
    const pub = deferred<Policy>()
    const sav = deferred<Policy>()
    const publishPolicy = vi.fn(() => pub.promise)
    const savePolicy = vi.fn(() => sav.promise)

    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy, savePolicy })} policy={self} />)
    fireEvent.click(publishButton())
    expect(publishPolicy).toHaveBeenCalledTimes(1)

    // The publish lands first and seals.
    pub.resolve({ ...self, status: 'published', version: 1, activeVersion: 1 })
    await waitFor(() => expect(publishButton().disabled).toBe(true))
    expect(screen.getByText(PUBLISH_SEALED_REASON)).toBeTruthy()

    // Then a save, whose own answer — a fresh unsealed v2 — re-opens the gate. A gate that
    // LATCHED on the seal rather than reading `server` would stay shut here.
    fireEvent.click(saveButton())
    expect(savePolicy, 'the save never fired, so the re-open below proves nothing about the gate').toHaveBeenCalledTimes(1)
    sav.resolve({ ...self, status: 'draft', version: 2, activeVersion: 1 })
    await waitFor(() => expect(publishButton().disabled, 'the gate latched instead of reading the last landed row').toBe(false))
    expect(screen.queryByTestId('publish-blocked-reason')).toBeNull()
    expect(screen.getByText('Publishing replaces v1 of this policy, which is in force now. There is no undo.')).toBeTruthy()
    // Neither verb wrote the other's slot.
    expect(screen.queryByTestId('policy-save-error')).toBeNull()
    expect(screen.queryByTestId('policy-publish-error')).toBeNull()
  })
})

// ----------------------------------------------------------------------------
// APPR-09-06 (task-510) AC-5 — the in-flight guard
// ----------------------------------------------------------------------------
// `save()`/`publish()` re-seed `working` from the answer, so a keystroke typed inside the round
// trip used to be silently overwritten. Before subtask 05 it could not happen — every keystroke
// was its own PUT. The remedy is one `submitting` flag disabling the form for the duration
// (RoleModal.tsx:74,:203; EntityFormModal.tsx:64; MemberDrawer.tsx:272), cleared in `finally`.
// The spec these replace — 'a re-seed that lands after an edit DISCARDS it' — recorded the race
// and said in its own comment that a real guard must REPLACE it rather than work around it.
//
// TRANSIENT, not the four-layer disabled recipe: MemberDrawer.tsx:269-272 writes that rule down
// — `UnbackedField` "always renders a reason note, and a staffing write in flight has none to
// show". A write in flight gets a bare `disabled`, no reason note and no `aria-describedby`.

/**
 * jsdom's `.disabled` IDL property reflects the CONTENT ATTRIBUTE only, so a control inside a
 * `<fieldset disabled>` still reports `false`. This walks the ancestry the way the HTML spec's
 * "actually disabled" definition does, so the specs below hold whether the guard lands on the
 * control itself or on a wrapping fieldset — the trade MemberDrawer.tsx:64-71 pre-authorises for
 * `WfSelect`, which carries no `disabled` prop of its own (WorkflowParts.tsx:216-226).
 */
function inert(el: Element): boolean {
  if ((el as HTMLInputElement).disabled) return true
  return el.closest('fieldset[disabled]') !== null
}

function nameInput(): HTMLInputElement {
  return screen.getByLabelText('Policy name') as HTMLInputElement
}

/**
 * A `hideLabel` WfSelect carries its `aria-label` on the <label> WRAPPER (WorkflowParts.tsx:226),
 * so RTL hands back the wrapper rather than the control. Descend when that happens, or the
 * assertions below would read a fieldset ancestor and miss a `disabled` on the select itself.
 */
function control(el: HTMLElement): HTMLElement {
  return el.tagName === 'LABEL' ? ((el.querySelector('select, input, button') as HTMLElement) ?? el) : el
}

/** One palette tile, matched on its own second line rather than a shared word like 'Approval'. */
function paletteTile(): HTMLElement {
  return screen.getByText('Someone must sign off').closest('button')!
}

describe('APPR-09-06 AC-5: the form is inert while a write is in flight', () => {
  it('every control that mutates the working tree is inert inside a save round trip', () => {
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    render(
      <WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise), roles: FIRM_ROLES })} policy={self} />,
    )
    // Selected BEFORE the write, so the inspector renders real controls rather than its
    // no-selection card — otherwise the last target below has nothing to find.
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    // THUNKS, not held elements: wrapping the canvas or the scope row in a new `<fieldset>`
    // remounts that subtree, and a node captured beforehand would be detached — `closest`
    // on a detached node answers null, which would read as a spurious failure.
    const targets: [string, () => Element][] = [
      ['the policy name', () => nameInput()],
      ['the scope select', () => control(screen.getByLabelText('Applies'))],
      ['Clear steps', () => screen.getByRole('button', { name: 'Clear steps' })],
      ['the palette', () => paletteTile()],
      ['the canvas move handle', () => screen.getByRole('button', { name: 'Move Engagement Manager must approve' })],
      ['the inspector deadline', () => screen.getByLabelText('Deadline')],
    ]
    expect(targets.length, 'nothing was checked').toBeGreaterThan(0)
    // Every query resolves, and every control is live, BEFORE the write — so the flips below
    // are neither vacuous nor a broken selector.
    for (const [what, get] of targets) {
      expect(inert(get()), `${what} was already inert before the write, so the flip below is vacuous`).toBe(false)
    }

    // Held rather than re-queried: `saveButton()` matches the exact label, and this control
    // takes its own `disabled` — it is not inside any wrapper that could remount it.
    const save = saveButton()
    fireEvent.click(save)

    for (const [what, get] of targets) {
      expect(inert(get()), `${what} still rewrites the working tree while a save is in flight`).toBe(true)
    }
    expect(inert(save), 'Save draft still fires a second PUT mid-flight').toBe(true)
  })

  it('the simulator and the way out stay live — neither loses a keystroke', () => {
    // ALREADY GREEN on write: the over-widening guard for the two controls the guard must NOT
    // reach. `WorkflowSimulator` writes only local `sim` state, and leaving ABANDONS a write
    // rather than silently overwriting one, which is the only thing AC-5 names.
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    fireEvent.click(saveButton())

    expect(inert(control(screen.getByLabelText('Doc type'))), 'the simulator writes no server state and must stay usable').toBe(false)
    expect(inert(screen.getByRole('button', { name: /All policies/ })), 'the guard trapped the user inside the builder').toBe(false)
  })

  it('a second Save click inside the round trip never reaches the gateway', () => {
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    const savePolicy = vi.fn(() => sav.promise)
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy })} policy={self} />)

    const save = saveButton()
    fireEvent.click(save)
    fireEvent.click(save)

    expect(savePolicy, 'the second click fired a second PUT, whose answer re-seeds over the first').toHaveBeenCalledTimes(1)
  })

  it('a publish in flight shuts Publish WITHOUT claiming the tree is unsaved', () => {
    // `blockedReason` is null here — the tree is clean and the top version is not yet sealed —
    // so `|| submitting` is what shuts `disabled`, while `title`, `aria-describedby` and the
    // visible note stay keyed on `blockedReason` ALONE: 'Save your changes first' is untrue
    // mid-publish, and stating the wrong reason is worse than stating none.
    const self = policyWith('fin_mgr')
    const pub = deferred<Policy>()
    const publishPolicy = vi.fn(() => pub.promise)
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy })} policy={self} />)
    expect(publishButton().disabled, 'Publish was already shut, so the flip below is vacuous').toBe(false)

    fireEvent.click(publishButton())

    expect(publishButton().disabled, 'a second Publish click reaches the gateway and earns a 409').toBe(true)
    expect(publishPolicy).toHaveBeenCalledTimes(1)
    fireEvent.click(publishButton())
    expect(publishPolicy, 'the guard disabled the control but did not guard the handler').toHaveBeenCalledTimes(1)
    // The whole point of keeping the reason on `blockedReason`: a transient lock has no reason
    // to state, and stating the wrong one is worse than stating none.
    expect(screen.queryByTestId('publish-blocked-reason'), 'a publish in flight renders "Save your changes first", which is untrue').toBeNull()
    expect(publishButton().title, 'the tooltip states a reason that does not apply mid-publish').toBeFalsy()
    expect(publishButton().getAttribute('aria-describedby')).toBeNull()
    // The publish path locks the same form the save path does.
    expect(inert(nameInput()), 'a keystroke typed inside a publish is overwritten by its re-seed').toBe(true)
  })

  it('a landed save re-opens the form', async () => {
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    fireEvent.click(saveButton())
    expect(inert(nameInput()), 'the form never locked, so the re-open below is vacuous').toBe(true)

    sav.resolve({ ...self, name: 'Server name' })

    await waitFor(() => expect(inert(nameInput()), 'the form stayed dead after the write landed').toBe(false))
    expect(nameInput().value, 'the re-seed never reached the field').toBe('Server name')
    expect(inert(control(screen.getByLabelText('Applies'))), 'the scope select stayed dead after the write landed').toBe(false)
  })

  it('a REFUSED save re-opens the form over its error slot', async () => {
    // The `finally` clause, not the success path: a rejection that left `submitting` true would
    // strand the user in a dead form with the server's reason and no way to act on it.
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    const refusal = 'only an admin can change approval policies'
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    const save = saveButton()
    fireEvent.click(save)
    expect(inert(nameInput()), 'the form never locked, so the re-open below is vacuous').toBe(true)

    sav.reject(new ApiError('http', refusal, 403))

    expect(await screen.findByText(refusal)).toBeTruthy()
    expect(inert(nameInput()), 'a refused write left the form permanently dead').toBe(false)
    expect(inert(save), 'the user cannot retry the write that was just refused').toBe(false)
  })

  // --------------------------------------------------------------------------
  // QA (Stage 4) — adversarial coverage the RED set did not carry
  // --------------------------------------------------------------------------

  it('a refused save can be RETRIED, and the retry reaches the gateway', async () => {
    // Re-opening the form is half the property; the other half is that the flag did not merely
    // flip a style. MemberDrawer.test.tsx:163's shape — the rejection clears and the second
    // write goes through.
    const self = policyWith('fin_mgr')
    const refusal = 'only an admin can change approval policies'
    const savePolicy = vi
      .fn()
      .mockRejectedValueOnce(new ApiError('http', refusal, 403))
      .mockResolvedValueOnce({ ...self, name: 'Server name' })
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy })} policy={self} />)

    fireEvent.click(saveButton())
    expect(await screen.findByText(refusal)).toBeTruthy()

    fireEvent.click(saveButton())

    await waitFor(() => expect(nameInput().value, 'the retry never landed').toBe('Server name'))
    expect(savePolicy, 'the flag never cleared, so the retry never reached the gateway').toHaveBeenCalledTimes(2)
    expect(screen.queryByTestId('policy-save-error'), 'the stale refusal survived a successful retry').toBeNull()
  })

  it('a REFUSED publish re-opens the form too — the `finally` is on BOTH verbs', () => {
    // `save()` and `publish()` carry their own try/finally. A `finally` present on one and
    // missing on the other strands the user in a dead form on exactly one of the two refusals,
    // and every spec above would still be green.
    const self = policyWith('fin_mgr')
    const pub = deferred<Policy>()
    const refusal = 'this policy is already published'
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy: vi.fn(() => pub.promise) })} policy={self} />)

    fireEvent.click(publishButton())
    expect(inert(nameInput()), 'the publish never locked the form, so the re-open below is vacuous').toBe(true)

    pub.reject(new ApiError('http', refusal, 409))

    return screen.findByText(refusal).then(() => {
      expect(inert(nameInput()), 'a refused publish left the form permanently dead').toBe(false)
      expect(publishButton().disabled, 'the user cannot retry the publish that was just refused').toBe(false)
    })
  })

  it('ONE flag covers both verbs — Publish is inert inside a SAVE round trip', () => {
    // Cross-verb, which no spec above reaches: the publish specs lock publish-during-publish and
    // the save specs lock save-during-save. A second flag per verb would pass both and still let
    // a publish fire against a tree the save is about to re-seed.
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    const publishPolicy = vi.fn()
    render(
      <WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise), publishPolicy })} policy={self} />,
    )
    // The tree is clean and the top version unsealed, so `blockedReason` is null and Publish is
    // open on its own terms — without this, `|| submitting` would not be what shuts it.
    expect(publishButton().disabled, 'Publish was already shut, so the flip below is vacuous').toBe(false)

    fireEvent.click(saveButton())

    expect(publishButton().disabled, 'a publish can fire against a tree the in-flight save is about to re-seed').toBe(true)
    fireEvent.click(publishButton())
    expect(publishPolicy).not.toHaveBeenCalled()
    // Same rule as the publish path: a transient lock states no reason, and 'Save your changes
    // first' is not the reason here either.
    expect(screen.queryByTestId('publish-blocked-reason')).toBeNull()
  })

  it('a write still in flight at unmount neither warns nor throws', async () => {
    // `setSubmitting(false)` in `finally` fires on a component that is gone. React 18 dropped the
    // "setState on an unmounted component" warning, so this pins the console rather than assuming
    // a warning would surface — a leaked timer or a stray throw here would be invisible otherwise.
    const errors: unknown[][] = []
    const spy = vi.spyOn(console, 'error').mockImplementation((...args: unknown[]) => void errors.push(args))
    try {
      const self = policyWith('fin_mgr')
      const sav = deferred<Policy>()
      render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

      fireEvent.click(saveButton())
      expect(inert(nameInput()), 'the form never locked, so unmounting mid-flight proves nothing').toBe(true)

      cleanup()
      sav.resolve({ ...self, name: 'Server name' })
      await new Promise((r) => setTimeout(r, 0))

      expect(errors, `a write landing after unmount logged: ${JSON.stringify(errors)}`).toHaveLength(0)
    } finally {
      spy.mockRestore()
    }
  })

  // AUTHORED-VALUE pin, NOT a rendered check. jsdom applies no stylesheet and runs no layout,
  // so this can only hold what the component asks for. It is here because the three wrappers
  // are the one part of AC-5 whose correctness is geometric: a fieldset's UA defaults are a
  // 2px groove border, ~0.35em/0.75em padding, a 2px inline margin and `min-inline-size:
  // min-content` — the last of which would refuse to shrink inside the canvas column's
  // `minmax(360px, 1fr)`. The `display: flex` belongs to the scope row alone: `WfSelect`'s root
  // is `inline-block` (WorkflowParts.tsx:226) and was blockified for free as a direct flex item
  // of that row; a block wrapper hands it back its line box, and the descender nudges the select
  // off centre against the `Applies` label beside it. The rendered check is owed at the deploy
  // gate. Verified statically alongside this: no `<legend>` and no anchor inside any of the
  // three subtrees (a disabled fieldset reaches neither), and no CSS in `frontend/app/src/styles`
  // or `packages/design-tokens` selects `fieldset` at all.
  it('the three in-flight wrappers carry the shipped fieldset reset, and only the scope row is a flex box', () => {
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], roles: FIRM_ROLES })} policy={self} />)

    const wrappers = Array.from(document.querySelectorAll('fieldset'))
    expect(wrappers, 'the scope row, the canvas and the inspector').toHaveLength(3)

    for (const fs of wrappers) {
      const s = (fs as HTMLElement).style
      expect([s.border, s.padding, s.margin], `a wrapper kept a fieldset UA default: ${fs.getAttribute('style')}`).toEqual([
        '0px',
        '0px',
        '0px',
      ])
      expect(s.minInlineSize, 'min-content would refuse to shrink inside the canvas column').toBe('0')
      expect(fs.querySelector('legend'), 'a legend re-introduces the notch the reset removes').toBeNull()
      expect(fs.querySelector('a[href]'), '`disabled` does not reach an anchor, so one inside would stay live').toBeNull()
    }

    const flex = wrappers.filter((fs) => (fs as HTMLElement).style.display === 'flex')
    expect(flex, 'exactly one wrapper blockifies its child').toHaveLength(1)
    expect(flex[0].contains(screen.getByLabelText('Applies')), 'the flex wrapper is not the one around WfSelect').toBe(true)
  })
})

// ----------------------------------------------------------------------------
// APPR-09-06 follow-up — the in-flight lock states that it is in flight
// ----------------------------------------------------------------------------
// AC-5's `disabled` lands with no pending affordance: no `:disabled` rule exists in
// frontend/app/src/styles/platform.css or packages/design-tokens/app-layer.css, and
// `.asc-app .v2-btn-ghost` sets an explicit `color` (app-layer.css:214), so the UA grey never
// paints. The form silently froze. Both precedents AC-5 cites flip their label instead
// (RoleModal.tsx:382, EntityFormModal.tsx:208), which is what these pin.

describe('APPR-09-06 follow-up: each write control names the verb in flight', () => {
  it('Save draft reads Saving… for the round trip, and Publish keeps its resting label', async () => {
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    // Held: the control is a direct child of the header row, so no render detaches it — and
    // `saveButton()` matches the RESTING label, which is the point of the assertion below.
    const save = saveButton()
    expect(screen.queryByText('Saving…'), 'the pending label is already on screen, so the flip below is vacuous').toBeNull()

    fireEvent.click(save)

    expect(save.textContent, 'the form locked with nothing on screen saying a write is running').toBe('Saving…')
    // The cross-verb half: ONE flag locks both controls, but only one verb is running.
    expect(publishButton().textContent, 'Publish claimed a publish that is not running').toBe('Publish')

    sav.resolve({ ...self, name: 'Server name' })

    await waitFor(() => expect(screen.queryByText('Saving…'), 'the pending label outlived the write').toBeNull())
    expect(save.textContent, 'the landed-write flash never replaced the pending label').toBe('Saved')
  })

  it('Publish reads Publishing… for the round trip, and Save draft keeps its resting label', async () => {
    const self = policyWith('fin_mgr')
    const pub = deferred<Policy>()
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy: vi.fn(() => pub.promise) })} policy={self} />)

    const publish = publishButton()
    expect(publish.disabled, 'Publish is already shut, so the click below starts no write').toBe(false)
    expect(screen.queryByText('Publishing…'), 'the pending label is already on screen, so the flip below is vacuous').toBeNull()

    fireEvent.click(publish)

    // Without this, a Publish shut by an in-flight write is indistinguishable from one shut by
    // `blockedReason` — and the latter is the only one that carries a visible reason.
    expect(publish.textContent, 'the form locked with nothing on screen saying a write is running').toBe('Publishing…')
    expect(saveButton().textContent, 'Save draft claimed a save that is not running').toBe('Save draft')

    pub.resolve({ ...self, status: 'published', version: 1, activeVersion: 1 })

    await waitFor(() => expect(publish.textContent, 'the pending label outlived the write').toBe('Publish'))
    expect(screen.queryByText('Publishing…')).toBeNull()
  })

  it('a REFUSED write drops the pending label too — the `finally` clears it on both verbs', async () => {
    // Settling is not landing. A label cleared only on the success path leaves a refused write
    // reading 'Saving…' forever, over an error slot the user cannot act on.
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    const refusal = 'only an admin can change approval policies'
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    const save = saveButton()
    fireEvent.click(save)
    expect(save.textContent, 'the label never flipped, so the clear below is vacuous').toBe('Saving…')

    sav.reject(new ApiError('http', refusal, 403))

    expect(await screen.findByText(refusal)).toBeTruthy()
    expect(save.textContent, 'a refused write is still claiming to be in flight').toBe('Save draft')
  })
})

describe('APPR-09-05 QA AC-5: the two error slots survive each other', () => {
  function rejecting(err: ApiError) {
    return vi.fn(() => Promise.reject(err))
  }

  it('a refused save does not blank a publish reason already on screen', async () => {
    const publishRefusal = 'an approval step names a workflow role that no longer exists'
    const saveRefusal = 'only an admin can change approval policies'
    const savePolicy = rejecting(new ApiError('http', saveRefusal, 403))
    const ctx = builderCtx({ publishPolicy: rejecting(new ApiError('http', publishRefusal, 409)), savePolicy })

    render(<WorkflowBuilder ctx={ctx} policy={policyWith('fin_mgr')} />)
    fireEvent.click(publishButton())
    expect(await screen.findByText(publishRefusal)).toBeTruthy()

    fireEvent.click(saveButton())
    expect(await screen.findByText(saveRefusal)).toBeTruthy()
    expect(screen.getByText(publishRefusal), 'the save wrote the publish slot — one slot, not two').toBeTruthy()
    expect(screen.getByTestId('policy-save-error').textContent).toBe(saveRefusal)
    expect(screen.getByTestId('policy-publish-error').textContent).toBe(publishRefusal)
  })

  it('a landed save clears only its own slot', async () => {
    const publishRefusal = 'an approval step names a workflow role that no longer exists'
    const self = policyWith('fin_mgr')
    const ctx = builderCtx({ publishPolicy: rejecting(new ApiError('http', publishRefusal, 409)), savePolicy: vi.fn(async (p: Policy) => p) })

    render(<WorkflowBuilder ctx={ctx} policy={self} />)
    fireEvent.click(publishButton())
    expect(await screen.findByText(publishRefusal)).toBeTruthy()

    fireEvent.click(saveButton())
    expect(await screen.findByText('Saved')).toBeTruthy()
    expect(screen.getByText(publishRefusal), 'a landed save erased a publish failure the user never acknowledged').toBeTruthy()
    expect(screen.queryByTestId('policy-save-error')).toBeNull()
  })
})

describe('APPR-09-05 QA: a write that lands after the builder is gone', () => {
  it('unmounting mid-await logs nothing and throws nothing', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const sav = deferred<Policy>()
    const self = policyWith('fin_mgr')
    const view = render(<WorkflowBuilder ctx={builderCtx({ savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    fireEvent.click(saveButton())
    view.unmount()
    sav.resolve({ ...self, name: 'Landed after unmount' })
    await sav.promise
    await Promise.resolve()

    expect(spy, `a state write after unmount logged: ${spy.mock.calls.map((c) => String(c[0])).join(' | ')}`).not.toHaveBeenCalled()
    spy.mockRestore()
  })
})

describe('APPR-09-05 QA: the Saved flash never outlives the tree it describes', () => {
  it('an edit ends the flash, so Saved and the blocked reason never co-render', async () => {
    // The cc79eff deviation: `applyEdit` clears `saved`. Without it the header reads 'Saved'
    // beside 'Save your changes first' for the rest of the 1700ms — two claims, one surface.
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(async (p: Policy) => p) })} policy={self} />)

    fireEvent.click(saveButton())
    expect(await screen.findByText('Saved')).toBeTruthy()
    expect(screen.queryByText(PUBLISH_BLOCKED_REASON), 'the tree is dirty before the edit, so the pairing below is vacuous').toBeNull()

    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })

    expect(screen.getByText(PUBLISH_BLOCKED_REASON)).toBeTruthy()
    expect(screen.queryByText('Saved'), "'Saved' still claims a landed write beside 'Save your changes first'").toBeNull()
    expect(saveButton()).toBeTruthy()
  })
})

describe('APPR-09-05 QA AC-9: the not-stored note is one node, in every toggle state', () => {
  it('flipping the toggle twice never duplicates or drops the note', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    const toggle = screen.getByRole('switch', { name: 'Allow delegation' })

    for (const expected of ['on', 'off', 'on']) {
      fireEvent.click(toggle)
      expect(toggle.getAttribute('aria-checked'), 'the click did not flip the toggle, so the count below is vacuous').toBe(expected === 'on' ? 'true' : 'false')
      expect(screen.getAllByText(DELEGATION_NOT_STORED), `the note is not exactly one node with delegation ${expected}`).toHaveLength(1)
    }
  })
})

describe('APPR-09-05: the builder carries exactly two write controls', () => {
  it('one Save draft and one Publish, and nothing else claims either verb', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    const buttons = Array.from(document.querySelectorAll('button'))
    expect(buttons.length, 'the builder rendered no controls, so the counts below are vacuous').toBeGreaterThan(0)
    expect(buttons.filter((b) => /save/i.test(b.textContent ?? '')), 'the builder lost its save control, or grew a second one').toHaveLength(1)
    expect(buttons.filter((b) => /publish/i.test(b.textContent ?? '')), 'the builder lost its Publish control, or grew a second one').toHaveLength(1)
  })
})
