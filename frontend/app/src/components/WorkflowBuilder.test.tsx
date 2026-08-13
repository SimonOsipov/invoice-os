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
const NO_POLICY_IN_FORCE = 'No policy is in force. Publishing puts this one in force.'
const DELEGATION_NOT_STORED = 'Delegation is not stored yet — this choice is not saved.'

const FIRM_ROLES: Role[] = [
  { key: 'fin_mgr', title: 'Engagement Manager', desc: 'First sign-off on a client invoice', members: [] },
  { key: 'cfo', title: 'Engagement Partner', desc: 'Signs off invoices above ₦1bn', members: [] },
]

function publishButton(): HTMLButtonElement {
  return screen.getByRole('button', { name: 'Publish' }) as HTMLButtonElement
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
    const self: Policy = { ...policyWith('fin_mgr'), status: 'published', version: 2, activeVersion: 2 }
    render(<WorkflowBuilder ctx={builderCtx({ policies: [self] })} policy={self} />)

    expect(screen.getByText('Publishing replaces v2 of this policy, which is in force now. There is no undo.')).toBeTruthy()
    expect(screen.queryByText(NO_POLICY_IN_FORCE), 'the note claims nothing is in force while this policy holds the slot').toBeNull()
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
    expect(screen.getByText('Publishing replaces v1 of this policy, which is in force now. There is no undo.')).toBeTruthy()
    expect(screen.queryByText(NO_POLICY_IN_FORCE)).toBeNull()
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

describe('APPR-09-05: the builder carries exactly two write controls', () => {
  it('one Save draft and one Publish, and nothing else claims either verb', () => {
    render(<WorkflowBuilder ctx={builderCtx()} policy={policyWith('fin_mgr')} />)

    const buttons = Array.from(document.querySelectorAll('button'))
    expect(buttons.length, 'the builder rendered no controls, so the counts below are vacuous').toBeGreaterThan(0)
    expect(buttons.filter((b) => /save/i.test(b.textContent ?? '')), 'the builder lost its save control, or grew a second one').toHaveLength(1)
    expect(buttons.filter((b) => /publish/i.test(b.textContent ?? '')), 'the builder lost its Publish control, or grew a second one').toHaveLength(1)
  })
})
