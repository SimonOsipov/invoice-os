// @vitest-environment jsdom
//
// RED specs for APPR-04-06 AC5/AC6: WorkflowBuilder does not read ctx.rolesState at all
// today, so an unlanded roles fetch renders every approval step as if its role were
// genuinely deleted -- roleOf's fallback (lib/roles.ts:63) can't tell "not fetched yet"
// from "fetched and gone". The guard belongs HERE, not in WorkflowsView (which forwards
// ctx whole and reads no role data) -- see the story's [D-BUILDER-GUARD].
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import type { Member } from '../lib/members'
import type { Role } from '../lib/roles'
import { CATCH_ALL_SCOPES, SIM_DEFAULT, type NotifyNode, type Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { WorkflowBuilder } from './WorkflowBuilder'
// Rendered bare by the R15 oracle only — every other spec reaches them through the builder.
import { WfSelect, WfToggle } from './WorkflowParts'

function policyWith(role: string): Policy {
  return {
    id: 'p1',
    name: 'Test policy',
    position: 0,
    scopes: CATCH_ALL_SCOPES,
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
    refetchRolesInForce: vi.fn(),
    // The four policy fields the ctx gained in APPR-09-03. Present so a reader can never
    // see `undefined` — the double cast below disables property checking, so nothing else
    // would say they are missing.
    policies: [],
    policiesState: 'ready',
    policiesError: null,
    refetchPolicies: vi.fn(),
    entities: [],
    entitiesState: 'ready',
    refetchEntities: vi.fn(),
    reorderPolicies: vi.fn(),
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
    const emptyPolicy: Policy = { id: 'p2', name: 'Empty policy', position: 0, scopes: CATCH_ALL_SCOPES, status: 'draft', version: 1, activeVersion: null, nodes: [] }

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
// Publishing is now a separate server verb that SEALS a version and becomes its
// policy's active version. Wiring it into the one existing button would re-publish on every
// Save, silently re-publishing the policy — [save-and-publish-are-two-verbs].
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
    expect(savePolicy).toHaveBeenCalledWith(policy, { sendScopes: false })
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

/** A second policy on the tenant, published unless told otherwise. */
function otherPolicy(over: Partial<Policy> = {}): Policy {
  return { id: 'p2', name: 'Legacy approvals', position: 0, scopes: CATCH_ALL_SCOPES, status: 'published', version: 3, activeVersion: 3, nodes: [], ...over }
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
    // returns a new object unconditionally (workflows.ts:257-259), so clearing an ALREADY
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
  it('Publish says the policies in force stay in force', () => {
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self, otherPolicy()] })} policy={self} />)

    expect(screen.getByText('Publishing puts this policy in force. The policies in force now stay in force. There is no undo.')).toBeTruthy()
    expect(screen.queryByText(/Publishing replaces «Legacy approvals»/)).toBeNull()
    expect(screen.queryByText(NO_POLICY_IN_FORCE)).toBeNull()
  })

  it('Publish says no policy is in force when no policy has an active version', () => {
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self, otherPolicy({ status: 'draft', activeVersion: null })] })} policy={self} />)

    expect(screen.getByText(NO_POLICY_IN_FORCE)).toBeTruthy()
    expect(screen.queryByText(/Legacy approvals/), 'the note named a policy that holds nothing').toBeNull()
  })

  it('Publish names THIS policy when it is the one in force', () => {
    // policyInForce excludes self by design (policies.ts:192-194), so with only the story's two
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
    expect(screen.queryByText(NO_POLICY_IN_FORCE), 'the note claims nothing is in force while this policy is in force').toBeNull()
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

  it('a sealed policy with no version in force is still not publishable', () => {
    // The branch a `activeVersion !== null` gate would miss: sealed here, in force elsewhere.
    const sealed: Policy = { ...policyWith('fin_mgr'), status: 'published', version: 2, activeVersion: null }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [sealed, otherPolicy()] })} policy={sealed} />)

    expect(publishButton().disabled).toBe(true)
    expect(screen.getByText(PUBLISH_SEALED_REASON)).toBeTruthy()
    expect(screen.queryByText(/The policies in force now stay in force/), 'the note promises a publish this control cannot run').toBeNull()
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
    expect(publishButton().disabled, 'the seal left Publish open, so the re-open below is vacuous').toBe(true)

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

/** An approval policy whose step already delegates; `to` names the delegate, '' means anyone. */
function delegating(to = ''): Policy {
  return { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true, delegateTo: to }] }
}

function switchDelegationOn() {
  fireEvent.click(screen.getByRole('switch', { name: 'Allow delegation' }))
}

function delegatePicker(): HTMLSelectElement {
  return control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
}

/** The eligibility note under the picker, by its exact text. */
function delegateNote(): HTMLElement {
  return screen.getByText('Only Admins and Reviewers can be a delegate.')
}

describe('DELEG-01-05: delegation controls', () => {
  it('delegation controls are enabled like their siblings', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    switchDelegationOn()

    const toggle = screen.getByRole('switch', { name: 'Allow delegation' }) as HTMLButtonElement
    const picker = delegatePicker()
    for (const el of [toggle, picker]) {
      expect(inert(el), 'a delegation control is shut').toBe(false)
      expect(el.getAttribute('title')).toBeNull()
      expect(el.getAttribute('aria-describedby')).toBeNull()
    }
    expect((toggle.parentElement as HTMLElement).style.opacity).toBe('')
  })

  it('the shut-state reason is gone and the note is the eligibility rule', () => {
    const { unmount } = render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    expect(screen.queryByText(/Delegation is switched off/)).toBeNull()
    expect(screen.queryByTestId('delegation-blocked-reason')).toBeNull()
    unmount()

    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={delegating()} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    expect(screen.queryByText(/Delegation is switched off/)).toBeNull()
    expect(screen.queryByTestId('delegation-blocked-reason')).toBeNull()
    expect(delegateNote().textContent).toBe('Only Admins and Reviewers can be a delegate.')
  })

  it('the picker follows the switch, and switching on preselects anyone', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    expect(screen.queryByLabelText('Delegate to')).toBeNull()

    switchDelegationOn()
    expect(delegatePicker().value).toBe('')
  })

  it('switching off and on again drops an earlier named delegate', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={delegating('m2')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    switchDelegationOn()
    expect(screen.queryByLabelText('Delegate to')).toBeNull()
    switchDelegationOn()
    expect(delegatePicker().value).toBe('')
  })
})

// ----------------------------------------------------------------------------
// APPR-10-03 — the delegate copy names the approver set {admin, reviewer}
// ----------------------------------------------------------------------------

describe('APPR-10-03 AC-3: the sentinel option names the approver set', () => {
  it('the default option names both approver roles, and stays worded apart from the note', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={delegating()} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    const sentinel = Array.from(picker.options).find((o) => o.value === '')
    expect(sentinel, "the picker lost its '' sentinel option, so the label below cannot be read").toBeTruthy()

    const label = sentinel!.textContent ?? ''
    expect(/Admin/.test(label), `the fallback option does not name the Admin role: ${label}`).toBe(true)
    expect(/Reviewer/.test(label), `the fallback option does not name the Reviewer role: ${label}`).toBe(true)

    // the option names the FALLBACK, the note states the
    // ELIGIBILITY RULE. Two registers, deliberately not harmonised into one sentence.
    const note = delegateNote().textContent ?? ''
    expect(label, 'the option and the note collapsed into one sentence').not.toBe(note)
    expect(note.includes(label), 'the note swallowed the option label verbatim').toBe(false)
    expect(label.includes(note), 'the option label swallowed the note verbatim').toBe(false)
  })
})

describe('DELEG-01-05: the picker offers active approvers by member id', () => {
  // Reviewer BEFORE admin, so role order and roster order disagree.
  const ROSTER: Member[] = [
    { id: 'm1', name: 'Folake Adesina', initials: 'FA', email: null, role: 'preparer', status: 'active', isYou: false },
    { id: 'm2', name: 'Musa Danjuma', initials: 'MD', email: null, role: 'reviewer', status: 'active', isYou: false },
    { id: 'm3', name: 'Chinedu Okafor', initials: 'CO', email: null, role: 'admin', status: 'active', isYou: false },
    { id: 'm4', name: 'Halima Yusuf', initials: 'HY', email: null, role: 'reviewer', status: 'suspended', isYou: false },
  ]

  it('lists the anyone option, then the active reviewer and admin in roster order, valued by id', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES, members: ROSTER })} policy={delegating()} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const options = Array.from(delegatePicker().options)
    expect(options.map((o) => o.value)).toEqual(['', 'm2', 'm3'])
    expect(options.slice(1).map((o) => o.textContent)).toEqual(['Musa Danjuma', 'Chinedu Okafor'])
  })

  it('an ineligible stored delegate is marked on the card and under the picker, and stays selected by name', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES, members: ROSTER })} policy={delegating('m4')} />)
    const sentence = 'Halima Yusuf can no longer be a delegate on Engagement Manager. Pick another delegate or switch delegation off.'

    const card = screen.getByTestId('step-delegate-ineligible')
    expect(card.textContent).toBe(sentence)
    const sub = card.previousElementSibling as HTMLElement
    expect(sub.textContent, 'the card line does not sit under the sub-line').toContain('within 24h')
    expect(card.parentElement, 'the card line left the sub-line\'s text column').toBe(sub.parentElement)
    expect(card.parentElement?.style.minWidth, 'the text column lost minWidth: 0').toMatch(/^0(px)?$/)
    expect(card.style.whiteSpace, 'the card line copies the sub-line, which wraps').toBe('')

    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    expect(screen.getByTestId('delegate-ineligible').textContent).toBe(sentence)
    const picker = delegatePicker()
    expect(picker.value).toBe('m4')
    expect(Array.from(picker.options).find((o) => o.value === 'm4')?.textContent).toBe('Halima Yusuf')
  })

  it('an eligible delegate and "anyone" show no mark', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES, members: ROSTER })} policy={delegating('m2')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    expect(screen.queryByTestId('step-delegate-ineligible')).toBeNull()
    expect(screen.queryByTestId('delegate-ineligible')).toBeNull()
  })
})

describe('APPR-10-04 QA (R14): the approval panel enumerates its controls', () => {
  // The condition panel got this pin in APPR-10-02 (`the condition panel gained or lost a
  // control`); the approval panel never had one. Dropping the delegate guard adds a control to
  // this card PERMANENTLY, which is exactly the change an enumeration exists to hold.
  it('renders Remove, the role select, Manage roles, the deadline and the switch - the picker only once delegation is on', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const controls = Array.from(inspectorPanel().querySelectorAll('input, select, textarea, button'))
    expect(controls.length, 'the panel query matched nothing, so the list below proves nothing').toBeGreaterThan(0)
    expect(controls.map((c) => [c.tagName, controlName(c)]), 'the approval panel gained or lost a control').toEqual([
      ['BUTTON', 'Remove'],
      ['SELECT', 'Who must approve'],
      ['BUTTON', 'Manage roles'],
      ['SELECT', 'Deadline'],
      ['BUTTON', 'Allow delegation'],
    ])
  })

  // QA (ROUTE-04-03). The enumeration above pins that the control EXISTS; nothing pinned
  // where it goes. onManageRoles moved from the two-call idiom (`ctx.setSettingsTab('roles')`
  // then `ctx.nav('settings')`) to one `ctx.nav('settings', { settingsTab: 'roles' })`.
  it('Manage roles navigates to Settings carrying the roles tab, in one call', () => {
    const ctx = builderCtx({ roles: FIRM_ROLES })
    render(<WorkflowBuilder ctx={ctx} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    fireEvent.click(screen.getByText('Manage roles'))

    expect((ctx.nav as ReturnType<typeof vi.fn>).mock.calls, 'the tab must arrive with the destination').toEqual([
      ['settings', { settingsTab: 'roles' }],
    ])
    expect(ctx.setSettingsTab, 'the separate tab write must be gone').not.toHaveBeenCalled()
  })
})

describe('APPR-10-04 QA (R15): the disabled paint is CONDITIONAL', () => {
  // THE MUTATION ORACLE. An unconditional paint spread inside either shared primitive satisfies
  // every assertion above and silently repaints all nine `WfSelect` call sites — one of them on
  // the Members screen this story never opens (MembersView.tsx). Only
  // this spec sees it. Rendered bare rather than through the builder: after this subtask the app
  // has no ENABLED `WfToggle` left, so the primitive is the only place the resting state exists.
  it('an enabled WfSelect and an enabled WfToggle keep their resting style', () => {
    render(
      <>
        <WfSelect label="Probe select" value="a" options={[{ value: 'a', label: 'A' }]} onChange={() => {}} />
        <WfToggle on={false} onToggle={() => {}} label="Probe toggle" />
      </>,
    )

    const sel = control(screen.getByLabelText('Probe select')) as HTMLSelectElement
    const tog = screen.getByRole('switch', { name: 'Probe toggle' }) as HTMLButtonElement
    expect([sel.disabled, tog.disabled], 'the probes rendered disabled, so the paint below proves nothing').toEqual([false, false])

    expect(sel.style.backgroundColor, 'an enabled WfSelect lost its resting background').toBe('var(--bg-1)')
    expect(sel.style.color, 'an enabled WfSelect lost its resting foreground').toBe('var(--fg-1)')
    expect(tog.style.background, 'an enabled WfToggle lost its resting background').toBe('var(--line-3)')
    expect(tog.style.color, 'an enabled WfToggle grew a foreground it never had').toBe('')
    // The track stays a pill (D3); the knob is a circle.
    expect((tog.querySelector('.pf-knob') as HTMLElement).style.borderRadius, 'the knob is no longer a circle').toBe('50%')
    expect(tog.style.borderRadius, 'the track is no longer a pill').toBe('99px')
    for (const [what, el] of [['WfSelect', sel], ['WfToggle', tog]] as [string, HTMLElement][]) {
      expect(el.style.cursor, `an enabled ${what} paints the not-allowed cursor`).toBe('pointer')
    }
  })
})

// ----------------------------------------------------------------------------
// APPR-10-04 Stage 4 QA — the gaps the AC tests leave open
// ----------------------------------------------------------------------------
// Each block below was written against a MUTATION that survived the whole suite.

describe('APPR-10-04 QA AC-6: the fieldset count holds with a step SELECTED', () => {
  // The pin at :1274 renders the builder and never selects, so the inspector shows its
  // no-selection card and the approval panel is not in the document at all. A `<fieldset>`
  // added AROUND the delegation controls — the exact shortcut P28 exists to forbid — survives
  // it untouched. Measured: with no `disabled` on that wrapper, nothing in the suite failed.
  it('selecting an approval step adds no wrapper — still three, none flex', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    switchDelegationOn()
    // The panel is genuinely mounted, or the count below measures the no-selection card.
    expect(screen.getByLabelText('Delegate to'), 'no approval panel is open, so this counts the wrong tree').toBeTruthy()

    const wrappers = Array.from(document.querySelectorAll('fieldset'))
    expect(wrappers, 'the Applies card, the canvas and the inspector — the delegation recipe adds none').toHaveLength(3)
    expect(wrappers.filter((fs) => (fs as HTMLElement).style.display === 'flex'), 'no wrapper blockifies a child').toHaveLength(0)
    // AC-2's distinction, stated as a tree property rather than per control: nothing inside the
    // open inspector is shut by ancestry, so every `.disabled` above is the control's own.
    const inspectorFieldsets = wrappers.filter((fs) => fs.contains(screen.getByLabelText('Delegate to')) && fs.hasAttribute('disabled'))
    expect(inspectorFieldsets, 'a disabled fieldset now wraps the picker, which AC-2 rules out').toHaveLength(0)
  })
})

describe('APPR-10-04 QA (R8): the recipe lands on the CONTROL, and nowhere else', () => {
  // R8's trap is one-directional in the AC specs: they prove `title`/`aria-describedby` reach
  // the `<select>`, but nothing forbade them ALSO landing on the `<label>` wrapper. Measured:
  // adding both to the wrapper survived the whole suite. A `title` there raises a tooltip over
  // the label text — the one place a disabled control's own tooltip cannot appear — and a second
  // `aria-describedby` announces the sentence twice.
  it('neither attribute leaks onto the <label> wrapper the aria-label lives on', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const wrappers = Array.from(document.querySelectorAll('label'))
    expect(wrappers.length, 'no <label> rendered, so the sweep below proves nothing').toBeGreaterThan(0)
    for (const l of wrappers) {
      expect(l.getAttribute('title'), `a <label> carries the reason as a tooltip: ${l.textContent}`).toBeNull()
      expect(l.getAttribute('aria-describedby'), `a <label> re-announces the reason: ${l.textContent}`).toBeNull()
      expect(l.hasAttribute('disabled'), 'a <label> took the disabled attribute, which does nothing there').toBe(false)
    }
  })
})

describe('DELEG-01-05: the sweep\'s handles exist locally', () => {
  it('the delegate picker sits inside the inspector body the sweep measures', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))
    switchDelegationOn()

    const body = screen.getByTestId('step-inspector-body')
    expect(body.contains(delegatePicker()), 'the picker is not inside the column the sweep measures against').toBe(true)
    expect(body.contains(screen.getByLabelText('Deadline')), 'the Deadline select is not inside the body').toBe(true)
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
// (RoleModal and EntityFormModal `submitting`; MemberDrawer `accessPending`), cleared in `finally`.
// The spec these replace — 'a re-seed that lands after an edit DISCARDS it' — recorded the race
// and said in its own comment that a real guard must REPLACE it rather than work around it.
//
// TRANSIENT, not the four-layer disabled recipe: MemberDrawer.tsx's `rolePending` fieldset comment writes that rule down
// — `UnbackedField` "always renders a reason note, and a staffing write in flight has none to
// show". A write in flight gets a bare `disabled`, no reason note and no `aria-describedby`.

/**
 * jsdom's `.disabled` IDL property reflects the CONTENT ATTRIBUTE only, so a control inside a
 * `<fieldset disabled>` still reports `false`. This walks the ancestry the way the HTML spec's
 * "actually disabled" definition does, so the specs below hold whether the guard lands on the
 * control itself or on a wrapping fieldset. Both paths are live: `WfSelect` and `WfToggle` carry
 * their own `disabled` (WorkflowParts.tsx:199-249, :284-308), and the builder's three fieldsets
 * still lock the scope row, the canvas and the inspector while a write is in flight.
 */
function inert(el: Element): boolean {
  if ((el as HTMLInputElement).disabled) return true
  return el.closest('fieldset[disabled]') !== null
}

function nameInput(): HTMLInputElement {
  return screen.getByLabelText('Policy name') as HTMLInputElement
}

/**
 * A `hideLabel` WfSelect carries its `aria-label` on the <label> WRAPPER (WorkflowParts.tsx:220),
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

    expect(inert(control(screen.getByLabelText('Scenario invoice amount in naira'))), 'the simulator writes no server state and must stay usable').toBe(false)
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
  // is `inline-block` (WorkflowParts.tsx:220) and was blockified for free as a direct flex item
  // of that row; a block wrapper hands it back its line box, and the descender nudges the select
  // off centre against the `Applies` label beside it. The rendered check is owed at the deploy
  // gate. Verified statically alongside this: no `<legend>` and no anchor inside any of the
  // three subtrees (a disabled fieldset reaches neither), and no CSS in `frontend/app/src/styles`
  // or `packages/design-tokens` selects `fieldset` at all.
  it('the three in-flight wrappers carry the shipped fieldset reset, and none is a flex box', () => {
    const self = policyWith('fin_mgr')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], roles: FIRM_ROLES })} policy={self} />)

    const wrappers = Array.from(document.querySelectorAll('fieldset'))
    expect(wrappers, 'the Applies card, the canvas and the inspector').toHaveLength(3)

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

    expect(wrappers.filter((fs) => (fs as HTMLElement).style.display === 'flex'), 'no wrapper blockifies a child').toHaveLength(0)
  })
})

// ----------------------------------------------------------------------------
// APPR-09-06 follow-up — the in-flight lock states that it is in flight
// ----------------------------------------------------------------------------
// AC-5's `disabled` lands with no pending affordance: no `:disabled` rule exists in
// frontend/app/src/styles/platform.css or packages/design-tokens/v2/app-layer.css, and
// `.asc-app .v2-btn-ghost` sets an explicit `color` (v2/app-layer.css), so the UA grey never
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

// ----------------------------------------------------------------------------
// APPR-09-06 follow-up — the paint tracks the lock
// ----------------------------------------------------------------------------
// Measured on the deployed build: mid-write both controls carried `disabled` while rendering
// pixel-identical to their live state — same background, `cursor: pointer`, opacity 1. The
// `disabled` ATTRIBUTE took two causes, the disabled APPEARANCE took one.
//
// These read the INLINE style, not `getComputedStyle`: jsdom applies no stylesheet, which is
// exactly why a green 2137-spec suite never saw this. The inline layer is also where the fix
// belongs — it is what outranks `.v2-btn-ghost:hover` (v2/app-layer.css).

describe('APPR-09-06 follow-up: a control shut by a write in flight is PAINTED shut', () => {
  it('Publish takes the muted paint on the same condition as its `disabled`', () => {
    const self = policyWith('fin_mgr')
    const pub = deferred<Policy>()
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], publishPolicy: vi.fn(() => pub.promise) })} policy={self} />)

    // Held: this control is a direct child of the header row, so no render below detaches it.
    const publish = publishButton()
    // Live AND painted live, or the flip below passes on a control that was never the action
    // colour to begin with — `blockedReason` is null here, so nothing else has muted it.
    expect(publish.disabled, 'Publish is already shut, so the click starts no write').toBe(false)
    expect(publish.style.background, 'Publish is not painted as the action, so the mute below proves nothing').toBe('var(--action)')
    expect(publish.style.color).toBe('var(--primary-foreground)')
    expect(publish.style.cursor, 'a live control carries no inline cursor — `.asc-app .v2-btn` supplies `pointer`').toBe('')

    fireEvent.click(publish)

    expect(publish.disabled, 'the lock never closed, so the paint assertions below are vacuous').toBe(true)
    expect(publish.style.background, 'a dead Publish lost the action fill').toBe('var(--action)')
    expect(publish.style.color, 'a dead Publish lost the on-dark label colour').toBe('var(--primary-foreground)')
    expect(publish.style.opacity, 'a dead Publish is not dimmed').toBe('0.45')
    expect(publish.style.filter).toBe('none')
    expect(publish.style.cursor, 'a dead Publish still invites the click').toBe('not-allowed')
    // The other half of the split, re-pinned HERE so the fix above cannot drag the reason
    // layers along with the paint: the paint tracks BOTH causes, the reason tracks only
    // `blockedReason`. Duplicates the guard at :822 on purpose — that spec is what this
    // change had to survive.
    expect(screen.queryByTestId('publish-blocked-reason'), 'a publish in flight renders "Save your changes first", which is untrue').toBeNull()
    expect(publish.title, 'the tooltip states a reason that does not apply mid-publish').toBeFalsy()
    expect(publish.getAttribute('aria-describedby')).toBeNull()

    pub.resolve({ ...self, status: 'published', version: 1, activeVersion: 1 })
  })

  it('Clear steps takes the ghost mute, where it carried no disabled paint at all', async () => {
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    // Re-queried rather than held, matching the thunks at :753: a future `<fieldset>` around
    // this row would remount the button, and a node captured beforehand would be detached.
    const clear = () => screen.getByRole('button', { name: 'Clear steps' }) as HTMLButtonElement
    expect(clear().disabled, 'Clear steps is already shut, so the flip below is vacuous').toBe(false)
    // A live ghost draws its whole look from `.v2-btn-ghost` — nothing inline to mute.
    expect(clear().style.background, 'a live ghost is painted inline, so the mute below proves nothing').toBe('')
    expect(clear().style.color).toBe('')
    expect(clear().style.cursor).toBe('')

    fireEvent.click(saveButton())

    expect(clear().disabled, 'the lock never closed, so the paint assertions below are vacuous').toBe(true)
    // `transparent` inline is not a no-op: it outranks `.v2-btn-ghost:hover`, which would
    // otherwise light a dead control up under the pointer.
    expect(clear().style.background, 'a dead Clear steps still lights up on hover').toBe('transparent')
    expect(clear().style.borderColor, 'a dead Clear steps keeps the live border weight').toBe('var(--line-1)')
    expect(clear().style.color, 'a dead Clear steps still reads as live text').toBe('var(--fg-4)')
    expect(clear().style.cursor, 'a dead Clear steps still invites the click').toBe('not-allowed')

    sav.resolve({ ...self, name: 'Server name' })
    await waitFor(() => expect(clear().disabled).toBe(false))
    // Idle again, and unpainted again — the mute is tied to the write, not latched by it.
    expect(clear().style.background, 'the mute outlived the write it describes').toBe('')
    expect(clear().style.color).toBe('')
    expect(clear().style.cursor).toBe('')
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

describe('APPR-09-05: the builder carries exactly two write controls', () => {
  it('one Save draft and one Publish, and nothing else claims either verb', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    const buttons = Array.from(document.querySelectorAll('button'))
    expect(buttons.length, 'the builder rendered no controls, so the counts below are vacuous').toBeGreaterThan(0)
    expect(buttons.filter((b) => /save/i.test(b.textContent ?? '')), 'the builder lost its save control, or grew a second one').toHaveLength(1)
    expect(buttons.filter((b) => /publish/i.test(b.textContent ?? '')), 'the builder lost its Publish control, or grew a second one').toHaveLength(1)
  })
})

describe('APSC-04-02: the builder renders no scope-not-routed note and no Applies select', () => {
  it('has neither node', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    expect(screen.queryByTestId('scope-not-routed')).toBeNull()
    expect(screen.queryByLabelText('Applies')).toBeNull()
  })

  it('the trigger reads the scope summary', () => {
    const scoped: Policy = { ...policyWith('fin_mgr'), scopes: { ...CATCH_ALL_SCOPES, document_types: ['credit_note'] } }
    const { unmount } = render(<WorkflowBuilder ctx={builderCtx()} policy={scoped} />)
    expect(screen.getByTestId('canvas-trigger-line').textContent).toBe('TRIGGER · CREDIT NOTES')
    expect(screen.getByTestId('canvas-trigger-line').title).toBe('TRIGGER · CREDIT NOTES')
    unmount()
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)
    expect(screen.getByTestId('canvas-trigger-line').textContent).toBe('TRIGGER · ALL INVOICES')
  })
})

// ----------------------------------------------------------------------------
// APPR-10-02 (task-514) — the condition domain reduces to amount
// ----------------------------------------------------------------------------
// `evalCondition` reads `ctx.amount` alone (workflows.ts:329-337), so the scenario amount is
// the whole scenario and the inspector's field select offers one domain.

const SCENARIO_AMOUNT = 'Scenario invoice amount in naira'

/** The simulator card, scoped off its own header so the query cannot stray into the inspector. */
function simulatorPanel(): HTMLElement {
  return screen.getByText('Test a scenario').parentElement as HTMLElement
}

/** The inspector card, scoped off the header its Remove button lives in. */
function inspectorPanel(): HTMLElement {
  return screen.getByRole('button', { name: 'Remove' }).parentElement!.parentElement as HTMLElement
}

/**
 * What a control is CALLED on screen. A non-`hideLabel` WfSelect names its select through the
 * visible span inside the wrapping <label> (WorkflowParts.tsx:221-225) rather than an
 * aria-label, so neither source alone can enumerate a mixed panel.
 */
function controlName(c: Element): string {
  return c.getAttribute('aria-label') ?? c.closest('label')?.querySelector('span.label')?.textContent ?? c.textContent ?? ''
}

function conditionPolicy(): Policy {
  return {
    id: 'p1',
    name: 'Test policy',
    position: 0,
    scopes: CATCH_ALL_SCOPES,
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'c1', type: 'condition', field: 'amount', op: '>', value: 250_000_000, then: [], else: [] }],
  }
}

/** Selects the one condition on the canvas and hands back the inspector's field select. */
function selectCondition(): HTMLSelectElement {
  // Queried BEFORE the click: selecting mounts the inspector's RULE card, which renders the
  // same sentence a second time.
  fireEvent.click(screen.getByText('Amount greater than ₦250,000,000'))
  const el = control(screen.getByLabelText('If this field'))
  expect(el.tagName, 'the field handle is not a <select>, so its option list cannot be read').toBe('SELECT')
  return el as HTMLSelectElement
}

describe('APPR-10-02 AC-5: the simulator drops the two inputs nothing reads', () => {
  it('renders neither a Doc type control nor a new-customer switch', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    // The simulator is on screen at all — without this the two absences below are vacuous.
    expect(screen.getByLabelText(SCENARIO_AMOUNT), 'the simulator never mounted').toBeTruthy()
    expect(screen.queryByLabelText('Doc type'), 'the scenario doc-type select still renders').toBeNull()
    expect(screen.queryByLabelText('Scenario is a new customer'), 'the scenario new-customer switch still renders').toBeNull()
  })

  it('offers exactly one input — the scenario amount', () => {
    // The positive half of AC-5. Nothing in the repo pinned what the simulator renders, so an
    // absence check alone would still pass over a panel that had grown a fourth control.
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const controls = Array.from(simulatorPanel().querySelectorAll('input, select, textarea, button'))
    expect(controls.length, 'the panel query matched nothing, so the count below proves nothing').toBeGreaterThan(0)
    expect(controls.map((c) => c.getAttribute('aria-label')), 'the simulator carries an input beyond the amount').toEqual([SCENARIO_AMOUNT])
  })
})

describe('APPR-10-02 AC-4: the condition inspector offers the amount field alone', () => {
  it('the field select has exactly one option', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={conditionPolicy()} />)

    const opts = Array.from(selectCondition().options)
    expect(opts.length, 'the field select rendered no options at all').toBeGreaterThan(0)
    expect(opts.map((o) => [o.value, o.textContent]), 'the retired domains are still selectable').toEqual([['amount', 'Invoice amount']])
  })

  // Over-removal guard, never a RED: both retired render arms are gone from the source, and
  // before that a fresh condition defaulted to `field: 'amount'` and never mounted them. It
  // fails if the sweep takes the operator select — the control that must survive the amount.
  it('renders no Equals control, and exactly one Is control', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={conditionPolicy()} />)
    selectCondition()

    expect(screen.queryByLabelText('Equals'), 'a second domain arm came back').toBeNull()
    expect(screen.getAllByLabelText('Is'), 'the operator select is the only Is the condition panel may carry').toHaveLength(1)
  })

  // APPR-10-02 QA. AC-4 names four things the panel MUST render and one it must not; the two
  // specs above close the must-not and the option count only. This enumerates the whole card,
  // so a sweep that took the amount input or a preset fails here rather than shipping.
  it('renders the field select, the operator select, the amount input and the three presets — and nothing else', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={conditionPolicy()} />)
    selectCondition()

    const controls = Array.from(inspectorPanel().querySelectorAll('input, select, textarea, button'))
    expect(controls.length, 'the panel query matched nothing, so the list below proves nothing').toBeGreaterThan(0)
    expect(controls.map((c) => [c.tagName, controlName(c)]), 'the condition panel gained or lost a control').toEqual([
      ['BUTTON', 'Remove'],
      ['SELECT', 'If this field'],
      ['SELECT', 'Is'],
      ['INPUT', 'Threshold amount in naira'],
      ['BUTTON', '₦100M'],
      ['BUTTON', '₦500M'],
      ['BUTTON', '₦1B'],
    ])
  })
})

describe('APPR-10-02 QA: the one-key scenario is copied, never mutated in place', () => {
  it('typing an amount leaves the shipped SIM_DEFAULT at ₦750,000,000', () => {
    // `sim` is seeded from the module constant itself (WorkflowBuilder.tsx:149), so an onSim
    // that assigned into `sim` instead of spreading it would rewrite SIM_DEFAULT for every
    // later render and every later test in the process. Cheap to pin, silent when it breaks.
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    fireEvent.change(screen.getByLabelText(SCENARIO_AMOUNT), { target: { value: '900000000' } })

    expect((screen.getByLabelText(SCENARIO_AMOUNT) as HTMLInputElement).value, 'the scenario amount never took the keystroke').toBe('900,000,000')
    expect(SIM_DEFAULT, 'the simulator wrote through to the module constant').toEqual({ amount: 750_000_000 })
  })
})

// ----------------------------------------------------------------------------
// APPR-10-05 (task-517) — notify names what it does not deliver
// ----------------------------------------------------------------------------
// THE SUBJECT IS DELIVERY, NEVER STORAGE (R18). The authored target and channel ARE persisted —
// columns (migrations/20260809210326_approval_policies.sql:98-99), Go Step/stepInput/stepRow
// (internal/approval/policy.go:26-27, :88-89, :120-121), TS write (lib/policies.ts:148) and read
// (:133), materialised onto the run step as `skipped` (internal/approval/engine.go:175-182).
// Nothing dispatches them: the repo carries no mail, SMS or push transport. So copy reading
// "not saved" or "does nothing" would be a NEW false statement, which is what these specs forbid.
//
// Both sentences are located by TESTID and never by literal: the wording is the executor's to write, and a spec that imported it would
// follow a typo into the product.
//
// G2: the inspector and the simulator co-render in one sticky column (WorkflowBuilder.tsx:519, :533),
// and `nodeTitle`/`simTitle` both return `Notify ${target}` (WorkflowParts.tsx:79, :100). Every query
// below is panel-scoped for that reason — a bare `screen.getByText` throws on this fixture.

const NOTIFY_CLAIM_ID = 'notify-not-delivered'
const SIM_NOTIFY_CLAIM_ID = 'sim-notify-not-delivered'

/** Names the act being denied — a message going out, not a value going down. */
const DELIVERY_ACT = /\b(sent|send|sends|sending|deliver|delivers|delivered|delivery|notif\w+|message|alert|email|reminder)\b/i
/** Denies it. */
const DENIAL = /\b(no|not|nothing|never|nobody|isn['’]?t|aren['’]?t|doesn['’]?t|don['’]?t|won['’]?t|cannot|can['’]?t|yet)\b/i
/** Affirms the authored value is kept — R18's positive half. */
const STORAGE_AFFIRMED = /\b(saved?|stored?|records?|recorded|kept|keeps|persist\w*|retained)\b/i
/** The claims R18 rules out: every one of these is false of a notify step. */
const STORAGE_DENIED = /\b(not saved|isn['’]?t saved|never saved|not stored|isn['’]?t stored|not recorded|not kept|does nothing|no effect|is ignored|is discarded|is dropped)\b/i

/** A root-level notify, so `simulate` always walks it (lib/workflows.ts:363-366). */
function notifyPolicy(): Policy {
  return {
    ...policyWith('fin_mgr'),
    nodes: [
      { id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false },
      { id: 'n2', type: 'notify', target: 'Tax Team', channel: 'In-app' },
    ],
  }
}

/** `nodeTitle`'s notify arm — the canvas card AND the simulator row both render this string. */
const NOTIFY_CARD = 'Notify Tax Team'

/** The canvas fieldset, anchored on the trigger card the canvas always renders. */
function canvasPanel(): HTMLElement {
  const fs = screen.getByText('Invoice submitted').closest('fieldset')
  expect(fs, 'the canvas sits in no fieldset, so this scope is not the canvas').toBeTruthy()
  return fs as HTMLElement
}

/** Selects the notify step FROM THE CANVAS — `screen.getByText(NOTIFY_CARD)` matches the simulator row too. */
function selectNotify(): void {
  fireEvent.click(within(canvasPanel()).getByText(NOTIFY_CARD))
}

/** The sentence under test, or null while it has not been written. */
function claimIn(panel: HTMLElement, testid: string): HTMLElement | null {
  return within(panel).queryByTestId(testid)
}

function claimText(el: HTMLElement): string {
  return (el.textContent ?? '').trim()
}

describe('APPR-10-05 AC-1: the notify panel states that no message goes out', () => {
  it('T5-4: the claim is a visible grey text node in the inspector, rendered once, carrying no control', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)
    selectNotify()

    const panel = inspectorPanel()
    // The notify arm really mounted, or every assertion below is about the wrong card.
    expect(control(within(panel).getByLabelText('Channel')).tagName, 'the inspector is not showing the notify step').toBe('SELECT')

    const claim = claimIn(panel, NOTIFY_CLAIM_ID)
    expect(claim, `the notify panel renders no [data-testid="${NOTIFY_CLAIM_ID}"] — nothing on screen says a message is never sent`).toBeTruthy()
    expect(within(panel).getByTestId('step-inspector-body').contains(claim!), 'the claim sits outside the panel body it belongs to').toBe(true)

    const text = claimText(claim!)
    expect(text, 'the claim node is empty, so nothing on screen states the gap').not.toBe('')
    // `getByText` never matches a title or an aria attribute, and RTL matches a node's DIRECT text
    // children — resolving the same string this way IS the visible-node proof.
    const found = within(panel).getByText(text)
    expect(found === claim || claim!.contains(found), 'the claim reaches the accessibility tree but not the screen').toBe(true)
    expect(within(panel).getAllByText(text), 'the claim renders more than once in the panel').toHaveLength(1)
    expect(hintColor(claim!), 'a read-only hint renders grey, not amber').toBe('var(--fg-3)')
    // G8/R20 — a control here reds the panel enumeration below and competes for the tab order.
    expect(claim!.querySelector('input, select, textarea, button'), 'the claim carries a control').toBeNull()
    expect(['DIV', 'P'], `the claim is a <${claim!.tagName.toLowerCase()}>`).toContain(claim!.tagName)
  })

  it('T5-4b: the claim denies DELIVERY and affirms the value is kept — never the reverse (R18)', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)
    selectNotify()

    const claim = claimIn(inspectorPanel(), NOTIFY_CLAIM_ID)
    expect(claim, `the notify panel renders no [data-testid="${NOTIFY_CLAIM_ID}"]`).toBeTruthy()
    const text = claimText(claim!)

    expect(DELIVERY_ACT.test(text), `the claim never names the act it denies: ${text}`).toBe(true)
    expect(DENIAL.test(text), `the claim denies nothing: ${text}`).toBe(true)
    expect(STORAGE_AFFIRMED.test(text), `the claim never says the target and channel are kept: ${text}`).toBe(true)
    expect(STORAGE_DENIED.test(text), `the claim says the value is not kept, which is false — it is persisted, sealed and materialised: ${text}`).toBe(false)
  })
})

describe('APPR-10-05 AC-2: the simulator claims it only where a notify step ran', () => {
  it('T5-5: the claim renders for a path holding a notify step, and not for one without', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)

    const present = claimIn(simulatorPanel(), SIM_NOTIFY_CLAIM_ID)
    expect(present, `the simulated path holds a notify step and the panel renders no [data-testid="${SIM_NOTIFY_CLAIM_ID}"]`).toBeTruthy()
    expect(claimText(present!), 'the simulator claim node is empty').not.toBe('')

    cleanup()

    // `policyWith` holds ONE approval and no notify (:20-30). D-F: a claim here would be a new
    // false statement about a step this path never took.
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    expect(within(simulatorPanel()).getByLabelText(SCENARIO_AMOUNT), 'the simulator never mounted, so the absence below is vacuous').toBeTruthy()
    expect(claimIn(simulatorPanel(), SIM_NOTIFY_CLAIM_ID), 'a policy with no notify step carries a claim about one').toBeNull()
  })

  it('T5-5b: the claim is not a control, and does not read as a fourth verdict line', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)

    const panel = simulatorPanel()
    const claim = claimIn(panel, SIM_NOTIFY_CLAIM_ID)
    expect(claim, `the simulator renders no [data-testid="${SIM_NOTIFY_CLAIM_ID}"], so the enumeration below is not about it`).toBeTruthy()

    // R20 — :1906-1913 pins this set on a policy with NO notify, so only this one can see a claim
    // that shipped as a <button>.
    const controls = Array.from(panel.querySelectorAll('input, select, textarea, button'))
    expect(controls.length, 'the panel query matched nothing, so the list below proves nothing').toBeGreaterThan(0)
    expect(controls.map((c) => c.getAttribute('aria-label')), 'the claim rendered as a control').toEqual([SCENARIO_AMOUNT])

    // R21 — the hint register, never a fourth 10px uppercase mono line under the verdict.
    expect(claim!.className.includes('mono'), 'the claim reads as part of the verdict line').toBe(false)
    expect(hintColor(claim!), 'the claim does not carry the read-only hint colour').toBe('var(--fg-3)')
  })

  it('T5-8: the claim sits beside the notify row’s own IN-APP sub-line, never in place of it', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)

    const panel = simulatorPanel()
    // `simSub`'s notify arm, upper-cased (WorkflowParts.tsx:105). Pinned pure at
    // WorkflowParts.test.ts:159-164 — this is the RENDERED half no pure-function test can reach.
    expect(within(panel).getAllByText('IN-APP'), 'the notify row lost its own channel sub-line').toHaveLength(1)
    expect(within(panel).getAllByText(NOTIFY_CARD), 'the notify row lost its own title').toHaveLength(1)

    const claim = claimIn(panel, SIM_NOTIFY_CLAIM_ID)
    expect(claim, 'the claim does not exist, so it cannot be shown to sit beside the row').toBeTruthy()
    const sub = within(panel).getByText('IN-APP')
    expect(claim!.contains(sub), 'the claim swallowed the row’s sub-line').toBe(false)
    expect(sub.contains(claim!), 'the row’s sub-line swallowed the claim').toBe(false)
  })
})

describe('APPR-10-05 AC-1: the fourth inspector sentence stays worded apart', () => {
  // R19: the delegation pair renders only under an APPROVAL node and this one only under a NOTIFY
  // node, so the pins in the DELEG-01-05 block can never see it. Its real collision is the simulator claim, which DOES co-render (G2). The two
  // neighbours are read LIVE off the approval panel here - a re-declared literal that had drifted
  // neighbours are read LIVE off the approval panel here — a re-declared literal that had drifted
  // would make every comparison below vacuous.
  it('T5-9: the notify claim is distinct — identity and containment — from the simulator claim and the two delegation registers', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)

    fireEvent.click(within(canvasPanel()).getByText('Engagement Manager must approve'))
    switchDelegationOn()
    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    const NEIGHBOURS: [string, string][] = [
      ['the sentinel option label', Array.from(picker.options).find((o) => o.value === '')?.textContent ?? ''],
      ['the eligibility note', delegateNote().textContent ?? ''],
    ]
    for (const [what, s] of NEIGHBOURS) expect(s, `${what} read empty`).not.toBe('')

    selectNotify()
    const inspector = claimIn(inspectorPanel(), NOTIFY_CLAIM_ID)
    const simulator = claimIn(simulatorPanel(), SIM_NOTIFY_CLAIM_ID)
    expect(inspector, `the inspector renders no [data-testid="${NOTIFY_CLAIM_ID}"]`).toBeTruthy()
    expect(simulator, `the simulator renders no [data-testid="${SIM_NOTIFY_CLAIM_ID}"]`).toBeTruthy()

    const a = claimText(inspector!)
    const b = claimText(simulator!)
    expect(a, 'the inspector claim is empty').not.toBe('')
    expect(b, 'the simulator claim is empty').not.toBe('')
    // They are on screen together and address different readers — this control vs this simulated path.
    expect(a, 'the two claims collapsed into one sentence').not.toBe(b)
    expect(a.includes(b), 'the inspector claim swallowed the simulator claim verbatim').toBe(false)
    expect(b.includes(a), 'the simulator claim swallowed the inspector claim verbatim').toBe(false)

    for (const [what, s] of NEIGHBOURS) {
      for (const [whose, claim] of [['the inspector claim', a], ['the simulator claim', b]] as [string, string][]) {
        expect(claim, `${whose} collapsed into ${what}`).not.toBe(s)
        expect(claim.includes(s), `${whose} swallowed ${what} verbatim`).toBe(false)
        expect(s.includes(claim), `${what} swallowed ${whose} verbatim`).toBe(false)
      }
    }
  })
})

describe('APPR-10-05: the notify panel’s control set', () => {
  // The only inspector arm with no enumeration — the condition panel has one (:1944-1955) and the
  // approval panel has one (:901-910). It is what makes G8 falsifiable here: a claim rendered as a
  // <button> reds this rather than shipping.
  it('renders the Remove button, the Notify select and the Channel select — and nothing else', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)
    selectNotify()

    const controls = Array.from(inspectorPanel().querySelectorAll('input, select, textarea, button'))
    expect(controls.length, 'the panel query matched nothing, so the list below proves nothing').toBeGreaterThan(0)
    expect(controls.map((c) => [c.tagName, controlName(c)]), 'the notify panel gained or lost a control').toEqual([
      ['BUTTON', 'Remove'],
      ['SELECT', 'Notify'],
      ['SELECT', 'Channel'],
    ])
  })
})

// ----------------------------------------------------------------------------
// APPR-10-05 QA (task-517) — four specs, each written against a mutation that
// survived the shipped set.
// ----------------------------------------------------------------------------

/** A notify inside ONE lane of a condition. `> ₦1bn` against SIM_DEFAULT's ₦750m takes `else`. */
function lanedNotifyPolicy(lane: 'then' | 'else'): Policy {
  const notify: NotifyNode = { id: 'n2', type: 'notify', target: 'Tax Team', channel: 'In-app' }
  return {
    ...policyWith('fin_mgr'),
    nodes: [
      {
        id: 'c1',
        type: 'condition',
        field: 'amount',
        op: '>',
        value: 1_000_000_000,
        then: lane === 'then' ? [notify] : [],
        else: lane === 'else' ? [notify] : [],
      },
    ],
  }
}

/** One policy carrying all four node types, so the inspector's four arms are reachable in one render. */
function everyKindPolicy(): Policy {
  return {
    ...policyWith('fin_mgr'),
    nodes: [
      { id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false },
      { id: 'c1', type: 'condition', field: 'amount', op: '>', value: 250_000_000, then: [], else: [] },
      { id: 'a1', type: 'autoapprove' },
      { id: 'n2', type: 'notify', target: 'Tax Team', channel: 'In-app' },
    ],
  }
}

describe('APPR-10-05 QA AC-2: the gate reads the path that ran, not the policy that holds it', () => {
  // The shipped T5-5 pairs a ROOT-level notify against a policy with no notify at all, so a gate
  // written over `policy.nodes` rather than `result.steps` passes it. That gate would claim a
  // message was reached on a lane the scenario never took — the exact false statement D-F forbids.
  it('T5-10: a notify in the UNTAKEN lane carries no claim, and the same notify in the TAKEN lane does', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={lanedNotifyPolicy('then')} />)

    // The notify is really in the policy and really on the canvas — otherwise the absence below
    // is just T5-5's no-notify case again, and proves nothing new.
    expect(within(canvasPanel()).getByText(NOTIFY_CARD), 'the untaken lane never rendered its notify card').toBeTruthy()
    expect(within(simulatorPanel()).getByLabelText(SCENARIO_AMOUNT), 'the simulator never mounted').toBeTruthy()
    expect(within(simulatorPanel()).queryByText(NOTIFY_CARD), 'the untaken lane put a step in the simulated path').toBeNull()
    expect(claimIn(simulatorPanel(), SIM_NOTIFY_CLAIM_ID), 'a lane the scenario never took carries a claim about its notify step').toBeNull()

    cleanup()

    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={lanedNotifyPolicy('else')} />)
    expect(within(simulatorPanel()).getByText(NOTIFY_CARD), 'the taken lane dropped its notify step').toBeTruthy()
    expect(claimIn(simulatorPanel(), SIM_NOTIFY_CLAIM_ID), 'the taken lane reached a notify step and carries no claim about it').toBeTruthy()
  })
})

describe('APPR-10-05 QA AC-1: the inspector claim belongs to the notify arm alone', () => {
  // R19 rests the whole four-way argument on "a node has exactly one type", but nothing pinned the
  // claim to the notify arm. Rendered outside it, the sentence lands on the approval, condition and
  // autoapprove panels — a delivery denial on a step that never notifies anyone.
  it('T5-11: selecting an approval, a condition or an autoapprove step shows no delivery claim', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={everyKindPolicy()} />)

    const OTHER_ARMS: [string, string, string][] = [
      ['approval', 'Engagement Manager must approve', 'Who must approve'],
      ['condition', 'Amount greater than ₦250,000,000', 'If this field'],
      ['autoapprove', 'Auto-approve', ''],
    ]
    for (const [kind, card, label] of OTHER_ARMS) {
      fireEvent.click(within(canvasPanel()).getByText(card))
      const panel = inspectorPanel()
      // The arm really mounted — without this the absence below would also pass on an empty panel.
      if (label) expect(control(within(panel).getByLabelText(label)).tagName, `the ${kind} arm is not on screen`).toBe('SELECT')
      else expect(within(panel).getByText(/clears with no manual sign-off/i), 'the autoapprove arm is not on screen').toBeTruthy()
      expect(claimIn(panel, NOTIFY_CLAIM_ID), `the ${kind} panel carries a claim about notify delivery`).toBeNull()
    }

    // The positive control for all three absences above: the same query on the notify arm resolves.
    fireEvent.click(within(canvasPanel()).getByText(NOTIFY_CARD))
    expect(claimIn(inspectorPanel(), NOTIFY_CLAIM_ID), 'the notify panel lost its claim, so the three absences prove nothing').toBeTruthy()
  })
})

describe('APPR-10-05 QA AC-2: the simulator claim reads before the rows it explains', () => {
  it('T5-12: the claim precedes the notify row in document order', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)

    const panel = simulatorPanel()
    const claim = claimIn(panel, SIM_NOTIFY_CLAIM_ID)
    expect(claim, 'the claim does not exist, so its position proves nothing').toBeTruthy()
    const row = within(panel).getByText(NOTIFY_CARD)

    // DOCUMENT_POSITION_FOLLOWING: `row` comes after `claim`. Moving the div below the step list
    // leaves every containment assertion in T5-8 intact and only flips this bit.
    expect(claim!.compareDocumentPosition(row) & Node.DOCUMENT_POSITION_FOLLOWING, 'the claim reads after the rows it explains').toBeGreaterThan(0)
  })
})

describe('APPR-10-05 QA AC-2: the simulator claim is held to the inspector claim’s register', () => {
  // T5-4b applies R18 to the inspector sentence only. The simulator sentence is the one the
  // scenario reader sees, and nothing stopped it denying storage or borrowing DELEGATE_NOTE's clause.
  it('T5-13: it denies DELIVERY, affirms the value is kept, and borrows no neighbour’s clause', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)

    const claim = claimIn(simulatorPanel(), SIM_NOTIFY_CLAIM_ID)
    expect(claim, `the simulator renders no [data-testid="${SIM_NOTIFY_CLAIM_ID}"]`).toBeTruthy()
    const text = claimText(claim!)
    expect(text, 'the simulator claim node is empty').not.toBe('')

    expect(DELIVERY_ACT.test(text), `the claim never names the act it denies: ${text}`).toBe(true)
    expect(DENIAL.test(text), `the claim denies nothing: ${text}`).toBe(true)
    expect(STORAGE_AFFIRMED.test(text), `the claim never says the target and channel are kept: ${text}`).toBe(true)
    expect(STORAGE_DENIED.test(text), `the claim says the value is not kept, which is false — it is persisted, sealed and materialised: ${text}`).toBe(false)
  })
})

describe('RESKIN2-05-03 WB: the shared workflow atoms', () => {
  it('WB-01 the builder status pill is a 4px chip', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const pills = screen.getAllByText('DRAFT')
    expect(pills, 'exactly one DRAFT pill must render').toHaveLength(1)
    const pill = pills[0]
    expect(pill.className, 'the pill is not a mono span').toContain('mono')
    expect(pill.style.borderRadius, 'the pill corner is not the 4px token').toBe('var(--radius-sm)')
    expect(pill.style.padding, 'the builder passes its own padding').toBe('3px 9px')
    expect(pill.children, 'the pill still nests a dot or an inner label').toHaveLength(0)
  })

  it('WB-02 the knob is a circle without a shadow; the track stays a pill', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const switches = screen.getAllByRole('switch')
    expect(switches.length, 'selecting an approval step rendered no switch').toBeGreaterThan(0)
    for (const tog of switches as HTMLElement[]) {
      const knob = tog.querySelector('.pf-knob') as HTMLElement
      expect(knob, 'the switch has no knob').toBeTruthy()
      expect(tog.style.borderRadius, 'the track is no longer a pill').toBe('99px')
      expect(knob.style.borderRadius, 'the knob is not a circle').toBe('50%')
      expect(knob.style.boxShadow, 'the knob still carries a shadow').toBe('')
    }
  })
})

/** The row both the name input and Publish sit in: the nearest ancestor of the input that holds Publish. */
function headerRow(): HTMLElement {
  let el: HTMLElement | null = nameInput().parentElement
  while (el && !el.contains(publishButton())) el = el.parentElement
  expect(el, 'no ancestor of the name input holds Publish').toBeTruthy()
  return el as HTMLElement
}

function builderCanvas(): HTMLElement {
  return canvasPanel().firstElementChild as HTMLElement
}

/** A root approval, a condition, and an approval in the condition's `then` lane. */
function cardsPolicy(): Policy {
  return {
    ...conditionPolicy(),
    nodes: [
      { id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false },
      { id: 'c1', type: 'condition', field: 'amount', op: '>', value: 250_000_000, then: [{ id: 'm1', type: 'approval', role: 'cfo', sla: '24', delegate: false }], else: [] },
    ],
  }
}

const SIMPLE_TITLE = 'Engagement Manager must approve'
const MINI_TITLE = 'Engagement Partner must approve'
const RULE_TITLE = 'Amount greater than ₦250,000,000'

const simpleCard = () => within(canvasPanel()).getByText(SIMPLE_TITLE).closest('[draggable]') as HTMLElement
const miniCard = () => within(canvasPanel()).getByText(MINI_TITLE).closest('[draggable]') as HTMLElement
const conditionHead = () => within(canvasPanel()).getByText(RULE_TITLE).closest('[draggable]') as HTMLElement
const conditionBlock = () => conditionHead().parentElement as HTMLElement

describe('RESKIN2-05-04 WB: the builder header, palette and canvas', () => {
  it('WB-03 the back link sits above the header row', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const back = screen.getByRole('button', { name: /All policies/ })
    const row = headerRow()
    expect(row.contains(back), 'the All policies button is still inside the header row').toBe(false)
    expect(row.style.alignItems, 'the header row does not align to the top').toBe('flex-start')
    expect(row.style.flexWrap, 'the header row lost its wrap').toBe('wrap')
  })

  it('WB-04 the columns and the name row follow the prototype', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const input = nameInput()
    const nameRow = input.parentElement as HTMLElement
    const left = nameRow.parentElement as HTMLElement
    const right = publishButton().parentElement!.parentElement as HTMLElement
    expect(left.style.flex, 'the left column has no 320px basis').toBe('1 1 320px')
    expect(left.style.minWidth, 'the left column cannot shrink').toMatch(/^0(px)?$/)
    expect(nameRow.style.flexWrap, 'a long name cannot wrap the status pill').toBe('wrap')
    expect(nameRow.style.rowGap).toBe('8px')
    expect(nameRow.style.gap, 'the name-row gap stays 10').toBe('10px')
    expect(right.style.maxWidth, 'the right column is not 420px wide').toBe('420px')
    expect(input.style.fontWeight, 'the name input is not bold').toBe('700')
  })

  it('WB-05 the enabled Publish labels in primary-foreground', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const publish = publishButton()
    expect(publish.disabled, 'Publish is already blocked, so its enabled paint is not under test').toBe(false)
    expect(publish.style.background).toBe('var(--action)')
    expect(publish.style.color, 'the enabled Publish label is not primary-foreground').toBe('var(--primary-foreground)')
    expect([publish.style.opacity, publish.style.cursor, publish.style.filter], 'an enabled Publish is dimmed').toEqual(['', '', ''])

    fireEvent.change(nameInput(), { target: { value: 'Renamed' } })

    expect(publishButton().disabled, 'an edit did not block Publish').toBe(true)
    expect(publishButton().style.background).toBe('var(--action)')
    expect(publishButton().style.color).toBe('var(--primary-foreground)')
    expect(publishButton().style.opacity).toBe('0.45')
    expect(publishButton().style.cursor).toBe('not-allowed')
    expect(publishButton().style.filter).toBe('none')
  })

  it('WB-06 the consequence reads --fg-2', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const consequence = screen.getByTestId('publish-consequence')
    expect(consequence.textContent, 'the consequence is not the no-policy-in-force line').toBe(NO_POLICY_IN_FORCE)
    expect(consequence.style.color, 'the consequence is not --fg-2').toBe('var(--fg-2)')

    fireEvent.change(nameInput(), { target: { value: 'Renamed' } })

    expect(screen.getByTestId('publish-blocked-reason').style.color, 'the blocked reason left --fg-3').toBe('var(--fg-3)')
  })

  it('WB-07 the palette wrapper and tile icons are 6px; tiles defer to pf-upcard', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const wrapper = screen.getByText('Building blocks').parentElement!.parentElement as HTMLElement
    expect(wrapper.style.borderRadius, 'the palette wrapper is not 6px').toBe('var(--radius-md)')

    const tiles = Array.from(document.querySelectorAll('button.pf-upcard')) as HTMLElement[]
    expect(tiles, 'the palette renders four tiles').toHaveLength(4)
    for (const tile of tiles) {
      expect(tile.style.borderRadius, 'a tile carries an inline radius; pf-upcard owns it').toBe('')
      const icon = tile.firstElementChild as HTMLElement
      expect(icon.style.borderRadius, 'a tile icon is not 6px').toBe('var(--radius-md)')
    }
  })

  it('WB-08 the canvas, trigger and terminal are flat 6px panels', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const canvas = builderCanvas()
    expect(canvas.style.backgroundColor, 'the canvas lost its fill').toBe('var(--bg-0)')
    expect(canvas.style.borderRadius, 'the canvas is not 6px').toBe('var(--radius-md)')
    expect(canvas.style.backgroundImage, 'the canvas still draws the dot texture').toBe('')

    const trigger = screen.getByText('Invoice submitted').parentElement!.parentElement as HTMLElement
    const terminal = screen.getByText('Transmit to NRS / MBS').parentElement!.parentElement as HTMLElement
    for (const [name, panel] of [['trigger', trigger], ['terminal', terminal]] as const) {
      expect(panel.style.borderRadius, `the ${name} is not 6px`).toBe('var(--radius-md)')
      expect(panel.style.boxShadow, `the ${name} still carries a resting shadow`).toBe('')
      expect((panel.firstElementChild as HTMLElement).style.borderRadius, `the ${name} icon tile is not 6px`).toBe('var(--radius-md)')
    }
  })

  it('WB-09 nodes are 6px, bordered, shadowless', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={cardsPolicy()} />)

    const cards = [['simple', simpleCard()], ['condition', conditionBlock()], ['mini', miniCard()]] as const
    for (const [name, card] of cards) {
      expect(card.style.borderRadius, `the ${name} card is not 6px`).toBe('var(--radius-md)')
      expect(card.style.border, `the ${name} card border is not the rest border`).toBe('1.5px solid var(--line-2)')
      expect(card.style.boxShadow, `the ${name} card still carries a shadow`).toBe('')
    }
    expect(conditionHead().style.background, 'the condition head does not rest on --bg-1').toBe('var(--bg-1)')

    const badges = Array.from(canvasPanel().querySelectorAll('span.mono')) as HTMLElement[]
    expect(badges.length, 'no badge rendered on the simple card').toBeGreaterThan(0)
    for (const badge of badges) expect(badge.style.borderRadius, 'a badge is not 4px').toBe('var(--radius-sm)')

    const icons = [simpleCard(), miniCard(), conditionHead()].map((c) => c.firstElementChild as HTMLElement)
    for (const icon of icons) expect(icon.style.borderRadius, 'a node icon tile is not 6px').toBe('var(--radius-md)')
  })

  it('WB-10 selection and arming light the border on every card kind', () => {
    const action = '1.5px solid var(--action)'
    const rest = '1.5px solid var(--line-2)'
    const borders = () => [simpleCard().style.border, miniCard().style.border, conditionBlock().style.border]
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={cardsPolicy()} />)

    fireEvent.click(within(canvasPanel()).getByText(SIMPLE_TITLE))
    expect(borders(), 'selecting the simple card lit another card or none').toEqual([action, rest, rest])

    fireEvent.click(within(canvasPanel()).getByText(MINI_TITLE))
    expect(borders(), 'selecting the mini card lit another card or none').toEqual([rest, action, rest])

    fireEvent.click(within(canvasPanel()).getByText(RULE_TITLE))
    expect(conditionBlock().style.border, 'a selected condition block has no action border').toBe(action)
    expect(conditionHead().style.background, 'a selected condition head is not tinted').toBe('var(--action-tint)')

    cleanup()
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={cardsPolicy()} />)
    const armCases: [string, () => HTMLElement][] = [
      [`Move ${SIMPLE_TITLE}`, simpleCard],
      [`Move ${MINI_TITLE}`, miniCard],
      [`Move ${RULE_TITLE}`, conditionBlock],
    ]
    for (const [name, card] of armCases) {
      const move = screen.getByRole('button', { name })
      fireEvent.click(move)
      expect(screen.getByRole('button', { name }).getAttribute('aria-pressed'), `${name} did not arm`).toBe('true')
      expect(card().style.border, `${name}: an armed card has no action border`).toBe(action)
    }

    cleanup()
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={cardsPolicy()} />)
    fireEvent.click(within(canvasPanel()).getByText(SIMPLE_TITLE))
    fireEvent.click(screen.getByRole('button', { name: `Move ${MINI_TITLE}` }))
    expect(borders(), 'a selected card and an armed card in another lane are both lit').toEqual([action, action, rest])

    fireEvent.click(screen.getByRole('button', { name: `Move ${MINI_TITLE}` }))
    expect(borders(), 'disarming dims the armed card and leaves the selected one lit').toEqual([action, rest, rest])

    fireEvent.click(screen.getByRole('button', { name: `Move ${SIMPLE_TITLE}` }))
    fireEvent.click(screen.getByRole('button', { name: `Move ${SIMPLE_TITLE}` }))
    expect(borders(), 'a card both selected and armed stays lit once disarmed').toEqual([action, rest, rest])
  })

  it('WB-11 hints read --fg-3 in 6px boxes (boundary)', () => {
    const empty = { ...policyWith('fin_mgr'), nodes: [] }
    const { unmount } = render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={empty} />)
    const hint = screen.getByText(/^No approval steps/)
    expect(hint.style.color, 'the empty-canvas hint is not --fg-3').toBe('var(--fg-3)')
    expect(hint.style.borderRadius, 'the empty-canvas hint is not 6px').toBe('var(--radius-md)')

    fireEvent.dragStart(paletteTile())
    const drop = screen.getByText('Drop step here')
    expect(drop.style.borderRadius, 'the root drop box is not 6px').toBe('var(--radius-md)')
    expect(drop.style.color, 'the idle drop label is not --fg-3').toBe('var(--fg-3)')
    unmount()

    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={conditionPolicy()} />)
    const lanes = [screen.getByText('Drop steps here'), screen.getByText('Continue (no extra step)')]
    for (const lane of lanes) {
      expect(lane.style.color, 'a lane hint is not --fg-3').toBe('var(--fg-3)')
      expect(lane.style.borderRadius, 'a lane hint is not 6px').toBe('var(--radius-md)')
    }

    fireEvent.dragStart(paletteTile())
    const branchSlots = Array.from(document.querySelectorAll('[data-wf-slot]')).filter((s) => !(s.getAttribute('data-wf-slot') ?? '').startsWith('root#'))
    expect(branchSlots, 'arming an approval opened no branch drop box').toHaveLength(2)
    for (const slot of branchSlots) {
      const box = slot.firstElementChild as HTMLElement
      expect(box.style.borderRadius, 'a branch drop box is not 6px').toBe('var(--radius-md)')
    }
  })

  it('WB-16 a refused save reads 12px in its 6px box (error)', async () => {
    const refusal = 'only an admin can change approval policies'
    const savePolicy = vi.fn(() => {
      const p = Promise.reject(new ApiError('http', refusal, 403))
      p.catch(() => {})
      return p
    })
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES, savePolicy })} policy={policyWith('fin_mgr')} />)

    fireEvent.click(saveButton())

    const err = await screen.findByTestId('policy-save-error')
    expect(err.textContent, 'the refusal is not the server sentence').toBe(refusal)
    expect(err.style.fontSize, 'a refused save is not 12px').toBe('12px')
    expect(err.style.borderRadius).toBe('var(--radius-md)')
  })

  it('WB-17 only the inspector selects are on --bg-1', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText(SIMPLE_TITLE))

    for (const name of ['Who must approve', 'Deadline']) {
      const select = control(screen.getByLabelText(name))
      expect(select.tagName, `${name} is not a select`).toBe('SELECT')
      expect(select.style.backgroundColor, `${name} left --bg-1`).toBe('var(--bg-1)')
    }
  })

  it('WB-17b a disabled WfSelect keeps --bg-3 whatever background it is given', () => {
    render(<WfSelect label="Probe" value="a" options={[{ value: 'a', label: 'A' }]} onChange={() => {}} background="var(--bg-2)" disabled />)

    const select = control(screen.getByLabelText('Probe'))
    expect(select.style.backgroundColor, 'the disabled paint did not outrank the background prop').toBe('var(--bg-3)')
  })

  it('WB-18 the Move glyph rests on --fg-4', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const move = screen.getByRole('button', { name: /^Move / })
    const del = screen.getByRole('button', { name: /^Delete / })
    expect(move.style.color, 'the resting Move glyph is not --fg-4').toBe('var(--fg-4)')
    expect(del.style.color, 'the Delete glyph left --fg-4').toBe('var(--fg-4)')

    fireEvent.click(move)

    expect(screen.getByRole('button', { name: /^Move / }).style.color, 'the armed Move glyph is not --action').toBe('var(--action)')
  })
})

/** The inspector card of a nothing-selected builder, scoped off its own header. */
const emptyInspector = () => screen.getByText('Step details').parentElement as HTMLElement

function thresholdPolicy(value: number): Policy {
  return { ...conditionPolicy(), nodes: [{ id: 'c1', type: 'condition', field: 'amount', op: '>', value, then: [], else: [] }] }
}

/** Selects the one condition from the canvas by its rule title. */
function selectThreshold(title: string): void {
  fireEvent.click(within(canvasPanel()).getByText(title))
}

const preset = (label: string) => within(inspectorPanel()).getByRole('button', { name: label })

const PRESET_REST = { background: 'var(--bg-1)', color: 'var(--fg-2)', border: '1px solid var(--line-2)' }
const PRESET_LIT = { background: 'var(--action-tint)', color: 'var(--action)', border: '1px solid var(--action)' }

function presetPaint(label: string) {
  const s = preset(label).style
  return { background: s.background, color: s.color, border: s.border }
}

describe('RESKIN2-05-05 WB: the builder inspector and simulator', () => {
  it('WB-12 the inspector is a 6px panel with 15px titles', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)

    const empty = emptyInspector()
    expect(empty.style.borderRadius, 'the empty inspector is not 6px').toBe('var(--radius-md)')
    const heading = within(empty).getByText('Step details')
    expect(heading.style.fontSize, 'the Step details title is not 15px').toBe('15px')
    expect(heading.style.fontWeight, 'the Step details title is not bold').toBe('700')

    fireEvent.click(within(canvasPanel()).getByText(SIMPLE_TITLE))

    const panel = inspectorPanel()
    expect(panel.style.borderRadius, 'the selected inspector is not 6px').toBe('var(--radius-md)')
    const title = within(panel).getByText('Approval step')
    expect(title.style.fontSize, 'the selected title is not 15px').toBe('15px')
    expect(title.style.fontWeight, 'the selected title is not bold').toBe('700')
    const remove = within(panel).getByRole('button', { name: 'Remove' })
    expect(remove.style.fontSize, 'Remove is not 12.5px').toBe('12.5px')
    expect(remove.style.fontWeight, 'Remove is not 600').toBe('600')
    const roles = within(panel).getByRole('button', { name: 'Manage roles' })
    expect(roles.style.fontWeight, 'Manage roles is not 500').toBe('500')
  })

  it('WB-13 the RULE box is 6px', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={conditionPolicy()} />)
    selectThreshold(RULE_TITLE)

    const label = within(inspectorPanel()).getByText('RULE')
    const box = label.parentElement as HTMLElement
    expect(box.contains(within(inspectorPanel()).getAllByText(RULE_TITLE)[0]), 'the box holding RULE does not hold the rule sentence').toBe(true)
    expect(box.style.borderRadius, 'the RULE box is not 6px').toBe('var(--radius-md)')
  })

  it('WB-14 the builder carries no v1 vocabulary (boundary)', () => {
    const policy: Policy = {
      ...conditionPolicy(),
      nodes: [
        { id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false },
        { id: 'c1', type: 'condition', field: 'amount', op: '>', value: 250_000_000, then: [{ id: 'm1', type: 'approval', role: 'cfo', sla: '24', delegate: false }], else: [] },
        { id: 'n2', type: 'notify', target: 'Tax Team', channel: 'In-app' },
        { id: 'a1', type: 'autoapprove' },
      ],
    }
    const { container } = render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policy} />)
    fireEvent.click(within(canvasPanel()).getByText(SIMPLE_TITLE))

    const switches = container.querySelectorAll('button[role="switch"]')
    expect(switches.length, 'no switch rendered, so the 99px exemption is unobserved').toBeGreaterThan(0)

    const styled = Array.from(container.querySelectorAll('[style]')) as HTMLElement[]
    expect(styled.length, 'the builder rendered no inline-styled element').toBeGreaterThan(0)

    const allowed = new Set(['var(--radius-sm)', 'var(--radius-md)', '50%', '99px'])
    const seen = new Set<string>()
    for (const el of styled) {
      const css = el.getAttribute('style') ?? ''
      for (const m of css.matchAll(/border[a-z-]*radius:\s*([^;]+)/g)) {
        const v = m[1].trim()
        seen.add(v)
        expect(allowed.has(v), `an off-vocabulary corner: ${v} on <${el.tagName.toLowerCase()}> ${(el.textContent ?? '').slice(0, 30)}`).toBe(true)
        if (v === '99px') {
          expect(el.matches('button[role="switch"]'), `99px outside the toggle track: <${el.tagName.toLowerCase()}> ${(el.textContent ?? '').slice(0, 30)}`).toBe(true)
        }
      }
      expect(css, 'a box-shadow is still painted').not.toMatch(/box-shadow/i)
      expect(css, 'an oklch colour is still painted').not.toMatch(/oklch/i)
      expect(css, 'a gradient is still painted').not.toMatch(/gradient/i)
    }
    expect(seen.has('var(--radius-md)'), 'no 6px corner observed, so the scan is not reading corners').toBe(true)
    expect(seen.has('99px'), 'the toggle track pill was not observed').toBe(true)
  })

  it('WB-15 the simulator\'s dots are circles and its connector a plain line', () => {
    const policy: Policy = {
      ...policyWith('fin_mgr'),
      nodes: [
        { id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false },
        { id: 'n2', type: 'approval', role: 'cfo', sla: '24', delegate: false },
      ],
    }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policy} />)

    const panel = simulatorPanel()
    expect(panel.style.borderRadius, 'the simulator panel is not 6px').toBe('var(--radius-md)')
    const title = within(panel).getByText('Test a scenario')
    expect(title.style.fontSize, 'the simulator title is not 15px').toBe('15px')
    expect(title.style.fontWeight, 'the simulator title is not bold').toBe('700')

    const all = Array.from(panel.querySelectorAll('*')) as HTMLElement[]
    const dots = all.filter((el) => el.tagName === 'SPAN' && el.style.width === '24px' && el.style.height === '24px')
    expect(dots, 'two approvals render two step dots').toHaveLength(2)
    for (const dot of dots) expect(dot.style.borderRadius, 'a step dot is not a circle').toBe('50%')

    // The amount input's ₦ prefix is also an absolute span; it is aria-hidden, the connector parts are not.
    const lines = all.filter((el) => el.tagName === 'SPAN' && el.style.position === 'absolute' && !el.hasAttribute('aria-hidden'))
    expect(lines, 'two steps leave one connector line and no arrowhead').toHaveLength(1)
    expect(lines[0].style.borderRadius, 'the connector still carries a corner').toBe('')

    const arrowheads = all.filter((el) => /border-top:\s*5px solid/.test(el.getAttribute('style') ?? ''))
    expect(arrowheads, 'the connector arrowhead remains').toHaveLength(0)
  })

  it('WB-19 the preset matching the threshold is highlighted', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={thresholdPolicy(500_000_000)} />)
    selectThreshold('Amount greater than ₦500,000,000')

    expect(presetPaint('₦500M'), 'the preset equal to the threshold is not lit').toEqual(PRESET_LIT)
    expect(presetPaint('₦100M'), '₦100M is not at rest').toEqual(PRESET_REST)
    expect(presetPaint('₦1B'), '₦1B is not at rest').toEqual(PRESET_REST)

    fireEvent.click(preset('₦1B'))

    expect(presetPaint('₦1B'), 'the clicked preset is not lit').toEqual(PRESET_LIT)
    expect(presetPaint('₦500M'), 'the previous preset did not return to rest').toEqual(PRESET_REST)
    expect(presetPaint('₦100M')).toEqual(PRESET_REST)
  })

  it('WB-20 a threshold matching no preset highlights none (boundary)', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={thresholdPolicy(250_000_000)} />)
    selectThreshold(RULE_TITLE)

    const labels = ['₦100M', '₦500M', '₦1B']
    for (const label of labels) expect(presetPaint(label), `${label} is lit though no preset equals the threshold`).toEqual(PRESET_REST)

    const input = within(inspectorPanel()).getByLabelText('Threshold amount in naira') as HTMLInputElement
    expect(input.value.replace(/\D/g, ''), 'the threshold input no longer reads 250000000').toBe('250000000')
  })

  it('WB-21 the delegation row is enabled and carries the siblings\' opacity', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(within(canvasPanel()).getByText(SIMPLE_TITLE))

    const tog = within(inspectorPanel()).getByRole('switch', { name: 'Allow delegation' })
    expect((tog as HTMLButtonElement).disabled, 'the delegation switch is shut').toBe(false)
    const row = tog.parentElement as HTMLElement
    expect(within(row).getByText('Allow delegation'), 'the switch parent is not the delegation row').toBeTruthy()
    const deadline = within(inspectorPanel()).getByLabelText('Deadline').closest('label') as HTMLElement
    expect(row.style.opacity, 'the delegation row is not painted like the Deadline row').toBe(deadline.style.opacity)
    expect(row.style.cursor, 'the delegation row still shows a not-allowed cursor').toBe('')
  })

  it('WB-19 the lit preset follows the typed threshold, not the last click (boundary)', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={thresholdPolicy(250_000_000)} />)
    selectThreshold(RULE_TITLE)
    const input = within(inspectorPanel()).getByLabelText('Threshold amount in naira') as HTMLInputElement

    fireEvent.change(input, { target: { value: '1000000000' } })
    expect(input.value.replace(/\D/g, ''), 'the typed threshold did not land').toBe('1000000000')
    expect(presetPaint('₦1B'), 'a typed value equal to a preset does not light it').toEqual(PRESET_LIT)
    expect(presetPaint('₦100M')).toEqual(PRESET_REST)
    expect(presetPaint('₦500M')).toEqual(PRESET_REST)

    fireEvent.change(input, { target: { value: '1000000001' } })
    expect(input.value.replace(/\D/g, ''), 'the edited threshold did not land').toBe('1000000001')
    for (const label of ['₦100M', '₦500M', '₦1B']) expect(presetPaint(label), `${label} stays lit one naira off its value`).toEqual(PRESET_REST)

    fireEvent.change(input, { target: { value: '' } })
    for (const label of ['₦100M', '₦500M', '₦1B']) expect(presetPaint(label), `${label} is lit on a cleared threshold`).toEqual(PRESET_REST)
  })

  it('WB-15 one step draws no connector; a three-kind path draws two between its circles (boundary)', () => {
    const connectors = (panel: HTMLElement) =>
      (Array.from(panel.querySelectorAll('*')) as HTMLElement[]).filter((el) => el.tagName === 'SPAN' && el.style.position === 'absolute' && !el.hasAttribute('aria-hidden'))
    const dots = (panel: HTMLElement) =>
      (Array.from(panel.querySelectorAll('*')) as HTMLElement[]).filter((el) => el.tagName === 'SPAN' && el.style.width === '24px' && el.style.height === '24px')

    const one: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false }] }
    const first = render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={one} />)
    expect(dots(simulatorPanel()), 'one step renders one dot').toHaveLength(1)
    expect(connectors(simulatorPanel()), 'the last step draws a connector below it').toHaveLength(0)
    first.unmount()

    const mixed: Policy = {
      ...policyWith('fin_mgr'),
      nodes: [
        { id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false },
        { id: 'n2', type: 'notify', target: 'Tax Team', channel: 'In-app' },
        { id: 'a1', type: 'autoapprove' },
      ],
    }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={mixed} />)
    const panel = simulatorPanel()
    const circles = dots(panel)
    expect(circles, 'approval, notify and auto-approve render three dots').toHaveLength(3)
    for (const dot of circles) expect(dot.style.borderRadius, 'a non-approval step dot is not a circle').toBe('50%')
    const lines = connectors(panel)
    expect(lines, 'three steps draw two connectors').toHaveLength(2)
    for (const line of lines) expect(line.style.borderRadius, 'a connector carries a corner').toBe('')
  })

  it('WB-22 the select hints read 11px / 1.5; the resolved-role line keeps 11.5px / 1.45', () => {
    const { unmount } = render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(within(canvasPanel()).getByText(SIMPLE_TITLE))
    const panel = inspectorPanel()

    fireEvent.click(within(panel).getByRole('switch', { name: 'Allow delegation' }))
    const note = within(panel).getByText('Only Admins and Reviewers can be a delegate.')
    expect(note.textContent, 'the located hint is the note, not the picker').toMatch(/Admins and Reviewers/)
    expect(note.style.fontSize, 'the delegate note is not 11px').toBe('11px')
    expect(note.style.lineHeight, 'the delegate note is not 1.5').toBe('1.5')

    const resolved = within(panel).getByRole('button', { name: 'Manage roles' }).parentElement as HTMLElement
    expect(resolved.firstElementChild?.textContent, 'control: the row leads with the resolved-role sentence').toBeTruthy()
    expect(resolved.style.display, 'control: the resolved line is the flex row holding Manage roles').toBe('flex')
    expect(resolved.style.fontSize, 'the resolved line is not 11.5px').toBe('11.5px')
    expect(resolved.style.lineHeight, 'the resolved line is not 1.45').toBe('1.45')
    unmount()

    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)
    selectNotify()
    const notify = within(inspectorPanel()).getByTestId('notify-not-delivered')
    expect(notify.style.fontSize, 'the notify note is not 11px').toBe('11px')
    expect(notify.style.lineHeight, 'the notify note is not 1.5').toBe('1.5')
  })
})

const CLIENT_A = { id: 'e1', name: 'Zenith Foods' }
const CLIENT_B = { id: 'e2', name: 'Alpha Mills' }
const ENTITIES = [CLIENT_A, CLIENT_B]
const BLOCKED_SAVE_FIRST = 'Save your changes first'

function scoped(id: string, scopes: Partial<Policy['scopes']> = {}): Policy {
  return { ...policyWith('fin_mgr'), id, scopes: { ...CATCH_ALL_SCOPES, ...scopes } }
}

/** `p` above a catch-all, so the card is editable. */
function editableCtx(p: Policy, over: Record<string, unknown> = {}): PlatformCtx {
  return builderCtx({ policies: [p, scoped('last')], roles: FIRM_ROLES, entities: ENTITIES, ...over })
}

function cell(name: 'doc' | 'client' | 'band' | 'role'): HTMLElement {
  return screen.getByTestId(`scope-cell-${name}`)
}

function cellSelect(name: 'doc' | 'client' | 'role'): HTMLSelectElement {
  return cell(name).querySelector('select') as HTMLSelectElement
}

function optionLabels(sel: HTMLSelectElement): string[] {
  return Array.from(sel.options).map((o) => o.textContent ?? '')
}

function chipLabels(el: HTMLElement): string[] {
  return Array.from(el.querySelectorAll('button.pf-chip')).map((b) => (b.querySelector('span')?.textContent ?? '').trim())
}

describe('APSC-04-03: the Applies card', () => {
  it('the Applies card offers the four scopes on a policy above the last', () => {
    const p = scoped('p1')
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)

    const card = screen.getByTestId('scope-card')
    for (const label of ['Document types', 'Clients', 'Amount band', 'Created by a member of']) {
      expect(within(card).getByText(label), `no "${label}" cell`).toBeTruthy()
    }
    const controls = Array.from(card.querySelectorAll('select, input, button')) as (HTMLSelectElement | HTMLInputElement | HTMLButtonElement)[]
    expect(controls).toHaveLength(6)
    expect(controls.filter((c) => c.disabled)).toHaveLength(0)
    expect(screen.queryByTestId('scope-catch-all-reason')).toBeNull()
  })

  it('choosing a document type adds a chip and marks the tree unsaved', () => {
    const p = scoped('p1')
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)
    expect(screen.queryByTestId('publish-blocked-reason')).toBeNull()

    fireEvent.change(cellSelect('doc'), { target: { value: 'credit_note' } })

    expect(chipLabels(cell('doc'))).toEqual(['Credit notes'])
    expect(screen.getByTestId('publish-blocked-reason').textContent).toContain(BLOCKED_SAVE_FIRST)
  })

  it('removing a chip removes the value', () => {
    const p = scoped('p1', { entity_ids: ['e1', 'e2'] })
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)
    expect(optionLabels(cellSelect('client'))).toEqual(['Add a client…'])

    fireEvent.click(screen.getByRole('button', { name: 'Remove Zenith Foods' }))

    expect(chipLabels(cell('client'))).toEqual(['Alpha Mills'])
    expect(optionLabels(cellSelect('client'))).toEqual(['Add a client…', 'Zenith Foods'])
  })

  it('the band inputs edit the band', async () => {
    const p = scoped('p1')
    const savePolicy = vi.fn(async (x: Policy) => x)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(screen.getByLabelText('Band currency'), { target: { value: 'ngn' } })
    fireEvent.change(screen.getByLabelText('Band from'), { target: { value: '1,000' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    const [sent, opts] = savePolicy.mock.calls[0] as unknown as [Policy, { sendScopes: boolean }]
    expect(sent.scopes.amount_band).toEqual({ lower: '1000', upper: null, currency: 'NGN' })
    expect(opts).toEqual({ sendScopes: true })
  })

  it('a paste that would add a second dot or a third decimal leaves the field as it was', () => {
    const p = scoped('p1')
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)
    const from = () => screen.getByLabelText('Band from') as HTMLInputElement

    fireEvent.change(from(), { target: { value: '1.2' } })
    fireEvent.change(from(), { target: { value: '1.234.56' } })
    expect(from().value).toBe('1.2')
    fireEvent.change(from(), { target: { value: '1.239' } })
    expect(from().value).toBe('1.2')
  })

  it('a bare dot and a leading-dot bound are kept in the field', async () => {
    const p = scoped('p1')
    const savePolicy = vi.fn(async (x: Policy) => x)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)
    const from = () => screen.getByLabelText('Band from') as HTMLInputElement

    fireEvent.change(screen.getByLabelText('Band currency'), { target: { value: 'NGN' } })
    fireEvent.change(from(), { target: { value: '.' } })
    expect(from().value).toBe('.')
    fireEvent.change(from(), { target: { value: '.5' } })
    expect(from().value).toBe('.5')
  })

  it('a client is added and saved as an entity id', async () => {
    const p = scoped('p1')
    const savePolicy = vi.fn(async (x: Policy) => x)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(cellSelect('client'), { target: { value: 'e2' } })
    expect(chipLabels(cell('client'))).toEqual(['Alpha Mills'])
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    expect((savePolicy.mock.calls[0] as unknown as [Policy])[0].scopes).toMatchObject({ entity_ids: ['e2'], document_types: [] })
  })

  it('the three band inputs each edit their own part of the band', async () => {
    const p = scoped('p1', { amount_band: { lower: '10', upper: null, currency: 'NGN' } })
    const savePolicy = vi.fn(async (x: Policy) => x)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(screen.getByLabelText('Band to'), { target: { value: '99.5' } })
    fireEvent.change(screen.getByLabelText('Band currency'), { target: { value: 'usd' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    expect((savePolicy.mock.calls[0] as unknown as [Policy])[0].scopes.amount_band).toEqual({ lower: '10', upper: '99.5', currency: 'USD' })
  })

  it('a scope edit ends the Saved flash', async () => {
    const p = scoped('p1')
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy: vi.fn(async (x: Policy) => x) })} policy={p} />)
    fireEvent.change(nameInput(), { target: { value: 'Renamed' } })
    fireEvent.click(saveButton())
    await waitFor(() => expect(screen.getByText('Saved')).toBeTruthy())

    fireEvent.change(cellSelect('doc'), { target: { value: 'credit_note' } })

    expect(screen.queryByText('Saved')).toBeNull()
  })

  it('clearing the band sends null', async () => {
    const p = scoped('p1', { amount_band: { lower: '10', upper: '20', currency: 'NGN' } })
    const savePolicy = vi.fn(async (x: Policy) => x)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    for (const name of ['Band currency', 'Band from', 'Band to']) fireEvent.change(screen.getByLabelText(name), { target: { value: '' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    expect((savePolicy.mock.calls[0] as unknown as [Policy])[0].scopes.amount_band).toBeNull()
  })

  it('Save draft sends the edited scopes and re-seeds from the answer', async () => {
    const p = scoped('p1')
    const answer = scoped('p1', { workflow_role_keys: ['fin_mgr', 'cfo'] })
    const savePolicy = vi.fn(async () => answer)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(cellSelect('role'), { target: { value: 'fin_mgr' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(chipLabels(cell('role'))).toEqual(['Engagement Manager', 'Engagement Partner']))
    expect((savePolicy.mock.calls[0] as unknown[])[1]).toEqual({ sendScopes: true })
    expect((savePolicy.mock.calls[0] as unknown as [Policy])[0].scopes).toMatchObject({ workflow_role_keys: ['fin_mgr'], entity_ids: [] })
  })

  it('a save with no scope edit sends no scopes', async () => {
    const p = scoped('p1', { workflow_role_keys: ['gone'] })
    const savePolicy = vi.fn(async (x: Policy) => x)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(nameInput(), { target: { value: 'Renamed' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    expect((savePolicy.mock.calls[0] as unknown[])[1]).toEqual({ sendScopes: false })
    expect(chipLabels(cell('role'))).toEqual(['Deleted role'])
  })

  it('a scope edit undone by the answer\'s re-seed resets sendScopes', async () => {
    const p = scoped('p1')
    const savePolicy = vi.fn(async (x: Policy) => x)
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(cellSelect('doc'), { target: { value: 'credit_note' } })
    fireEvent.click(saveButton())
    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.getByText('Saved')).toBeTruthy())
    fireEvent.change(nameInput(), { target: { value: 'Renamed' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(2))
    expect((savePolicy.mock.calls[1] as unknown[])[1]).toEqual({ sendScopes: false })
  })

  it('a scope edit keeps a Deleted role chip and saves', async () => {
    const p = scoped('p1', { workflow_role_keys: ['gone'] })
    const savePolicy = vi.fn(async (x: Policy) => scoped('p1', { workflow_role_keys: ['gone'], document_types: x.scopes.document_types }))
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(cellSelect('doc'), { target: { value: 'credit_note' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    const [sent, opts] = savePolicy.mock.calls[0] as unknown as [Policy, { sendScopes: boolean }]
    expect(opts).toEqual({ sendScopes: true })
    expect(sent.scopes.workflow_role_keys).toEqual(['gone'])
    await waitFor(() => expect(screen.queryByText('Saving…')).toBeNull())
    expect(chipLabels(cell('role'))).toEqual(['Deleted role'])
    expect(screen.getByTestId('scope-deleted-role-note')).toBeTruthy()
    expect(screen.queryByTestId('policy-save-error')).toBeNull()
  })

  it('a Deleted role chip states that Publish is refused', () => {
    const NOTE = 'This role was deleted. Publish is refused while the scope names it — remove it first.'
    const p = scoped('p1', { workflow_role_keys: ['gone'] })
    const { unmount } = render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)
    expect(within(cell('role')).getByTestId('scope-deleted-role-note').textContent).toContain(NOTE)
    unmount()
    const mixed = scoped('p1', { workflow_role_keys: ['fin_mgr', 'gone'] })
    const { unmount: unmountMixed } = render(<WorkflowBuilder ctx={editableCtx(mixed)} policy={mixed} />)
    expect(within(cell('role')).getByTestId('scope-deleted-role-note').textContent).toBe(NOTE)
    unmountMixed()
    const live = scoped('p1', { workflow_role_keys: ['fin_mgr'] })
    render(<WorkflowBuilder ctx={editableCtx(live)} policy={live} />)
    expect(screen.queryByTestId('scope-deleted-role-note')).toBeNull()
  })

  it('the note warns that removing the only deleted roles makes the scope match every creator', () => {
    const p = scoped('p1', { workflow_role_keys: ['gone', 'gone2'] })
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)
    expect(within(cell('role')).getByTestId('scope-deleted-role-note').textContent).toContain('match every creator')
  })

  it('the note omits the match-every-creator warning while a live role remains', () => {
    const p = scoped('p1', { workflow_role_keys: ['fin_mgr', 'gone'] })
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)
    expect(within(cell('role')).getByTestId('scope-deleted-role-note').textContent).not.toContain('match every creator')
  })

  // Sentence built in internal/approval/policy.go (policyStatusForErr, quoteTitles).
  it('a refused publish of a deleted-role scope shows the server sentence and keeps the note', async () => {
    const sentence = 'a scope names a workflow role that was deleted: \u201cTax Reviewer\u201d — remove it from the scope, then publish'
    const p = scoped('p1', { workflow_role_keys: ['gone'] })
    const publishPolicy = vi.fn(() => Promise.reject(new ApiError('http', sentence, 409)))
    render(<WorkflowBuilder ctx={editableCtx(p, { publishPolicy })} policy={p} />)

    fireEvent.click(publishButton())

    await waitFor(() => expect(screen.getByTestId('policy-publish-error').textContent).toBe(sentence))
    expect(screen.getByTestId('scope-deleted-role-note')).toBeTruthy()
    expect(chipLabels(cell('role'))).toEqual(['Deleted role'])
    expect(screen.queryByTestId('policy-save-error')).toBeNull()
  })

  it('removing the deleted role and saving clears the note and leaves Publish on its usual rules', async () => {
    const p = scoped('p1', { workflow_role_keys: ['gone'] })
    const clean = scoped('p1')
    const savePolicy = vi.fn(async () => clean)
    const { unmount } = render(<WorkflowBuilder ctx={editableCtx(clean)} policy={clean} />)
    const expectedDisabled = publishButton().disabled
    unmount()
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.click(screen.getByRole('button', { name: 'Remove Deleted role' }))
    fireEvent.click(saveButton())

    await waitFor(() => expect(savePolicy).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.queryByTestId('scope-deleted-role-note')).toBeNull())
    expect(publishButton().disabled).toBe(expectedDisabled)
  })

  it('a long chip label is truncated with its full text in title', () => {
    const name = 'N'.repeat(80)
    const p = scoped('p1', { entity_ids: ['long'] })
    render(<WorkflowBuilder ctx={editableCtx(p, { entities: [{ id: 'long', name }] })} policy={p} />)

    const label = cell('client').querySelector('button.pf-chip span') as HTMLElement
    expect(label.title).toBe(name)
    expect(label.style.textOverflow).toBe('ellipsis')
  })

  it('a select offers only the values not chosen and resets to its placeholder', () => {
    const p = scoped('p1', { document_types: ['credit_note'] })
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)
    expect(optionLabels(cellSelect('doc'))).toEqual(['Add a document type…', 'Commercial invoices', 'Debit notes'])

    fireEvent.change(cellSelect('doc'), { target: { value: 'commercial_invoice' } })

    expect(cellSelect('doc').value).toBe('')
  })

  it('a chip for an id outside the lists reads Unknown client or Deleted role', () => {
    const p = scoped('p1', { entity_ids: ['nobody'], workflow_role_keys: ['gone'] })
    render(<WorkflowBuilder ctx={editableCtx(p)} policy={p} />)

    expect(chipLabels(cell('client'))).toEqual(['Unknown client'])
    expect(chipLabels(cell('role'))).toEqual(['Deleted role'])
  })

  it('the last policy\'s scope controls are disabled with one visible reason', () => {
    const [a, b, c] = [scoped('a'), scoped('b'), scoped('c', { document_types: ['credit_note'] })]
    render(<WorkflowBuilder ctx={builderCtx({ policies: [a, b, c], roles: FIRM_ROLES, entities: ENTITIES })} policy={c} />)

    const reason = screen.getByTestId('scope-catch-all-reason')
    expect(reason.textContent).toBe(
      'The last policy decides every document no other policy matches, so it takes no scopes. To scope this policy, move it up the list or create a new policy.',
    )
    const controls = Array.from(screen.getByTestId('scope-card').querySelectorAll('select, input, button')) as HTMLInputElement[]
    expect(controls.length).toBe(7)
    for (const c2 of controls) {
      expect(c2.disabled, `${c2.getAttribute('aria-label') ?? c2.tagName} is enabled`).toBe(true)
      expect(c2.getAttribute('aria-describedby')).toBe(reason.id)
    }
  })

  it('the only policy is the last policy', () => {
    const a = scoped('a')
    render(<WorkflowBuilder ctx={builderCtx({ policies: [a], roles: FIRM_ROLES, entities: ENTITIES })} policy={a} />)

    expect(screen.getByTestId('scope-catch-all-reason')).toBeTruthy()
    expect(cellSelect('doc').disabled).toBe(true)
    expect((screen.getByLabelText('Band from') as HTMLInputElement).disabled).toBe(true)
  })

  it('a policy above the last carries no catch-all reason', () => {
    const [a, b, c] = [scoped('a'), scoped('b'), scoped('c')]
    render(<WorkflowBuilder ctx={builderCtx({ policies: [a, b, c], roles: FIRM_ROLES, entities: ENTITIES })} policy={b} />)

    expect(screen.queryByTestId('scope-catch-all-reason')).toBeNull()
    expect(screen.getByTestId('scope-card').querySelectorAll('[aria-describedby]')).toHaveLength(0)
  })

  it('the card is inert while a write is in flight and recovers', async () => {
    const p = scoped('p1', { document_types: ['credit_note'] })
    const sav = deferred<Policy>()
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy: vi.fn(() => sav.promise) })} policy={p} />)
    const controls = () => Array.from(screen.getByTestId('scope-card').querySelectorAll('select, input, button'))
    expect(controls().some(inert)).toBe(false)

    fireEvent.click(saveButton())
    await waitFor(() => expect(screen.getByText('Saving…')).toBeTruthy())
    expect(controls().every(inert), 'a card control stayed live mid-save').toBe(true)

    sav.resolve(p)
    await waitFor(() => expect(controls().some(inert)).toBe(false))
  })

  it('a 400 scope refusal shows the server sentence and keeps the edit', async () => {
    const sentence = 'an amount band names a currency outside the NRS currency list'
    const p = scoped('p1')
    const savePolicy = vi.fn(async () => {
      throw new ApiError('http', sentence, 400)
    })
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(screen.getByLabelText('Band currency'), { target: { value: 'zzz' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(screen.getByTestId('policy-save-error').textContent).toBe(sentence))
    expect((screen.getByLabelText('Band currency') as HTMLInputElement).value).toBe('ZZZ')
  })

  it('a 409 catch-all refusal shows the server sentence', async () => {
    const sentence = 'a policy with no scopes must stay last while any policy has scopes'
    const p = scoped('p1')
    const savePolicy = vi.fn(async () => {
      throw new ApiError('http', sentence, 409)
    })
    render(<WorkflowBuilder ctx={editableCtx(p, { savePolicy })} policy={p} />)

    fireEvent.change(cellSelect('doc'), { target: { value: 'credit_note' } })
    fireEvent.click(saveButton())

    await waitFor(() => expect(screen.getByTestId('policy-save-error').textContent).toBe(sentence))
  })

  it('an idle entities fetch shows Loading', () => {
    const p = scoped('p1')
    render(<WorkflowBuilder ctx={editableCtx(p, { entitiesState: 'idle' })} policy={p} />)
    expect(screen.queryByTestId('scope-card')).toBeNull()
  })

  it('an unlanded entities fetch shows Loading, an errored one ErrorState with retry', () => {
    const p = scoped('p1')
    const { unmount } = render(<WorkflowBuilder ctx={editableCtx(p, { entitiesState: 'loading' })} policy={p} />)
    expect(screen.queryByTestId('scope-card')).toBeNull()
    unmount()

    const refetchEntities = vi.fn()
    render(<WorkflowBuilder ctx={editableCtx(p, { entitiesState: 'error', entitiesError: new ApiError('http', 'clients down', 503), refetchEntities })} policy={p} />)
    expect(screen.getByText('clients down')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(refetchEntities).toHaveBeenCalledTimes(1)
  })
})
