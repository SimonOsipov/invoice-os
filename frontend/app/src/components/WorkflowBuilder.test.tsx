// @vitest-environment jsdom
//
// RED specs for APPR-04-06 AC5/AC6: WorkflowBuilder does not read ctx.rolesState at all
// today, so an unlanded roles fetch renders every approval step as if its role were
// genuinely deleted -- roleOf's fallback (lib/roles.ts:63) can't tell "not fetched yet"
// from "fetched and gone". The guard belongs HERE, not in WorkflowsView (which forwards
// ctx whole and reads no role data) -- see the story's [D-BUILDER-GUARD].
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
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
  function saveButton(): HTMLElement {
    // Matched on the handler's own control, not on copy: the label is 'Save & publish'
    // today and subtask 05 splits it into 'Save draft' / 'Publish'. Both spellings start
    // with 'Save', so this survives that rename while still naming one button.
    const buttons = Array.from(document.querySelectorAll('button')).filter((b) => /^Save/.test(b.textContent ?? ''))
    expect(buttons, 'no Save control rendered at all').toHaveLength(1)
    return buttons[0]
  }

  it('clicking Save calls savePolicy once and publishPolicy never', () => {
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

  it('an ordinary edit writes a draft too — no edit path reaches publishPolicy', () => {
    const savePolicy = vi.fn(async (p: Policy) => p)
    const publishPolicy = vi.fn(async () => policyWith('fin_mgr'))

    // PUBLISHED on purpose: the demotion `touch` used to apply is gone, so this is also
    // where an edit path that re-introduced it would show up.
    const live: Policy = { ...policyWith('fin_mgr'), status: 'published', activeVersion: 1 }
    render(<WorkflowBuilder ctx={builderCtx({ savePolicy, publishPolicy })} policy={live} />)
    fireEvent.change(screen.getByLabelText('Policy name'), { target: { value: 'Renamed policy' } })

    expect(savePolicy).toHaveBeenCalledTimes(1)
    expect(savePolicy.mock.calls[0][0].name).toBe('Renamed policy')
    // The reducers no longer demote, so an edit must leave a published policy published —
    // the server decides what a save does to a version.
    expect(savePolicy.mock.calls[0][0].status).toBe('published')
    expect(publishPolicy).not.toHaveBeenCalled()
  })
})
