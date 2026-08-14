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
import { SIM_DEFAULT, type NotifyNode, type Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { WorkflowBuilder } from './WorkflowBuilder'
// Rendered bare by the R15 oracle only — every other spec reaches them through the builder.
import { WfSelect, WfToggle } from './WorkflowParts'

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
const DELEGATION_BLOCKED_REASON = 'Delegation is switched off — the server has nowhere to store it yet.'

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

/**
 * The picker's eligibility note — the hint under the 'Delegate to' select. Located by
 * POSITION, not by text: its wording is the thing the specs below assert.
 */
function delegateNote(): string {
  const picker = control(screen.getByLabelText('Delegate to'))
  const wrap = picker.closest('div')
  expect(wrap, 'the delegate picker has no wrapping div, so the note cannot be located').toBeTruthy()
  const note = wrap!.lastElementChild
  expect(note, 'the delegate picker has no sibling hint').toBeTruthy()
  expect(note!.contains(picker), 'the located node is the picker itself, so the assertions below are vacuous').toBe(false)
  return note!.textContent ?? ''
}

/**
 * The delegation reason node, by TESTID and never by literal: the sentence is the executor's
 * to write, and a spec that imported it would follow a typo into the product. Every assertion
 * below reads its properties.
 */
function delegationReason(): HTMLElement {
  return screen.getByTestId('delegation-blocked-reason')
}

/** Its visible text, trimmed the way RTL trims before matching a text query. */
function reasonText(el: HTMLElement): string {
  return (el.textContent ?? '').trim()
}

/** The two controls one cause shuts. Thunks: the guard drop remounts this subtree. */
function delegationControls(): [string, HTMLElement][] {
  return [
    ['the Allow delegation switch', screen.getByRole('switch', { name: 'Allow delegation' })],
    ['the Delegate to select', control(screen.getByLabelText('Delegate to'))],
  ]
}

describe('APPR-10-04 AC-2/AC-4: delegation ships shut, and says so where text can reach', () => {
  // RE-AUTHORED from APPR-09-05's 'the delegation controls say the choice is not stored'. That
  // spec asserted `toggle.disabled === false` on purpose, with the message 'APPR-10 owns
  // disabling this control, not APPR-09' — the written hand-off. Inverted here, not deleted.
  it('the toggle reports disabled on ITSELF, not through a fieldset ancestor', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const toggle = screen.getByRole('switch', { name: 'Allow delegation' }) as HTMLButtonElement
    expect(toggle.disabled, 'the switch still takes a delegation the server drops on write').toBe(true)
    // No write is in flight, so the inspector's own `<fieldset disabled>` is open and the only
    // thing that can have shut this control is its own prop. AC-2 names that distinction.
    expect(toggle.closest('fieldset[disabled]'), 'the switch is inert only by ancestry, which AC-2 rules out').toBeNull()
    expect(inert(toggle), "the file's own inertness helper disagrees with the IDL property").toBe(true)
  })

  it('the reason is a real text node, not a title or an aria attribute', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const reason = delegationReason()
    const text = reasonText(reason)
    expect(text, 'the reason node is empty, so nothing on screen states the disable').not.toBe('')
    // getByText never matches a title/aria attribute, and RTL matches only a node's DIRECT text
    // children — so resolving the same string this way IS the visible-node proof.
    expect(screen.getByText(text), 'the reason reaches the accessibility tree but not the screen').toBe(reason)
    expect(hintColor(reason), 'a layer-3 reason renders grey, not amber').toBe('var(--fg-3)')
  })

  // KEEP-GREEN, and kept in its OWN `it` on purpose: this is APPR-10-03's shipped AC-8, and
  // folding it into the RED spec below would make it unreachable until this subtask lands.
  // Asserted on its properties, not its literal: APPR-00 Q1 widened the eligible set to
  // {admin, reviewer}, and the sentence naming that set is the executor's to write.
  it('the picker keeps its own eligibility note, naming both approver roles', () => {
    const on: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true }] }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={on} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const note = delegateNote()
    expect(/Admin/.test(note), `the eligibility note does not name the Admin role: ${note}`).toBe(true)
    expect(/Reviewer/.test(note), `the eligibility note does not name the Reviewer role: ${note}`).toBe(true)
    expect(/not available yet/i.test(note), `the eligibility note does not say delegation is not available yet: ${note}`).toBe(true)
  })

  it('the reason renders once, not twice, when delegation is on — and stays worded apart from both neighbours', () => {
    const on: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true }] }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={on} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const reason = delegationReason()
    const text = reasonText(reason)
    expect(screen.getAllByText(text), 'the reason sits both inside and outside the delegate block').toHaveLength(1)

    // The LIVE three-way distinctness. The APPR-10-03 block below pins the same property against
    // the RETIRED literal, which cannot see a collision between the NEW sentence and its two
    // neighbours. AC-8's own trap: two identical strings send the count above to 2.
    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    const label = Array.from(picker.options).find((o) => o.value === '')?.textContent ?? ''
    expect(label, 'the picker lost its sentinel option, so the comparison below is vacuous').not.toBe('')
    expect(text, 'the reason and the fallback option label collapsed into one sentence').not.toBe(label)
    // R9 keeps DELEGATE_NOTE the picker wrapper's LAST child, so `delegateNote()` still reads it
    // and not the new reason. This is the assertion that catches the swap if it does not.
    const note = delegateNote()
    expect(text, 'the reason and the eligibility note collapsed into one sentence').not.toBe(note)
  })
})

// ----------------------------------------------------------------------------
// APPR-10-03 — the delegate copy names the approver set {admin, reviewer}
// ----------------------------------------------------------------------------

describe('APPR-10-03 AC-3: the sentinel option names the approver set', () => {
  it('the default option names both approver roles, and stays worded apart from the note', () => {
    const on: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true }] }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={on} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    const sentinel = Array.from(picker.options).find((o) => o.value === '')
    expect(sentinel, "the picker lost its '' sentinel option, so the label below cannot be read").toBeTruthy()

    const label = sentinel!.textContent ?? ''
    expect(/Admin/.test(label), `the fallback option does not name the Admin role: ${label}`).toBe(true)
    expect(/Reviewer/.test(label), `the fallback option does not name the Reviewer role: ${label}`).toBe(true)

    // WorkflowInspector.tsx:36-37 — the option names the FALLBACK, the note states the
    // ELIGIBILITY RULE. Two registers, deliberately not harmonised into one sentence.
    const note = delegateNote()
    expect(label, 'the option and the note collapsed into one sentence').not.toBe(note)
    expect(note.includes(label), 'the note swallowed the option label verbatim').toBe(false)
    expect(label.includes(note), 'the option label swallowed the note verbatim').toBe(false)
    // Neither may become DELEGATION_BLOCKED_REASON, or the one-node count at :706 breaks.
    expect(label).not.toBe(DELEGATION_BLOCKED_REASON)
    expect(note).not.toBe(DELEGATION_BLOCKED_REASON)
  })
})

describe('QA APPR-10-03: the picker offers the roster its own predicate admits', () => {
  // `builderCtx` defaults to `members: []`, so every other spec renders a picker holding
  // nothing but the sentinel. Without this one, `delegates={delegateCandidates(ctx.members)}`
  // could be starved or re-narrowed in the component and no spec would notice.
  // Reviewer BEFORE admin, so role order and roster order disagree.
  const ROSTER: Member[] = [
    { id: 'm1', name: 'Folake Adesina', initials: 'FA', email: null, role: 'preparer', status: 'active', isYou: false },
    { id: 'm2', name: 'Musa Danjuma', initials: 'MD', email: null, role: 'reviewer', status: 'active', isYou: false },
    { id: 'm3', name: 'Chinedu Okafor', initials: 'CO', email: null, role: 'admin', status: 'active', isYou: false },
    { id: 'm4', name: 'Halima Yusuf', initials: 'HY', email: null, role: 'reviewer', status: 'suspended', isYou: false },
  ]

  it('names the active reviewer and the active admin in roster order, and nobody else', () => {
    const on: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true }] }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES, members: ROSTER })} policy={on} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    const named = Array.from(picker.options)
      .filter((o) => o.value !== '')
      .map((o) => o.textContent)
    expect(named, 'the picker offers no named delegate at all, so the order below is vacuous').toHaveLength(2)
    expect(named).toEqual(['Musa Danjuma', 'Chinedu Okafor'])
  })
})

// ----------------------------------------------------------------------------
// APPR-10-04 — delegation ships disabled, with all four layers
// ----------------------------------------------------------------------------
// RED, written before the implementation. The recipe's nearest precedent is on this very
// surface: Publish at WorkflowBuilder.tsx:417-440 — real `disabled`, inline paint, `title`,
// `aria-describedby`, and a visible sibling carrying the sentence.

describe('APPR-10-04 AC-3: the delegate picker renders even with delegation off', () => {
  it('the picker is on screen with node.delegate false, and reports disabled on the control itself', () => {
    // `lib/policies.ts:131` forces `delegate: false` on every wire read, so OFF is the only
    // state this screen can reach. Behind `{node.delegate && (…)}` a shut toggle REMOVES the
    // picker rather than labelling it (P27) — the guard is what this pins.
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const toggle = screen.getByRole('switch', { name: 'Allow delegation' })
    expect(toggle.getAttribute('aria-checked'), 'the fixture is not the delegation-off state this pins').toBe('false')

    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    expect(picker.tagName, 'the delegate handle is not a <select>, so `.disabled` below reads nothing').toBe('SELECT')
    expect(picker.disabled, 'the picker still takes a delegate the server drops on write').toBe(true)
    expect(picker.closest('fieldset[disabled]'), 'the picker is inert only by ancestry, which AC-2 rules out').toBeNull()
    expect(inert(picker), "the file's own inertness helper disagrees with the IDL property").toBe(true)
  })
})

describe('APPR-10-04 AC-4: one reason node, two aria-describedby pointers', () => {
  // R10 — a deliberate deviation from INVED-02, where every disabled control gets its OWN id
  // (InvoiceDetail.tsx:150-159). There the causes differ per control; here one cause shuts both,
  // and a second copy of the sentence would break the one-node counts above.
  it('both controls point at the reason node id and mirror its text in title', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const reason = delegationReason()
    const text = reasonText(reason)
    expect(reason.id, 'the reason node has no id, so aria-describedby cannot reach it').not.toBe('')

    const targets = delegationControls()
    expect(targets.length, 'nothing was checked').toBeGreaterThan(0)
    for (const [what, el] of targets) {
      expect(el.getAttribute('aria-describedby'), `${what} does not point at the reason node`).toBe(reason.id)
      expect((el.getAttribute('title') ?? '').trim(), `${what}'s tooltip does not mirror the reason`).toBe(text)
    }

    // A LIVE id collision, not a hypothetical one: the edit below makes the tree dirty, so the
    // publish reason renders alongside this one (InvoiceDetail.test.tsx:2065-2068's shape).
    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })
    const publishReason = screen.getByTestId('publish-blocked-reason')
    expect(publishReason.id, 'the publish reason lost its id, so the check below is vacuous').toBe('publish-blocked-reason-text')
    expect(delegationReason().id, 'the delegation reason reuses PUBLISH_BLOCKED_REASON_ID').not.toBe(publishReason.id)
  })
})

describe('APPR-10-04 AC-5: both shut controls carry the muted paint', () => {
  // P5 — no `:disabled` rule exists anywhere in this repo, so the inline paint is mandatory
  // rather than stylistic. The PROPERTY NAME differs per control and one shared assertion would
  // pin the wrong name on one of them: app-layer.css:224-232 forbids the `background` shorthand
  // on `.pf-select`, whose own resting style sets `backgroundColor` (WorkflowParts.tsx:235),
  // while `WfToggle`'s resting style sets the shorthand (:305). Both are INLINE — the
  // `.pf-toggle` class sets only a transition (platform.css:169-171). Measured in jsdom: a
  // select's `style.background` reads '' and a toggle's `style.backgroundColor` reads ''.
  it('the select paints backgroundColor, the toggle paints background, and both mute the rest', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const toggle = screen.getByRole('switch', { name: 'Allow delegation' })
    const picker = control(screen.getByLabelText('Delegate to'))
    expect(picker.style.backgroundColor, 'the shut picker is still painted as a live control').toBe('var(--bg-3)')
    expect(toggle.style.background, 'the shut switch is still painted as a live control').toBe('var(--bg-3)')

    for (const [what, el] of delegationControls()) {
      expect(el.style.color, `${what} keeps its live foreground`).toBe('var(--fg-4)')
      expect(el.style.cursor, `${what} still invites a click`).toBe('not-allowed')
      // NO `filter: 'none'` (R13). The unguarded `filter: brightness(1.22)` at
      // app-layer.css:213 targets `.v2-btn-primary` alone; `.pf-toggle` and `.pf-select` carry
      // no `:hover` rule at all, so neutralising a filter here would state a hazard that does
      // not exist. Pinned rather than left silent — InvoiceDetail.test.tsx:2071-2084's shape.
      expect(el.style.filter, `${what} neutralises a filter no rule applies to it`).toBe('')
    }
  })
})

describe('APPR-10-04 AC-4: the shut switch refuses the keyboard, and its reason stays reachable', () => {
  // Layer 3 is the whole justification for rendering disabled rather than hidden: a disabled
  // control is out of the tab order, so the sentence must stay reachable even though the control
  // is not. KILLS `aria-hidden="true"` on the reason node (InvoiceDetail.test.tsx:2320-2337).
  // No `aria-disabled`: native `disabled` on a <button> already covers focus, the keyboard and
  // the a11y tree, and this repo uses `aria-disabled` nowhere.
  it('focus is refused, Enter and Space never flip it, and the reason is not hidden', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const toggle = screen.getByRole('switch', { name: 'Allow delegation' })
    const before = toggle.getAttribute('aria-checked')
    toggle.focus()
    expect(document.activeElement, 'a disabled switch is out of the tab order').not.toBe(toggle)
    for (const key of ['Enter', ' ']) {
      fireEvent.keyDown(toggle, { key })
      fireEvent.keyUp(toggle, { key })
    }
    expect(toggle.getAttribute('aria-checked'), 'the keyboard armed a switch the pointer cannot').toBe(before)

    // The focus half applies to the picker too; a <select> has no Enter/Space semantics to pin.
    const picker = control(screen.getByLabelText('Delegate to'))
    picker.focus()
    expect(document.activeElement, 'a disabled select is out of the tab order').not.toBe(picker)

    const reason = delegationReason()
    expect(reason.getAttribute('aria-hidden'), 'the only layer a screen reader can still reach is hidden from it').toBeNull()
    expect(reason.hasAttribute('hidden'), 'the reason node is hidden outright').toBe(false)
    expect(screen.getByText(reasonText(reason))).toBe(reason)
  })
})

describe('APPR-10-04 QA (R14): the approval panel enumerates its controls', () => {
  // The condition panel got this pin in APPR-10-02 (`the condition panel gained or lost a
  // control`); the approval panel never had one. Dropping the delegate guard adds a control to
  // this card PERMANENTLY, which is exactly the change an enumeration exists to hold.
  it('renders Remove, the role select, Manage roles, the deadline, the switch and the picker — and nothing else', () => {
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
      ['SELECT', 'Delegate to'],
    ])
  })
})

describe('APPR-10-04 QA (R15): the disabled paint is CONDITIONAL', () => {
  // THE MUTATION ORACLE. An unconditional paint spread inside either shared primitive satisfies
  // every assertion above and silently repaints all ten `WfSelect` call sites — two of them on
  // the Members screen this story never opens (MemberParts.tsx:427, MembersView.tsx:127). Only
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
  it('selecting an approval step adds no wrapper — still three, still one flex', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    // The panel is genuinely mounted, or the count below measures the no-selection card.
    expect(screen.getByLabelText('Delegate to'), 'no approval panel is open, so this counts the wrong tree').toBeTruthy()

    const wrappers = Array.from(document.querySelectorAll('fieldset'))
    expect(wrappers, 'the scope row, the canvas and the inspector — the delegation recipe adds none').toHaveLength(3)
    expect(wrappers.filter((fs) => (fs as HTMLElement).style.display === 'flex'), 'exactly one wrapper blockifies its child').toHaveLength(1)
    // AC-2's distinction, stated as a tree property rather than per control: nothing inside the
    // open inspector is shut by ancestry, so every `.disabled` above is the control's own.
    const inspectorFieldsets = wrappers.filter((fs) => fs.contains(screen.getByLabelText('Delegate to')) && fs.hasAttribute('disabled'))
    expect(inspectorFieldsets, 'a disabled fieldset now wraps the picker, which AC-2 rules out').toHaveLength(0)
  })
})

describe('APPR-10-04 QA (G1/G2): the picker keeps its own wrapper, and the reason sits outside it', () => {
  // `delegateNote()` is POSITIONAL — it climbs to the picker's nearest `div` ancestor and reads
  // that node's LAST element child. G2 called the `marginTop: 12` wrapper load-bearing for
  // exactly that reason, but no assertion held it: deleting the wrapper outright, and deleting
  // only its margin, BOTH survived the suite. Without this, the helper silently re-points at the
  // inspector body the first time anything is appended below the delegation block.
  it('the wrapper is the picker\'s own div, holds the note, and excludes the reason', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const picker = control(screen.getByLabelText('Delegate to'))
    const wrap = picker.closest('div') as HTMLElement
    expect(wrap, 'the picker has no wrapping div, so delegateNote() reads whatever is above it').toBeTruthy()

    const body = screen.getByTestId('step-inspector-body')
    expect(wrap, 'the wrapper IS the inspector body — the picker lost its own div').not.toBe(body)
    expect(body.contains(wrap), 'the wrapper is not inside the inspector body').toBe(true)
    // AUTHORED-VALUE pin, the shape :1258 already uses: jsdom runs no layout, so this holds what
    // the component ASKS for. The gap between the reason above and the picker below is the only
    // thing separating two sentences and a control in a 320px column.
    expect(wrap.style.marginTop, 'the picker block lost the gap that separates it from the reason above').toBe('12px')

    const reason = delegationReason()
    expect(wrap.contains(reason), 'the reason moved inside the picker block, where it can become delegateNote()\'s last child').toBe(false)

    // The wrapper's last child must be the ELIGIBILITY note, which is what `delegateNote()`
    // returns. Compared against that helper rather than restated, so the two cannot drift, and
    // against the note's own properties so neither side can be satisfied by an empty node.
    const last = wrap.lastElementChild
    expect(last, 'the wrapper is empty, so the identity below is vacuous').toBeTruthy()
    const note = delegateNote()
    expect(note, 'delegateNote() read an empty node').not.toBe('')
    expect(last!.textContent, 'the wrapper\'s last child is not the node delegateNote() returns').toBe(note)
    expect(/Admin/.test(note) && /Reviewer/.test(note), `the located last child is not the eligibility note: ${note}`).toBe(true)
    expect(note, 'delegateNote() re-pointed at the delegation reason').not.toBe(reasonText(reason))
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

describe('APPR-10-04 QA AC-4: which layer actually carries the reason', () => {
  // The executor flagged that a `title` on a DISABLED form control will most likely never raise
  // a tooltip — browsers suppress mouse events on disabled controls, so the hover that would
  // show it never fires. That is not a reason to drop layer 4; it is the reason layer 3 exists.
  // This pins the division of labour: `title` and `aria-describedby` are present and correct,
  // and the VISIBLE text node is the only one a text query — or a reader who cannot focus the
  // control — can reach.
  it('title and aria-describedby are present, and the visible node is what carries the sentence', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const reason = delegationReason()
    const text = reasonText(reason)
    expect(text, 'the reason node is empty').not.toBe('')

    const targets = delegationControls()
    expect(targets.length, 'nothing was checked').toBeGreaterThan(0)
    for (const [what, el] of targets) {
      // Layer 4 is present — it just cannot be the one that does the work.
      expect(el.getAttribute('title'), `${what} lost layer 4`).toBe(text)
      // …and it is NOT reachable as text. `getByText` walks text nodes, never attributes, so a
      // control whose only reason is a `title` would leave this query with nothing but the node.
      expect(el.textContent?.includes(text), `${what} states the reason as its own text, not through a sibling`).toBe(false)
    }
    // Exactly one node in the whole document renders the sentence, and it is the sibling — not
    // either control, and not a duplicate the two `aria-describedby` pointers made necessary.
    const carriers = screen.getAllByText(text)
    expect(carriers, 'the sentence renders somewhere other than the one reason node').toHaveLength(1)
    expect(carriers[0], 'a control carries the sentence as its own text').toBe(reason)
    expect(reason.tagName, 'the reason is itself a control, so it competes for the tab order').toBe('DIV')
  })
})

describe('APPR-10-04 QA AC-8: the THIRD register is held the way the first two are', () => {
  // `:743-750` pins the option label and the note against each other on IDENTITY *and*
  // CONTAINMENT. The reason sentence joined that column as a third string and got identity
  // only — measured: rewriting it to `Delegation is not available yet.` passed the whole suite,
  // even though that is DELEGATE_NOTE's own clause, pinned as such at :709 and forbidden to
  // this sentence in as many words (`must carry the disable in DIFFERENT words`).
  //
  // 320px of column carries all three at once. Each states a different thing — the option names
  // the FALLBACK, the note states the ELIGIBILITY RULE, this states the DISABLE and its CAUSE —
  // and two of them echoing each other is the failure this holds.
  it('the reason neither repeats the note\'s pinned clause nor swallows either neighbour', () => {
    const on: Policy = { ...policyWith('fin_mgr'), nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true }] }
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={on} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const reason = reasonText(delegationReason())
    const note = delegateNote()
    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    const label = Array.from(picker.options).find((o) => o.value === '')?.textContent ?? ''
    // Every one of the three resolved to real copy, or the comparisons below are vacuous.
    for (const [what, s] of [['the reason', reason], ['the note', note], ['the option label', label]] as [string, string][]) {
      expect(s, `${what} read empty`).not.toBe('')
    }

    // `not available yet` belongs to DELEGATE_NOTE — :709 requires it there, which is exactly
    // why this sentence may not borrow it.
    expect(/not available yet/i.test(note), 'the clause moved off the note, so the exclusion below guards nothing').toBe(true)
    expect(/not available yet/i.test(reason), `the reason repeats the note's own clause instead of stating the cause: ${reason}`).toBe(false)

    // CONTAINMENT, not just identity — the half the reason never got.
    for (const [what, other] of [['the eligibility note', note], ['the fallback option label', label]] as [string, string][]) {
      expect(reason.includes(other), `the reason swallowed ${what} verbatim`).toBe(false)
      expect(other.includes(reason), `${what} swallowed the reason verbatim`).toBe(false)
    }
  })
})

describe('APPR-10-04 QA AC-9: the sweep\'s two handles exist locally', () => {
  // AC-9 runs only at the deploy gate, so the two `data-testid`s it resolves are unreachable by
  // every local check. Measured: deleting `step-inspector-body` outright failed nothing here.
  // These are the handles, pinned as literals on purpose — a spec that imported the constants
  // would follow a rename into the e2e spec and stay green while the sweep broke.
  it('the inspector body and the reason node carry the ids e2e/topology/workflows.spec.ts resolves', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const body = screen.getByTestId('step-inspector-body')
    const reason = screen.getByTestId('delegation-blocked-reason')
    // `outer` must actually contain `inner`, or `assertFillsColumn` measures two unrelated boxes
    // and its gap arithmetic returns numbers that mean nothing.
    expect(body.contains(reason), 'the reason is not inside the column the sweep measures against').toBe(true)
    // The sweep compares the reason's gutters to the Deadline select's, so both must share ONE
    // parent — the padded body itself. A nested inner box makes the comparison read its own
    // wrapper's padding rather than the column's.
    const deadlineLabel = screen.getByLabelText('Deadline').closest('label') as HTMLElement
    expect(deadlineLabel, 'the Deadline select has no wrapping label for the sweep to measure').toBeTruthy()
    expect(reason.parentElement, 'the reason node and the Deadline select no longer share a parent').toBe(deadlineLabel.parentElement)
    expect(reason.parentElement, 'their shared parent is not the body the sweep anchors on').toBe(body)
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
  // is `inline-block` (WorkflowParts.tsx:220) and was blockified for free as a direct flex item
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

// ----------------------------------------------------------------------------
// APPR-09-06 follow-up — the paint tracks the lock
// ----------------------------------------------------------------------------
// Measured on the deployed build: mid-write both controls carried `disabled` while rendering
// pixel-identical to their live state — same background, `cursor: pointer`, opacity 1. The
// `disabled` ATTRIBUTE took two causes, the disabled APPEARANCE took one.
//
// These read the INLINE style, not `getComputedStyle`: jsdom applies no stylesheet, which is
// exactly why a green 2137-spec suite never saw this. The inline layer is also where the fix
// belongs — it is what outranks `.v2-btn-ghost:hover` (app-layer.css:215).

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
    expect(publish.style.color).toBe('var(--text-on-dark)')
    expect(publish.style.cursor, 'a live control carries no inline cursor — `.asc-app .v2-btn` supplies `pointer`').toBe('')

    fireEvent.click(publish)

    expect(publish.disabled, 'the lock never closed, so the paint assertions below are vacuous').toBe(true)
    expect(publish.style.background, 'a dead Publish is still painted as the action').toBe('var(--bg-3)')
    expect(publish.style.color, 'a dead Publish still carries the on-dark label colour').toBe('var(--fg-4)')
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

describe('APPR-10-04 QA AC-2: the shut toggle takes no click, and the reason stays one node', () => {
  // RE-AUTHORED from APPR-09-05's 'flipping the toggle twice never duplicates or drops the
  // note', which clicked the switch three times and counted the sentence after each flip. Once
  // the switch is disabled React fires no onClick for it, so `aria-checked` never moves and that
  // spec's own vacuity guard failed BEFORE its count ran — unfixable-green. Retired here rather
  // than deleted, the way 'two writes, one after the other' was: the property it existed for —
  // the reason is exactly ONE node, whatever the toggle state — is kept, on the post-change
  // behaviour. The delegation-ON half of it now lives in the AC-4 block above.
  it('clicking the shut toggle does not flip aria-checked, and the reason still renders once', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={policyWith('fin_mgr')} />)
    fireEvent.click(screen.getByText('Engagement Manager must approve'))

    const toggle = screen.getByRole('switch', { name: 'Allow delegation' }) as HTMLButtonElement
    expect(toggle.disabled, 'the switch is live, so refusing a click is not the property this pins').toBe(true)
    const before = toggle.getAttribute('aria-checked')
    expect(before, 'the fixture does not ship delegation OFF').toBe('false')

    fireEvent.click(toggle)

    expect(toggle.getAttribute('aria-checked'), 'a disabled switch took a click and wrote to the working tree').toBe(before)
    expect(screen.getAllByText(reasonText(delegationReason())), 'the reason is not exactly one node').toHaveLength(1)
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

// ----------------------------------------------------------------------------
// APPR-10-01 — the Applies control offers only the scope the server stores
// ----------------------------------------------------------------------------
// A LITERAL, per the same rule as the copy consts above: a test that imports the string
// it checks would follow a typo into the product.

const SCOPE_NOT_ROUTED = 'Per-scope routing is not yet available — every policy applies to all invoices.'

/** The scope <select>, descended from the aria-labelled <label> wrapper `hideLabel` produces. */
function scopeSelect(): HTMLSelectElement {
  return control(screen.getByLabelText('Applies')) as HTMLSelectElement
}

describe('APPR-10-01 AC-1/AC-2: the scope select offers one option', () => {
  it('renders exactly the one scope the server will store', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    const sel = scopeSelect()
    expect(sel.tagName, 'the handle resolved to the label wrapper, so the option count below is vacuous').toBe('SELECT')
    expect(Array.from(sel.options).map((o) => o.value), 'the editor still offers scopes the server refuses').toEqual(['All invoices'])
  })
})

describe('APPR-10-01 AC-3: the row says per-scope routing is not available', () => {
  it('the sentence is on screen exactly once, in the hint register', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    // getByText never matches a title/aria attribute, so finding it IS the visible-node assertion.
    const note = screen.getByText(SCOPE_NOT_ROUTED)
    expect(screen.getAllByText(SCOPE_NOT_ROUTED), 'the sentence renders more than once').toHaveLength(1)
    expect(hintColor(note), 'an explanation renders grey, not amber').toBe('var(--fg-3)')
  })
})

describe('APPR-10-01 AC-11: the scope select stays enabled at rest', () => {
  // KEEP-GREEN pin, green before the change: D-A takes one option plus copy, and rejects the
  // four-layer disabled recipe on this control. The comment at :627 is APPR-10-04's, about
  // the delegation toggle, and must not be read as licence to disable this one.
  it('carries no disabled, on itself or on a wrapper', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    const sel = scopeSelect()
    expect(sel.disabled, 'D-A: the scope select is explained, not disabled').toBe(false)
    expect(inert(sel), 'D-A: a disabled fieldset ancestor disables it just as well').toBe(false)
  })
})

// --- APPR-10-01 QA: adversarial cover over the four ACs above -----------------

describe('APPR-10-01 QA AC-2: the one option is labelled, and it is the one on screen', () => {
  it('carries a visible label and is the control’s current value', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    const opts = Array.from(scopeSelect().options)
    // SCOPE_OPTIONS maps value AND label; a valued option with a blank label passes an
    // option-value assertion while the dropdown renders empty.
    expect(opts, 'no options rendered, so the label assertion below is vacuous').toHaveLength(1)
    expect(opts[0].textContent, 'a valued option with no label renders a blank dropdown').toBe('All invoices')
    expect(scopeSelect().value, 'the control does not show the scope it was handed').toBe('All invoices')
  })
})

describe('APPR-10-01 QA AC-3/AC-11: the note is copy, not the disabled recipe (D-A)', () => {
  it('no tooltip, no aria-describedby, no dead paint, no fieldset', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    const sel = scopeSelect()
    const note = screen.getByText(SCOPE_NOT_ROUTED)
    expect(sel.title, 'a tooltip is layer 2 of the disabled recipe, and this control is live').toBeFalsy()
    expect(sel.getAttribute('aria-describedby'), 'D-A: the note explains the product, it does not describe a blocked control').toBeNull()
    expect(note.getAttribute('title'), 'the sentence must be the visible node, never a tooltip carrying it').toBeNull()
    expect(sel.style.cursor, 'not-allowed is disabled paint on a control D-A keeps live').not.toBe('not-allowed')
    // Outside the in-flight wrapper, so the explanation does not grey out mid-write.
    expect(note.closest('fieldset'), 'a fieldset ancestor also reds the 3-fieldset count at :974').toBeNull()
  })

  it('the note is not an Applies-labelled second match', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    // A second match makes getByLabelText('Applies') throw at :752, :841 and :990.
    expect(screen.getAllByLabelText('Applies'), 'the new node introduced a second Applies handle').toHaveLength(1)
  })
})

describe('APPR-10-01 QA AC-3: the explanation outlives a write', () => {
  it('stays on screen, and stays live, inside a save round trip', () => {
    const self = policyWith('fin_mgr')
    const sav = deferred<Policy>()
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self], savePolicy: vi.fn(() => sav.promise) })} policy={self} />)

    expect(screen.getAllByText(SCOPE_NOT_ROUTED), 'the sentence is missing before the write, so the check below is vacuous').toHaveLength(1)
    fireEvent.click(saveButton())

    // The select goes inert here (:771); the sentence explains the PRODUCT, not the write.
    expect(screen.getAllByText(SCOPE_NOT_ROUTED), 'the explanation vanished mid-write').toHaveLength(1)
    expect(inert(screen.getByText(SCOPE_NOT_ROUTED)), 'the explanation greys out with the control it explains').toBe(false)
  })
})

describe('APPR-10-01 QA: a policy carrying a retired scope (the hazard D-B accepted)', () => {
  // D-B declined a defensive `scopeOptions(current)` prepend because the column's birth
  // CHECK makes this unreachable in production. Characterisation, not endorsement: if the
  // prepend is ever added, this fails and forces the decision to be retaken.
  it('renders the select blank rather than inventing an option for it', () => {
    const stale: Policy = { ...policyWith('fin_mgr'), scope: 'Capex & fixed assets' }
    render(<WorkflowBuilder ctx={builderCtx()} policy={stale} />)

    const sel = scopeSelect()
    expect(Array.from(sel.options).map((o) => o.value), 'the retired scope was prepended back into the list').toEqual(['All invoices'])
    expect(sel.value, 'the control silently rewrote the policy’s stored scope').not.toBe('Capex & fixed assets')
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
    scope: 'All invoices',
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
// Both sentences are located by TESTID and never by literal — the idiom `delegation-blocked-reason`
// already sets (:638): the wording is the executor's to write, and a spec that imported it would
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
    // :1084-1087 reserves this clause to DELEGATE_NOTE and forbids it to the delegation reason by
    // name. A fourth borrower makes that exclusion arbitrary.
    expect(/not available yet/i.test(text), `the claim borrows DELEGATE_NOTE's own clause: ${text}`).toBe(false)
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
  // R19: the delegation trio renders only under an APPROVAL node and this one only under a NOTIFY
  // node, so the three-way pins at :699-721, :726-750 and :1060-1096 can never see it and are left
  // alone. Its real collision is the simulator claim, which DOES co-render (G2). The three
  // neighbours are read LIVE off the approval panel here — a re-declared literal that had drifted
  // would make every comparison below vacuous.
  it('T5-9: the notify claim is distinct — identity and containment — from the simulator claim and the three delegation registers', () => {
    render(<WorkflowBuilder ctx={builderCtx({ roles: FIRM_ROLES })} policy={notifyPolicy()} />)

    fireEvent.click(within(canvasPanel()).getByText('Engagement Manager must approve'))
    const picker = control(screen.getByLabelText('Delegate to')) as HTMLSelectElement
    const NEIGHBOURS: [string, string][] = [
      ['the sentinel option label', Array.from(picker.options).find((o) => o.value === '')?.textContent ?? ''],
      ['the eligibility note', delegateNote()],
      ['the delegation reason', reasonText(delegationReason())],
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
    expect(/not available yet/i.test(text), `the claim borrows DELEGATE_NOTE's own clause: ${text}`).toBe(false)
  })
})
