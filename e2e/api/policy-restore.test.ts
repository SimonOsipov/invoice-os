// task-571 (APPR-14-05, Mode A): RED specs for ensureFirmPolicyActive/mapApprovalSteps,
// authored before either exists in contract-helpers.ts. Once the default transport wraps
// './client' (which pulls topology/targets and calls resolveTarget at module scope), a
// static import of contract-helpers.ts would die under vitest -- e2e/api/client.test.ts:10-11's
// workaround applies here too: set GATEWAY_URL/APP_URL before the dynamic import.
//
// The transport is injected exactly as task-570's approveUntilClosed is (client.ts) -- every
// test below drives ensureFirmPolicyActive/mapApprovalSteps with scripted responses, no
// network. Two failure modes drove this file's shape, both measured independently by two
// architecture passes on task-571: a bare re-publish 409s (PUT draft must precede publish),
// and an empty or truncated successor tree leaves the firm tenant reading governed while
// every invoice auto-approves -- the "ungated" tests below guard exactly that.
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type {
  ApprovalPolicy,
  ApprovalPoliciesResponse,
  ApprovalPolicyDraftInput,
  ApprovalPolicyVersion,
  ApprovalStep,
  ApprovalStepInput,
} from './client'

// Local types for the two not-yet-exported symbols -- NOT `typeof import('./contract-helpers')`,
// which would fail `tsc --noEmit` until they land (the wrong kind of red for Mode A: this wants
// a RUNTIME "not a function", not a collection-time error).
type ApprovalPolicyTransport = {
  list: (token: string) => Promise<ApprovalPoliciesResponse>
  putDraft: (token: string, id: string, body: ApprovalPolicyDraftInput) => Promise<ApprovalPolicy>
  publish: (token: string, id: string) => Promise<ApprovalPolicy>
}
type EnsureFirmPolicyActive = (token: string, transport?: ApprovalPolicyTransport) => Promise<ApprovalPolicy>
type MapApprovalSteps = (steps: ApprovalStep[]) => ApprovalStepInput[]

let ensureFirmPolicyActive: EnsureFirmPolicyActive
let mapApprovalSteps: MapApprovalSteps

beforeAll(async () => {
  process.env.GATEWAY_URL = 'https://gateway.test'
  process.env.APP_URL = 'https://app.test'
  const mod = (await import('./contract-helpers')) as unknown as {
    ensureFirmPolicyActive: EnsureFirmPolicyActive
    mapApprovalSteps: MapApprovalSteps
  }
  ;({ ensureFirmPolicyActive, mapApprovalSteps } = mod)
})

// captureRejection awaits a promise and returns its rejection -- so a test can assert the
// EXACT reason rather than merely that something threw. Duplicated from client.test.ts by
// this repo's own convention (no cross-suite imports between spec/test files).
async function captureRejection(p: Promise<unknown>): Promise<Error> {
  try {
    await p
  } catch (e) {
    return e as Error
  }
  throw new Error('expected the promise to reject, but it resolved')
}

const POLICY_NAME = 'Standard approval policy'

// approvalStep()/conditionStep()/version()/policy(): explicit-param builders, no Partial<T>
// spread onto a fully keyed literal (that widens every base field to T|undefined) --
// client.test.ts's step()/run() precedent.
function approvalStep(id: string, role: string): ApprovalStep {
  return {
    id,
    kind: 'approval',
    workflow_role_key: role,
    sla_hours: null,
    cond_op: null,
    cond_amount: null,
    notify_target: null,
    notify_channel: null,
    then: [],
    else: [],
  }
}

function conditionStep(id: string, then: ApprovalStep[], elseBranch: ApprovalStep[] = []): ApprovalStep {
  return {
    id,
    kind: 'condition',
    workflow_role_key: null,
    sla_hours: null,
    cond_op: '>',
    cond_amount: '250000000.00',
    notify_target: null,
    notify_channel: null,
    then,
    else: elseBranch,
  }
}

// The seeded firm tree's SHAPE (internal/demopolicy's firmPlan): fin_mgr first, a condition
// naming a nested approver in between, compliance last. Never the full polF1 literal a third
// time (Correction 2) -- the helper reads its source tree from the server, this is only
// enough shape to exercise the boundary check and the mapper's recursion.
const GOOD_TREE: ApprovalStep[] = [
  approvalStep('srv-1', 'fin_mgr'),
  conditionStep('srv-2', [approvalStep('srv-3', 'fin_dir')]),
  approvalStep('srv-4', 'compliance'),
]

function version(v: number, isActive: boolean, sealed = true): ApprovalPolicyVersion {
  return { version: v, sealed, is_active: isActive, published_at: '2026-01-01T00:00:00Z', published_by: 'system' }
}

function policy(opts: {
  id: string
  name: string
  version: number
  steps: ApprovalStep[]
  versions: ApprovalPolicyVersion[]
}): ApprovalPolicy {
  return {
    id: opts.id,
    name: opts.name,
    scope: 'All invoices',
    status: 'published',
    version: opts.version,
    sealed: true,
    steps: opts.steps,
    versions: opts.versions,
  }
}

describe('mapApprovalSteps', () => {
  // Correction 4: ApprovalStep (response) carries `id`; ApprovalStepInput (request) has
  // none. Assert the mapped shape directly, not merely via a round trip through the transport.
  it('drops id and preserves every other field, including explicit nulls', () => {
    const mapped = mapApprovalSteps([approvalStep('srv-1', 'fin_mgr')])
    expect(mapped).toEqual([
      {
        kind: 'approval',
        workflow_role_key: 'fin_mgr',
        sla_hours: null,
        cond_op: null,
        cond_amount: null,
        notify_target: null,
        notify_channel: null,
        then: [],
        else: [],
      },
    ])
  })

  it('keeps a real value distinct from a sibling explicit null on the same step', () => {
    const mapped = mapApprovalSteps([conditionStep('srv-1', [])])
    expect(mapped[0].cond_op).toBe('>')
    expect(mapped[0].cond_amount).toBe('250000000.00')
    expect(mapped[0].workflow_role_key).toBeNull()
  })

  // The truncation failure mode this test guards: a mapper that maps only the root lane and
  // forgets to recurse would silently answer [] for `then` here -- the exact defect that lands
  // a restore in failure mode 2 (an active policy that reads governed but blocks nothing).
  it('recurses into then and else, so a nested step is not truncated', () => {
    const mapped = mapApprovalSteps(GOOD_TREE)
    expect(mapped).toHaveLength(3)
    expect(mapped[1].then).toEqual([
      {
        kind: 'approval',
        workflow_role_key: 'fin_dir',
        sla_hours: null,
        cond_op: null,
        cond_amount: null,
        notify_target: null,
        notify_channel: null,
        then: [],
        else: [],
      },
    ])
    expect(mapped[1].else).toEqual([])
  })

  // QA gap-fill: the test above only exercises `then` with a nested step -- `else` is [] on
  // every fixture including GOOD_TREE, so a mapper that recurses into `then` but forgets
  // `else` (`else: []` instead of `else: mapApprovalSteps(s.else)`) passed the whole suite
  // uncaught. Same truncation failure mode, the other lane.
  it('recurses into else, not just then, so an else-branch step is not truncated', () => {
    const mapped = mapApprovalSteps([conditionStep('cnd-1', [], [approvalStep('srv-9', 'compliance')])])
    expect(mapped[0].else).toEqual([
      {
        kind: 'approval',
        workflow_role_key: 'compliance',
        sla_hours: null,
        cond_op: null,
        cond_amount: null,
        notify_target: null,
        notify_channel: null,
        then: [],
        else: [],
      },
    ])
  })
})

describe('ensureFirmPolicyActive', () => {
  const SEEDED_ID = 'pol-seeded'

  it('reads the seeded policy by name and republishes its OWN tree, never a hard-coded one', async () => {
    const source = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 3, steps: GOOD_TREE, versions: [version(3, false)] })
    const restored = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 4,
      steps: GOOD_TREE,
      versions: [version(4, true), version(3, false)],
    })

    const list = vi
      .fn()
      .mockResolvedValueOnce({ approval_policies: [source] } satisfies ApprovalPoliciesResponse)
      .mockResolvedValueOnce({ approval_policies: [restored] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn().mockResolvedValue(source)
    const publish = vi.fn().mockResolvedValue(restored)

    const result = await ensureFirmPolicyActive('tok', { list, putDraft, publish })

    expect(result.id).toBe(SEEDED_ID)
    expect(putDraft).toHaveBeenCalledWith('tok', SEEDED_ID, { steps: mapApprovalSteps(GOOD_TREE) })
    expect(publish).toHaveBeenCalledWith('tok', SEEDED_ID)
    expect(list).toHaveBeenCalledTimes(2)
  })

  // AC-2. Correction 1's two-call sequence is what makes this true: publish deactivates
  // tenant-wide BEFORE claiming the slot, so whichever policy a failed delete left active
  // is displaced by the seeded policy's own publish, not specifically targeted.
  it('AC-2: converges over a competing active policy left behind by a failed delete', async () => {
    const source = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const competing = policy({ id: 'pol-competing', name: 'Probe Policy 1234-9', version: 1, steps: [], versions: [version(1, true)] })
    const restoredSeeded = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 2,
      steps: GOOD_TREE,
      versions: [version(2, true), version(1, false)],
    })
    const competingNowInactive = policy({
      id: 'pol-competing',
      name: 'Probe Policy 1234-9',
      version: 1,
      steps: [],
      versions: [version(1, false)],
    })

    const list = vi
      .fn()
      .mockResolvedValueOnce({ approval_policies: [competing, source] } satisfies ApprovalPoliciesResponse)
      .mockResolvedValueOnce({ approval_policies: [competingNowInactive, restoredSeeded] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn().mockResolvedValue(source)
    const publish = vi.fn().mockResolvedValue(restoredSeeded)

    const result = await ensureFirmPolicyActive('tok', { list, putDraft, publish })

    expect(result.id).toBe(SEEDED_ID)
    expect(putDraft).toHaveBeenCalledWith('tok', SEEDED_ID, expect.anything())
    expect(publish).toHaveBeenCalledWith('tok', SEEDED_ID)
    // The competing policy is never touched -- displaced by publish's tenant-wide
    // deactivation, not by the helper deliberately targeting it.
    expect(putDraft).not.toHaveBeenCalledWith('tok', 'pol-competing', expect.anything())
    expect(publish).not.toHaveBeenCalledWith('tok', 'pol-competing')
  })

  // The restore "must not depend on the swallowed deletes having succeeded" (task-571's
  // Implementation Plan) generalizes to its own mutation: a putDraft/publish failure must not
  // fail the file when the independent read proves the tenant is already converged anyway.
  it('tolerates the mutation failing when the independent read shows the tenant already converged', async () => {
    const already = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 2, steps: GOOD_TREE, versions: [version(2, true)] })
    const list = vi.fn().mockResolvedValue({ approval_policies: [already] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn().mockRejectedValue(new Error('409 this policy has no unpublished changes'))
    const publish = vi.fn().mockRejectedValue(new Error('409 this policy has no unpublished changes'))

    const result = await ensureFirmPolicyActive('tok', { list, putDraft, publish })

    expect(result.id).toBe(SEEDED_ID)
  })

  // AC-3. publish's own response CLAIMS the restore succeeded; the independent read that
  // follows says otherwise. A helper that trusted the mutation's response would resolve here.
  it("AC-3: proves success by an independent read, never by the mutation's own response", async () => {
    const source = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const publishResponseLie = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 2,
      steps: GOOD_TREE,
      versions: [version(2, true)],
    })
    const actualAfter = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 2,
      steps: GOOD_TREE,
      versions: [version(2, false), version(1, false)],
    })

    const list = vi
      .fn()
      .mockResolvedValueOnce({ approval_policies: [source] } satisfies ApprovalPoliciesResponse)
      .mockResolvedValueOnce({ approval_policies: [actualAfter] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn().mockResolvedValue(source)
    const publish = vi.fn().mockResolvedValue(publishResponseLie)

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toMatch(/did not converge/i)
    expect(list).toHaveBeenCalledTimes(2)
  })

  // AC-4, the zero-active case.
  it('AC-4: fails loudly and names what it found when nothing ends up active', async () => {
    const source = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const afterNoneActive = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 2,
      steps: GOOD_TREE,
      versions: [version(2, false), version(1, false)],
    })

    const list = vi
      .fn()
      .mockResolvedValueOnce({ approval_policies: [source] } satisfies ApprovalPoliciesResponse)
      .mockResolvedValueOnce({ approval_policies: [afterNoneActive] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn().mockResolvedValue(source)
    const publish = vi.fn().mockResolvedValue(afterNoneActive)

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toMatch(/did not converge/i)
    expect(err.message).toContain('0')
  })

  // AC-4, the more-than-one-active case: names EVERY active version found, not just a count.
  it('AC-4: names every active version when more than one is found', async () => {
    const source = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const stillActiveSeeded = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 2,
      steps: GOOD_TREE,
      versions: [version(2, true), version(1, false)],
    })
    const rogueActive = policy({ id: 'pol-rogue', name: 'Rogue Policy', version: 1, steps: [], versions: [version(1, true)] })

    const list = vi
      .fn()
      .mockResolvedValueOnce({ approval_policies: [source] } satisfies ApprovalPoliciesResponse)
      .mockResolvedValueOnce({ approval_policies: [stillActiveSeeded, rogueActive] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn().mockResolvedValue(source)
    const publish = vi.fn().mockResolvedValue(stillActiveSeeded)

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toContain(POLICY_NAME)
    expect(err.message).toContain('Rogue Policy')
  })

  // AC-8, source side: an empty seeded tree must refuse BEFORE any write -- publishing it
  // would leave the firm tenant reading governed while every invoice auto-approves.
  it('AC-8: refuses an empty-tree source before writing anything, naming the tenant as ungated', async () => {
    const emptySource = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 1, steps: [], versions: [version(1, false)] })
    const list = vi.fn().mockResolvedValue({ approval_policies: [emptySource] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn()
    const publish = vi.fn()

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toMatch(/ungated/i)
    expect(putDraft).not.toHaveBeenCalled()
    expect(publish).not.toHaveBeenCalled()
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('AC-8: refuses a tree truncated below the boundary (missing leading fin_mgr)', async () => {
    const truncated = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 1,
      steps: [approvalStep('srv-1', 'compliance')],
      versions: [version(1, false)],
    })
    const list = vi.fn().mockResolvedValue({ approval_policies: [truncated] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn()
    const publish = vi.fn()

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toMatch(/ungated/i)
    expect(putDraft).not.toHaveBeenCalled()
  })

  it('AC-8: refuses a tree truncated below the boundary (missing trailing compliance)', async () => {
    const truncated = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 1,
      steps: [approvalStep('srv-1', 'fin_mgr')],
      versions: [version(1, false)],
    })
    const list = vi.fn().mockResolvedValue({ approval_policies: [truncated] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn()
    const publish = vi.fn()

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toMatch(/ungated/i)
    expect(putDraft).not.toHaveBeenCalled()
  })

  // AC-8, restored side: the source was fine, but the version that actually landed active is
  // empty (e.g. a race, or a bug in the mapper this file's other describe block guards).
  // Trusting AC-1's name-only check here is exactly the failure mode task-571 exists to close.
  it('AC-8: refuses when the RESTORED active tree itself is empty, not just the source', async () => {
    const source = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const afterEmpty = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 2,
      steps: [],
      versions: [version(2, true), version(1, false)],
    })

    const list = vi
      .fn()
      .mockResolvedValueOnce({ approval_policies: [source] } satisfies ApprovalPoliciesResponse)
      .mockResolvedValueOnce({ approval_policies: [afterEmpty] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn().mockResolvedValue(source)
    const publish = vi.fn().mockResolvedValue(afterEmpty)

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toMatch(/ungated/i)
  })

  // Name-uniqueness guard (task-571's Implementation Notes): approval_policies has no unique
  // index on name, so more than one live "Standard approval policy" must fail loudly rather
  // than restore an arbitrary one of them.
  it('throws naming the count when more than one live policy is named "Standard approval policy"', async () => {
    const a = policy({ id: 'pol-a', name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const b = policy({ id: 'pol-b', name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const list = vi.fn().mockResolvedValue({ approval_policies: [a, b] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn()
    const publish = vi.fn()

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toContain('2')
    expect(putDraft).not.toHaveBeenCalled()
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('throws naming the policy when no live policy is named "Standard approval policy"', async () => {
    const list = vi.fn().mockResolvedValue({ approval_policies: [] } satisfies ApprovalPoliciesResponse)
    const putDraft = vi.fn()
    const publish = vi.fn()

    const err = await captureRejection(ensureFirmPolicyActive('tok', { list, putDraft, publish }))

    expect(err.message).toContain(POLICY_NAME)
    expect(putDraft).not.toHaveBeenCalled()
  })

  // AC-9: two calls converge on the same identity and name even though each call forks a NEW
  // version (Correction 1 -- a bare re-publish 409s, so PUT-draft always mints max+1). Neither
  // call is a literal no-op at the row level; both must still leave the tenant governed.
  it('AC-9: idempotent and convergent -- calling twice reaches the same end state', async () => {
    const v1 = policy({ id: SEEDED_ID, name: POLICY_NAME, version: 1, steps: GOOD_TREE, versions: [version(1, false)] })
    const v2Active = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 2,
      steps: GOOD_TREE,
      versions: [version(2, true), version(1, false)],
    })
    const v3Active = policy({
      id: SEEDED_ID,
      name: POLICY_NAME,
      version: 3,
      steps: GOOD_TREE,
      versions: [version(3, true), version(2, false), version(1, false)],
    })

    const list = vi
      .fn()
      .mockResolvedValueOnce({ approval_policies: [v1] } satisfies ApprovalPoliciesResponse) // call 1: source read
      .mockResolvedValueOnce({ approval_policies: [v2Active] } satisfies ApprovalPoliciesResponse) // call 1: verify
      .mockResolvedValueOnce({ approval_policies: [v2Active] } satisfies ApprovalPoliciesResponse) // call 2: source read
      .mockResolvedValueOnce({ approval_policies: [v3Active] } satisfies ApprovalPoliciesResponse) // call 2: verify
    const putDraft = vi.fn().mockResolvedValueOnce(v1).mockResolvedValueOnce(v2Active)
    const publish = vi.fn().mockResolvedValueOnce(v2Active).mockResolvedValueOnce(v3Active)

    const first = await ensureFirmPolicyActive('tok', { list, putDraft, publish })
    const second = await ensureFirmPolicyActive('tok', { list, putDraft, publish })

    expect(first.name).toBe(POLICY_NAME)
    expect(second.name).toBe(POLICY_NAME)
    expect(first.id).toBe(second.id)
    expect(list).toHaveBeenCalledTimes(4)
  })
})
