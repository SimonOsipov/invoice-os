import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import {
  createApprovalPolicy,
  deleteApprovalPolicy,
  listApprovalPolicies,
  nodesFromSteps,
  policyInForce,
  policyStanding,
  publishApprovalPolicy,
  putApprovalPolicyDraft,
  stepInputsFromNodes,
  toPolicy,
  type PolicyStepWire,
  type PolicyVersionWire,
  type PolicyWire,
} from './policies'
import type { AuthedFetch } from './portfolio'
import { seedPolicies } from './policies.fixture'
import { newPolicy, removePolicy, replacePolicy, type ApprovalNode, type ConditionNode, type NotifyNode, type Policy } from './workflows'

// --- fixtures ---------------------------------------------------------------
// The wire helpers spell every one of the ten Step keys, because the server never omits
// one — a fixture that leaves a key off would test a response shape that cannot arrive.

const step = (over: Partial<PolicyStepWire> = {}): PolicyStepWire => ({
  id: 's1',
  kind: 'approval',
  workflow_role_key: null,
  sla_hours: null,
  cond_op: null,
  cond_amount: null,
  notify_target: null,
  notify_channel: null,
  then: [],
  else: [],
  ...over,
})

const ver = (version: number, sealed: boolean, isActive: boolean): PolicyVersionWire => ({
  version,
  sealed,
  is_active: isActive,
  published_at: null,
  published_by: null,
})

const wire = (over: Partial<PolicyWire> = {}): PolicyWire => ({
  id: 'p1',
  name: 'Standard approval policy',
  scope: 'All invoices',
  status: 'draft',
  version: 1,
  sealed: false,
  steps: [],
  versions: [],
  ...over,
})

// Built through a factory, never as a fresh literal at the call site, so these specs hold
// whether the standing functions take a whole Policy or a narrower slice of one.
const policy = (over: Partial<Policy> = {}): Policy => ({
  id: 'p1',
  name: 'Standard approval policy',
  scope: 'All invoices',
  status: 'draft',
  updated: 'now',
  version: 1,
  activeVersion: null,
  nodes: [],
  ...over,
})

describe('nodesFromSteps / stepInputsFromNodes (AC-2)', () => {
  it("maps an approval step's role key and SLA hours in both directions", () => {
    const nodes = nodesFromSteps([step({ kind: 'approval', workflow_role_key: 'cfo', sla_hours: 72 })])
    expect(nodes).toHaveLength(1)
    const node = nodes[0] as ApprovalNode
    expect(node.type).toBe('approval')
    expect(node.role).toBe('cfo')
    expect(node.sla).toBe('72')
    expect(node.delegate).toBe(false)

    const back = stepInputsFromNodes([node])
    expect(back).toHaveLength(1)
    expect(back[0].kind).toBe('approval')
    expect(back[0].workflow_role_key).toBe('cfo')
    expect(back[0].sla_hours).toBe(72)
  })

  it("treats sla_hours null as the '0' no-deadline sentinel, never as zero hours", () => {
    const nodes = nodesFromSteps([step({ kind: 'approval', workflow_role_key: 'cfo', sla_hours: null })])
    expect(nodes).toHaveLength(1)
    expect((nodes[0] as ApprovalNode).sla).toBe('0')

    const back = stepInputsFromNodes([{ id: 'n1', type: 'approval', role: 'cfo', sla: '0', delegate: false }])
    expect(back).toHaveLength(1)
    expect(back[0].sla_hours).toBeNull()
    expect(back[0].sla_hours).not.toBe(0)
    // The other direction of the same sentinel: a real deadline stays a number.
    expect(stepInputsFromNodes([{ id: 'n2', type: 'approval', role: 'cfo', sla: '48', delegate: false }])[0].sla_hours).toBe(48)
  })

  it('reads cond_amount decimal text back as a number and sends it back as text', () => {
    const nodes = nodesFromSteps([step({ kind: 'condition', cond_op: '>', cond_amount: '250000000.00' })])
    expect(nodes).toHaveLength(1)
    const cond = nodes[0] as ConditionNode
    expect(cond.value).toBe(250_000_000)

    const back = stepInputsFromNodes([cond])
    expect(back).toHaveLength(1)
    expect(back[0].cond_amount).toBe('250000000')
    expect(back[0].cond_op).toBe('>')
  })

  it("always reports a condition's field as amount, the only domain the column holds", () => {
    const nodes = nodesFromSteps([step({ kind: 'condition', cond_op: '>=', cond_amount: '1000.00' })])
    expect(nodes).toHaveLength(1)
    expect((nodes[0] as ConditionNode).field).toBe('amount')
  })

  it("round-trips a notify step's target and channel", () => {
    const nodes = nodesFromSteps([step({ kind: 'notify', notify_target: 'Tax Team', notify_channel: 'In-app' })])
    expect(nodes).toHaveLength(1)
    const node = nodes[0] as NotifyNode
    expect(node.type).toBe('notify')
    expect(node.target).toBe('Tax Team')
    expect(node.channel).toBe('In-app')

    const back = stepInputsFromNodes([node])
    expect(back).toHaveLength(1)
    expect(back[0].kind).toBe('notify')
    expect(back[0].notify_target).toBe('Tax Team')
    expect(back[0].notify_channel).toBe('In-app')
  })

  it('nests then and else lanes to two levels and no further', () => {
    const nodes = nodesFromSteps([
      step({
        id: 'c1',
        kind: 'condition',
        cond_op: '>',
        cond_amount: '500000000.00',
        then: [
          step({ id: 't1', kind: 'approval', workflow_role_key: 'fin_dir', sla_hours: 48 }),
          step({ id: 't2', kind: 'notify', notify_target: 'Audit Committee', notify_channel: 'Email' }),
        ],
        else: [step({ id: 'e1', kind: 'autoapprove' })],
      }),
    ])
    expect(nodes).toHaveLength(1)
    const cond = nodes[0] as ConditionNode
    expect(cond.then.map((n) => n.type)).toEqual(['approval', 'notify'])
    expect(cond.else.map((n) => n.type)).toEqual(['autoapprove'])

    // A branch node holds no lanes of its own — that is what makes a third level unrepresentable.
    const branches = [...cond.then, ...cond.else]
    expect(branches).toHaveLength(3)
    for (const n of branches) {
      expect(Object.hasOwn(n, 'then')).toBe(false)
      expect(Object.hasOwn(n, 'else')).toBe(false)
    }
  })
})

describe('delegation never reaches the wire (AC-3)', () => {
  it('never emits delegate or delegateTo on the wire', () => {
    const node: ApprovalNode = { id: 'n1', type: 'approval', role: 'line_mgr', sla: '48', delegate: true, delegateTo: 'Tunde Adeyemi' }
    const back = stepInputsFromNodes([node])
    expect(back).toHaveLength(1)
    expect(Object.hasOwn(back[0], 'delegate')).toBe(false)
    expect(Object.hasOwn(back[0], 'delegateTo')).toBe(false)
    // The fields that DO have a column still went out, so absence above is a filter, not a dropped step.
    expect(back[0].kind).toBe('approval')
    expect(back[0].workflow_role_key).toBe('line_mgr')
    expect(back[0].sla_hours).toBe(48)
  })

  it('leaves delegateTo ABSENT on a node built from the wire, not undefined', () => {
    const nodes = nodesFromSteps([step({ kind: 'approval', workflow_role_key: 'cfo', sla_hours: 24 })])
    expect(nodes).toHaveLength(1)
    expect(Object.hasOwn(nodes[0], 'delegateTo')).toBe(false)
    expect((nodes[0] as ApprovalNode).delegate).toBe(false)
  })
})

describe('toPolicy (AC-2)', () => {
  it('names the active version from versions, never from the top version', () => {
    const p = toPolicy(wire({ version: 4, status: 'draft', sealed: false, versions: [ver(4, false, false), ver(3, true, true)] }))
    expect(p.version).toBe(4)
    expect(p.activeVersion).toBe(3)
    expect(p.status).toBe('draft')
  })

  it("reports a null active version when another policy holds the tenant's slot", () => {
    const p = toPolicy(wire({ version: 2, status: 'draft', versions: [ver(2, false, false), ver(1, true, false)] }))
    expect(p.activeVersion).toBeNull()
    expect(p.version).toBe(2)
  })
})

describe('policyStanding / policyInForce (AC-2)', () => {
  it('policyStanding names the in-force version and the draft separately when both exist', () => {
    expect(policyStanding(policy({ version: 4, activeVersion: 3, status: 'draft' }))).toBe('v3 in force · v4 draft')
  })

  it('policyStanding says Never published only for an untouched version 1', () => {
    expect(policyStanding(policy({ version: 1, activeVersion: null, status: 'draft' }))).toBe('Never published')
    expect(policyStanding(policy({ version: 2, activeVersion: null, status: 'draft' }))).toBe('Not in force')
  })

  it('policyInForce finds the other policy holding the slot, and no policy finds itself', () => {
    const a = policy({ id: 'polA', version: 2, activeVersion: null, status: 'draft' })
    const b = policy({ id: 'polB', version: 1, activeVersion: 1, status: 'published' })
    const list = [a, b]
    expect(policyInForce(list, a.id)?.id).toBe('polB')
    expect(policyInForce(list, b.id)).toBeNull()
  })

  // The tenant-wide unique index allows ONE active version per store; a seed with two would
  // make policyInForce return the first of them and hide the bug it exists to catch.
  it('the seed store stays a tenant the server could actually produce', () => {
    const store = seedPolicies()
    for (const mode of ['firm', 'inhouse'] as const) {
      expect(store[mode].length).toBeGreaterThan(0)
      expect(store[mode].filter((p) => p.activeVersion !== null)).toHaveLength(1)
      for (const p of store[mode]) expect(p.version).toBe(1)
    }
    // Wrapped, not passed bare to map — map would feed the index in as a second argument.
    expect(store.firm.map((p) => policyStanding(p))).toEqual(['v1 in force', 'Not in force', 'Never published'])
    expect(store.inhouse.map((p) => policyStanding(p))).toEqual(['v1 in force', 'Never published'])
  })
})

// --- QA additions -----------------------------------------------------------
// Everything below covers a line the AC specs above leave un-pinned: each one was written
// against a mutation that survived the original fourteen.

describe('QA: toPolicy projects every wire field, not only the version pair', () => {
  it('carries id, name, scope and the step tree straight through', () => {
    const p = toPolicy(
      wire({
        id: 'pol-7',
        name: 'Capex sign-off',
        scope: 'Capex & fixed assets',
        steps: [step({ id: 's9', kind: 'approval', workflow_role_key: 'cfo', sla_hours: 24 })],
      }),
    )
    expect(p.id).toBe('pol-7')
    expect(p.name).toBe('Capex sign-off')
    // Carried verbatim: WF_SCOPE_OPTIONS is the EDITOR's list, not a filter on what may arrive.
    expect(p.scope).toBe('Capex & fixed assets')
    expect(p.nodes).toHaveLength(1)
    expect((p.nodes[0] as ApprovalNode).role).toBe('cfo')
  })

  it('carries a status outside the SPA union rather than defaulting it (AC-1)', () => {
    // The wire types `status` as string precisely so a value the server grows is visible
    // here instead of being silently rewritten to 'draft'.
    expect(toPolicy(wire({ status: 'archived' })).status).toBe('archived')
    expect(toPolicy(wire({ status: 'published' })).status).toBe('published')
  })

  it('reports no active version for a policy carrying no versions at all', () => {
    const p = toPolicy(wire({ version: 1, versions: [] }))
    expect(p.activeVersion).toBeNull()
    expect(p.version).toBe(1)
  })

  it('maps a stepless policy to an empty node list, both directions', () => {
    expect(toPolicy(wire({ steps: [] })).nodes).toEqual([])
    expect(nodesFromSteps([])).toEqual([])
    expect(stepInputsFromNodes([])).toEqual([])
  })
})

describe('QA: node identity is the SERVER step id', () => {
  it('carries the wire id onto every node, at the root and inside both lanes', () => {
    const nodes = nodesFromSteps([
      step({ id: 'srv-a', kind: 'approval', workflow_role_key: 'cfo', sla_hours: 24 }),
      step({ id: 'srv-n', kind: 'notify', notify_target: 'Tax Team', notify_channel: 'Email' }),
      step({
        id: 'srv-c',
        kind: 'condition',
        cond_op: '>',
        cond_amount: '1000.00',
        then: [step({ id: 'srv-t', kind: 'approval', workflow_role_key: 'ceo', sla_hours: 72 })],
        else: [step({ id: 'srv-e', kind: 'autoapprove' })],
      }),
    ])
    expect(nodes).toHaveLength(3)
    expect(nodes.map((n) => n.id)).toEqual(['srv-a', 'srv-n', 'srv-c'])
    const cond = nodes[2] as ConditionNode
    expect(cond.then.map((n) => n.id)).toEqual(['srv-t'])
    expect(cond.else.map((n) => n.id)).toEqual(['srv-e'])
  })
})

describe('QA: stepInputsFromNodes emits exactly what stepInput declares', () => {
  it('emits no id — the server drops a client-supplied one at decode', () => {
    const back = stepInputsFromNodes([
      { id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false },
      { id: 'n2', type: 'notify', target: 'Tax Team', channel: 'Email' },
      { id: 'n3', type: 'autoapprove' },
      { id: 'n4', type: 'condition', field: 'amount', op: '>', value: 1000, then: [{ id: 'n5', type: 'autoapprove' }], else: [] },
    ])
    expect(back).toHaveLength(4)
    for (const s of back) expect(Object.hasOwn(s, 'id')).toBe(false)
    expect(back[3].then).toHaveLength(1)
    expect(Object.hasOwn(back[3].then![0], 'id')).toBe(false)
  })

  it('emits the four kinds the server vocabulary accepts, spelled exactly', () => {
    const back = stepInputsFromNodes([
      { id: 'n1', type: 'approval', role: 'cfo', sla: '24', delegate: false },
      { id: 'n2', type: 'condition', field: 'amount', op: '>', value: 1000, then: [], else: [] },
      { id: 'n3', type: 'notify', target: 'Tax Team', channel: 'Email' },
      { id: 'n4', type: 'autoapprove' },
    ])
    expect(back.map((s) => s.kind)).toEqual(['approval', 'condition', 'notify', 'autoapprove'])
  })

  it('sends a condition with both lanes empty as two empty arrays, never absent', () => {
    const back = stepInputsFromNodes([{ id: 'c1', type: 'condition', field: 'amount', op: '>=', value: 500, then: [], else: [] }])
    expect(back).toHaveLength(1)
    expect(back[0].then).toEqual([])
    expect(back[0].else).toEqual([])
  })
})

describe('QA: the wire values the four SLA options cannot express', () => {
  // The server bounds sla_hours at >= 0 and the column has no CHECK
  // (migrations/20260809210326_approval_policies.sql), so a stored 0 is producible — and
  // the SPA has no way to say "0 hours" that is not the no-deadline sentinel. Known lossy
  // edge, pinned rather than fixed: a 0 read back saves as null.
  it('collapses a stored 0-hour SLA onto the no-deadline sentinel', () => {
    const nodes = nodesFromSteps([step({ kind: 'approval', workflow_role_key: 'cfo', sla_hours: 0 })])
    expect(nodes).toHaveLength(1)
    expect((nodes[0] as ApprovalNode).sla).toBe('0')
    expect(stepInputsFromNodes(nodes)[0].sla_hours).toBeNull()
  })

  it('renders an SLA outside the four options rather than dropping the step', () => {
    const nodes = nodesFromSteps([step({ kind: 'approval', workflow_role_key: 'cfo', sla_hours: 36 })])
    expect(nodes).toHaveLength(1)
    expect((nodes[0] as ApprovalNode).sla).toBe('36')
    expect(stepInputsFromNodes(nodes)[0].sla_hours).toBe(36)
  })

  it('reads an approval step with no role key as the empty-string default', () => {
    const nodes = nodesFromSteps([step({ kind: 'approval', workflow_role_key: null, sla_hours: 24 })])
    expect(nodes).toHaveLength(1)
    expect((nodes[0] as ApprovalNode).role).toBe('')
  })

  it('reads a notify step with no target or channel as empty strings, not undefined', () => {
    const nodes = nodesFromSteps([step({ kind: 'notify', notify_target: null, notify_channel: null })])
    expect(nodes).toHaveLength(1)
    const n = nodes[0] as NotifyNode
    expect(n.target).toBe('')
    expect(n.channel).toBe('')
  })
})

describe('QA: cond_amount across the column the server allows', () => {
  it('keeps a scaled threshold through the round trip', () => {
    const nodes = nodesFromSteps([step({ kind: 'condition', cond_op: '>', cond_amount: '250.55' })])
    expect(nodes).toHaveLength(1)
    expect((nodes[0] as ConditionNode).value).toBe(250.55)
    expect(stepInputsFromNodes(nodes)[0].cond_amount).toBe('250.55')
  })

  it('survives the largest value numeric(14,2) holds', () => {
    // condAmountCeiling (internal/approval/policy.go) is 1e12, exclusive, at scale 2.
    const nodes = nodesFromSteps([step({ kind: 'condition', cond_op: '<', cond_amount: '999999999999.99' })])
    expect(nodes).toHaveLength(1)
    expect((nodes[0] as ConditionNode).value).toBe(999_999_999_999.99)
    expect(stepInputsFromNodes(nodes)[0].cond_amount).toBe('999999999999.99')
  })
})

describe('QA: policyStanding and policyInForce, without going through the seed', () => {
  // M12/M14: before these two, dropping either term of the activeVersion === null branch
  // and mislabelling the equal-version branch were caught ONLY by the seed-store test.
  it('names the in-force version when the top version is the live one', () => {
    expect(policyStanding(policy({ version: 1, activeVersion: 1, status: 'published' }))).toBe('v1 in force')
    expect(policyStanding(policy({ version: 3, activeVersion: 3, status: 'published' }))).toBe('v3 in force')
  })

  it('says Not in force for a version-1 policy that was published and lost the slot', () => {
    expect(policyStanding(policy({ version: 1, activeVersion: null, status: 'published' }))).toBe('Not in force')
  })

  it('calls a brand-new policy Never published', () => {
    expect(policyStanding(newPolicy())).toBe('Never published')
    expect(newPolicy().activeVersion).toBeNull()
  })

  it('policyInForce reports null when the list holds no active policy, and when it is empty', () => {
    const none = [policy({ id: 'a' }), policy({ id: 'b' })]
    expect(none).toHaveLength(2)
    expect(policyInForce(none, 'a')).toBeNull()
    expect(policyInForce([], 'a')).toBeNull()
  })
})

// ============================================================================
// APPR-09-02 (task-506) — the five wrappers over the APPR-01 approval-policy routes
// ============================================================================
// Two doubles, matching two jobs (roles.test.ts:1108-1136):
//  - URL / method / exact-wire-body specs drive the REAL createAuthedFetch with only
//    `fetch` stubbed, so a stubbed 4xx yields a genuine ApiError instead of a
//    re-implementation of apiFetch's contract.
//  - The "propagates unchanged" spec injects a hand-rolled AuthedFetch double: apiFetch
//    mints a FRESH ApiError per call, so instance identity is unprovable any other way.

interface PolicyWireResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function mockFetchOnce(response: PolicyWireResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const wireBase = 'https://gw'

const authed = () => createAuthedFetch(() => 'tok', vi.fn())

describe('AC-1 — each wrapper hits its own gateway path with its own method', () => {
  it('each wrapper hits its own path with its own method', async () => {
    const af = authed()
    const cases: Array<{ body: unknown; run: () => Promise<unknown> }> = [
      { body: { approval_policies: [] }, run: () => listApprovalPolicies(af, wireBase) },
      { body: wire(), run: () => createApprovalPolicy(af, wireBase, 'Untitled policy') },
      { body: wire(), run: () => putApprovalPolicyDraft(af, wireBase, 'p1', policy({ id: 'p1' })) },
      { body: wire(), run: () => publishApprovalPolicy(af, wireBase, 'p1') },
      { body: wire(), run: () => deleteApprovalPolicy(af, wireBase, 'p1') },
    ]

    const seen: Array<[string, string | undefined]> = []
    for (const c of cases) {
      const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(c.body) })
      await c.run()
      expect(fetchMock).toHaveBeenCalledTimes(1)
      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
      seen.push([url, init.method])
    }

    expect(seen).toHaveLength(5)
    // The `/api/invoice` prefix is the gateway's, not the service's — a wrapper that drops
    // it reaches nothing, and no body assertion elsewhere would notice.
    expect(seen).toEqual([
      ['https://gw/api/invoice/v1/approval-policies', 'GET'],
      ['https://gw/api/invoice/v1/approval-policies', 'POST'],
      ['https://gw/api/invoice/v1/approval-policies/p1/draft', 'PUT'],
      ['https://gw/api/invoice/v1/approval-policies/p1/publish', 'POST'],
      ['https://gw/api/invoice/v1/approval-policies/p1', 'DELETE'],
    ])
  })

  it('create POSTs the name the caller gave, and nothing else', async () => {
    const created = wire({ id: 'pol-9', name: 'Untitled policy', version: 1, steps: [], versions: [ver(1, false, false)] })
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(created) })

    const result = await createApprovalPolicy(authed(), wireBase, 'Untitled policy')

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/approval-policies')
    expect(init.method).toBe('POST')
    // `scope: ""` and an absent key are indistinguishable server-side, so the wrapper sends
    // only what its signature carries.
    expect(init.body).toBe(JSON.stringify({ name: 'Untitled policy' }))
    // Mapped, not handed back raw: `versions` is gone and `nodes` exists.
    expect(result).toEqual(toPolicy(created))
    expect(result.id).toBe('pol-9')
    expect(result.activeVersion).toBeNull()
  })

  it('list unwraps the approval_policies envelope into mapped policies', async () => {
    const listed = wire({
      id: 'pol-1',
      name: 'Standard approval policy',
      version: 2,
      steps: [step({ id: 's1', kind: 'approval', workflow_role_key: 'cfo', sla_hours: 48 })],
      versions: [ver(2, false, false), ver(1, true, true)],
    })
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ approval_policies: [listed] }) })

    const result = await listApprovalPolicies(authed(), wireBase)

    expect(result).toHaveLength(1)
    expect(result[0].id).toBe('pol-1')
    expect(result[0].version).toBe(2)
    expect(result[0].activeVersion).toBe(1)
    expect(result[0].nodes).toHaveLength(1)
    expect((result[0].nodes[0] as ApprovalNode).role).toBe('cfo')
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.body).toBeUndefined()
  })
})

describe('AC-2 — putApprovalPolicyDraft always sends steps', () => {
  const nodes = [
    { id: 'n1', type: 'approval', role: 'cfo', sla: '48', delegate: false },
    { id: 'n2', type: 'condition', field: 'amount', op: '>', value: 1000, then: [{ id: 'n3', type: 'autoapprove' }], else: [] },
  ] as Policy['nodes']

  it('a name-only draft save still sends the whole step tree', async () => {
    const next = policy({ id: 'p1', name: 'Capex sign-off', nodes })
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire({ id: 'p1' })) })

    await putApprovalPolicyDraft(authed(), wireBase, 'p1', next)

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/approval-policies/p1/draft')
    expect(init.method).toBe('PUT')
    const sent = JSON.parse(init.body as string) as { name?: string; steps?: unknown[] }
    // The rename is the point of the save; the tree rides along because the PUT is a
    // whole-tree REPLACE — an omitted `steps` is the server's 400, not a no-op merge.
    expect(sent.name).toBe('Capex sign-off')
    expect(Array.isArray(sent.steps)).toBe(true)
    expect(sent.steps).toHaveLength(2)
    expect(sent.steps).toEqual(stepInputsFromNodes(next.nodes))
  })

  it('an emptied canvas sends steps as an explicit empty array, never an absent key', async () => {
    const next = policy({ id: 'p1', name: 'Capex sign-off', nodes: [] })
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire({ id: 'p1' })) })

    await putApprovalPolicyDraft(authed(), wireBase, 'p1', next)

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    const sent = JSON.parse(init.body as string) as Record<string, unknown>
    // JSON.stringify drops an undefined value, so the key check IS the assertion here:
    // `steps: undefined` and `steps: []` are one line apart and only this tells them apart.
    expect(Object.hasOwn(sent, 'steps')).toBe(true)
    expect(sent.steps).toEqual([])
    expect(sent.name).toBe('Capex sign-off')
  })
})

describe('AC-3 — publishApprovalPolicy sends no body', () => {
  it('publish sends no request body', async () => {
    const published = wire({ id: 'p1', status: 'published', version: 1, sealed: true, versions: [ver(1, true, true)] })
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(published) })

    const result = await publishApprovalPolicy(authed(), wireBase, 'p1')

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/approval-policies/p1/publish')
    expect(init.method).toBe('POST')
    // apiFetch always passes a `body` KEY holding undefined, so presence proves nothing.
    expect(init.body).toBeUndefined()
    // What kills a `body: {}` mutation: apiFetch sets the header only when a body exists.
    expect((init.headers as Headers).get('content-type')).toBeNull()
    // The request still went out authed — absence above is a choice, not a dead call.
    expect((init.headers as Headers).get('authorization')).toBe('Bearer tok')
    expect(result).toEqual(toPolicy(published))
    expect(result.activeVersion).toBe(1)
  })
})

describe('AC-4 — deleteApprovalPolicy discards the inert response', () => {
  it('delete resolves void and never hands back the inert response', async () => {
    // The server's DELETE answer carries only id/name/scope: a policy published at v3 still
    // answers status 'draft', version 0, steps [] and versions []. Patching a client row
    // from it corrupts that row.
    const inert = wire({ id: 'p1', status: 'draft', version: 0, sealed: false, steps: [], versions: [] })
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(inert) })

    const result = await deleteApprovalPolicy(authed(), wireBase, 'p1')

    expect(result).toBeUndefined()
    // A wrapper that resolves undefined by never fetching would otherwise pass the line above.
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/approval-policies/p1')
    expect(init.method).toBe('DELETE')
    expect(init.body).toBeUndefined()
  })
})

describe('AC-5 — a non-2xx reaches the caller as the gateway wrote it', () => {
  it("a 403 reaches the caller carrying the server's own sentence", async () => {
    const msg = 'only an admin can change approval policies'
    mockFetchOnce({ ok: false, status: 403, json: () => Promise.resolve({ error: msg }) })

    const caught = await createApprovalPolicy(authed(), wireBase, 'Untitled policy').catch((e: unknown) => e)

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).message).toBe(msg)
    expect((caught as ApiError).status).toBe(403)
    expect((caught as ApiError).kind).toBe('http')
  })

  it("a 409 reaches the caller carrying the server's own sentence", async () => {
    const msg = 'an approval step names a workflow role that no longer exists'
    mockFetchOnce({ ok: false, status: 409, json: () => Promise.resolve({ error: msg }) })

    const caught = await publishApprovalPolicy(authed(), wireBase, 'p1').catch((e: unknown) => e)

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).message).toBe(msg)
    expect((caught as ApiError).status).toBe(409)
  })

  it('a 400 on a foreign scope is not reshaped into client copy', async () => {
    mockFetchOnce({ ok: false, status: 400, json: () => Promise.resolve({ error: 'invalid request' }) })

    const caught = await putApprovalPolicyDraft(authed(), wireBase, 'p1', policy({ id: 'p1' })).catch((e: unknown) => e)

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).message).toBe('invalid request')
    expect((caught as ApiError).status).toBe(400)
  })

  it('every wrapper propagates the ApiError instance itself, not a copy', async () => {
    const boom = new ApiError('http', 'only an admin can change approval policies', 403)
    const double = () => vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch

    await expect(listApprovalPolicies(double(), wireBase)).rejects.toBe(boom)
    await expect(createApprovalPolicy(double(), wireBase, 'Untitled policy')).rejects.toBe(boom)
    await expect(putApprovalPolicyDraft(double(), wireBase, 'p1', policy({ id: 'p1' }))).rejects.toBe(boom)
    await expect(publishApprovalPolicy(double(), wireBase, 'p1')).rejects.toBe(boom)
    await expect(deleteApprovalPolicy(double(), wireBase, 'p1')).rejects.toBe(boom)
  })
})

// --- QA additions: the wrappers ---------------------------------------------
// Each spec below was written against a mutation the eleven AC specs let through.

function mockFetchRejectingOnce(err: unknown) {
  const fetchMock = vi.fn().mockRejectedValue(err)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('QA: putApprovalPolicyDraft forwards what the caller holds, and projects what comes back', () => {
  it("sends the policy's own scope, never a hardcoded default", async () => {
    // scope is a *string on putDraftRequest (policy.go:104), so an absent key leaves the
    // stored scope untouched while a present foreign one earns normalizeScope's 400. Both
    // halves — presence AND the forwarded value — are the wrapper's job.
    for (const scope of ['All invoices', 'Capex & fixed assets']) {
      const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire({ id: 'p1' })) })
      await putApprovalPolicyDraft(authed(), wireBase, 'p1', policy({ id: 'p1', scope }))
      const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
      const sent = JSON.parse(init.body as string) as Record<string, unknown>
      expect(Object.hasOwn(sent, 'scope'), scope).toBe(true)
      expect(sent.scope, scope).toBe(scope)
    }
  })

  it('projects the saved draft through toPolicy rather than handing back the raw wire', async () => {
    const saved = wire({
      id: 'p1',
      version: 3,
      steps: [step({ id: 's1', kind: 'approval', workflow_role_key: 'cfo', sla_hours: 48 })],
      versions: [ver(3, false, false), ver(2, true, true)],
    })
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(saved) })

    const result = await putApprovalPolicyDraft(authed(), wireBase, 'p1', policy({ id: 'p1' }))

    expect(result).toEqual(toPolicy(saved))
    // The two fields only the projection produces: the wire carries neither.
    expect(result.activeVersion).toBe(2)
    expect(result.nodes).toHaveLength(1)
    expect((result.nodes[0] as ApprovalNode).role).toBe('cfo')
    expect(Object.hasOwn(result, 'versions')).toBe(false)
  })
})

describe('QA: the wrapper gates nothing the server owns', () => {
  it('sends a notify step the canvas left blank, with no client-side check', async () => {
    // The wrapper does not pre-validate: validateStepFields already rejects a blank
    // notify target/channel server-side, and its 400 is what the user must see.
    const blank = policy({ id: 'p1', nodes: [{ id: 'n1', type: 'notify', target: '', channel: '' }] as Policy['nodes'] })
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire({ id: 'p1' })) })

    await putApprovalPolicyDraft(authed(), wireBase, 'p1', blank)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    const sent = JSON.parse(init.body as string) as { steps: Array<Record<string, unknown>> }
    expect(sent.steps).toHaveLength(1)
    expect(sent.steps[0].kind).toBe('notify')
    expect(sent.steps[0].notify_target).toBe('')
    expect(sent.steps[0].notify_channel).toBe('')
  })

  it("sends a tree past the 64 KiB cap whole — the cap is the server's to enforce", async () => {
    // maxPolicyBodyBytes wraps the handler in a MaxBytesReader (policy_handlers.go:126).
    // A client-side slice would turn that 400 into a silent partial save.
    const many = Array.from({ length: 1500 }, (_, i) => ({
      id: `n${i}`,
      type: 'approval',
      role: 'finance_manager',
      sla: '48',
      delegate: false,
    })) as Policy['nodes']
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire({ id: 'p1' })) })

    await putApprovalPolicyDraft(authed(), wireBase, 'p1', policy({ id: 'p1', nodes: many }))

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((init.body as string).length).toBeGreaterThan(64 * 1024)
    const sent = JSON.parse(init.body as string) as { steps: unknown[] }
    expect(sent.steps).toHaveLength(1500)
  })

  it("returns the cap's own 400 sentence, not a client-side size complaint", async () => {
    mockFetchOnce({ ok: false, status: 400, json: () => Promise.resolve({ error: 'invalid request body' }) })

    const caught = await putApprovalPolicyDraft(authed(), wireBase, 'p1', policy({ id: 'p1' })).catch((e: unknown) => e)

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).message).toBe('invalid request body')
    expect((caught as ApiError).status).toBe(400)
  })
})

describe('QA: the list envelope at its edges', () => {
  it('resolves an empty array for a tenant holding no policies', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ approval_policies: [] }) })
    await expect(listApprovalPolicies(authed(), wireBase)).resolves.toEqual([])
  })

  it('fails loudly when the envelope key is absent, rather than reporting an empty tenant', async () => {
    // The key is never absent on the wire (no omitempty, [] never null). Defaulting it to
    // [] would render a broken response as "no policies yet" — the one reading a user acts on.
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    const caught = await listApprovalPolicies(authed(), wireBase).catch((e: unknown) => e)

    expect(caught).toBeInstanceOf(TypeError)
    expect(caught).not.toEqual([])
  })
})

describe('QA: the non-http failure kinds reach the caller unreshaped', () => {
  it('carries a network failure through as kind network with a null status', async () => {
    for (const run of [
      (f: AuthedFetch) => listApprovalPolicies(f, wireBase),
      (f: AuthedFetch) => publishApprovalPolicy(f, wireBase, 'p1'),
    ]) {
      mockFetchRejectingOnce(new TypeError('Failed to fetch'))
      const onUnauthorized = vi.fn()

      const caught = await run(createAuthedFetch(() => 'tok', onUnauthorized)).catch((e: unknown) => e)

      expect(caught).toBeInstanceOf(ApiError)
      expect((caught as ApiError).kind).toBe('network')
      expect((caught as ApiError).status).toBeNull()
      expect((caught as ApiError).message).toBe('Failed to fetch')
      // Only a 401 is a sign-out; an unreachable gateway must not evict the session.
      expect(onUnauthorized).not.toHaveBeenCalled()
    }
  })

  it('rejects malformed on a 200 whose body will not parse, never a half-mapped policy', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.reject(new SyntaxError('Unexpected token <')) })

    const caught = await listApprovalPolicies(authed(), wireBase).catch((e: unknown) => e)

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).kind).toBe('malformed')
    expect((caught as ApiError).message).toBe('malformed response body')
    expect((caught as ApiError).status).toBe(200)
  })

  it('fires onUnauthorized once on a 401 and still rejects with that same error', async () => {
    const runs: Array<{ name: string; run: (f: AuthedFetch) => Promise<unknown> }> = [
      { name: 'list', run: (f) => listApprovalPolicies(f, wireBase) },
      { name: 'create', run: (f) => createApprovalPolicy(f, wireBase, 'Untitled policy') },
      { name: 'putDraft', run: (f) => putApprovalPolicyDraft(f, wireBase, 'p1', policy({ id: 'p1' })) },
      { name: 'publish', run: (f) => publishApprovalPolicy(f, wireBase, 'p1') },
      { name: 'delete', run: (f) => deleteApprovalPolicy(f, wireBase, 'p1') },
    ]
    expect(runs).toHaveLength(5)

    for (const { name, run } of runs) {
      mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
      const onUnauthorized = vi.fn()

      const caught = await run(createAuthedFetch(() => 'tok', onUnauthorized)).catch((e: unknown) => e)

      // The side-effect fires AND the error still propagates: authedFetch catches only to
      // sign out, then rethrows the same instance.
      expect(onUnauthorized, name).toHaveBeenCalledTimes(1)
      expect(caught, name).toBeInstanceOf(ApiError)
      expect((caught as ApiError).status, name).toBe(401)
      expect((caught as ApiError).message, name).toBe('unauthorized')
    }
  })
})

// ============================================================================
// APPR-09-03 (task-507) — the four ctx write verbs App.tsx composes over the wrappers
// ============================================================================
// createPolicy/savePolicy/deletePolicy/publishPolicy stay INLINE closures in App.tsx, the
// setMemberStatus precedent, and Workspace cannot mount in jsdom (it needs a session and a
// live entities fetch). The harnesses below reproduce that composition exactly — wire call,
// then patch the mirror through the reducer App.tsx uses — so these specs pin the contract
// Stage 3's App.tsx has to satisfy. Same shape as roles.test.ts:1338-1400.

describe('AC-3 / AC-1 — the ctx write verbs', () => {
  async function harnessCreatePolicy(af: AuthedFetch, mirror: readonly Policy[], name: string) {
    const created = await createApprovalPolicy(af, wireBase, name)
    // Append: the server answers ORDER BY created_at, id, so the new row belongs last.
    return { created, mirror: [...mirror, created] }
  }

  async function harnessSavePolicy(af: AuthedFetch, mirror: readonly Policy[], next: Policy) {
    const saved = await putApprovalPolicyDraft(af, wireBase, next.id, next)
    return { saved, mirror: replacePolicy(mirror, saved) }
  }

  async function harnessDeletePolicy(af: AuthedFetch, mirror: readonly Policy[], id: string) {
    await deleteApprovalPolicy(af, wireBase, id)
    return removePolicy(mirror, id)
  }

  it('createPolicy stages the SERVER row last, leaving the rows before it identical', async () => {
    const created = toPolicy(wire({ id: 'pol-9', name: 'Untitled policy' }))
    const before = [policy({ id: 'polA' }), policy({ id: 'polB' })]
    const af = vi.fn().mockResolvedValue(wire({ id: 'pol-9', name: 'Untitled policy' }))

    const { created: result, mirror } = await harnessCreatePolicy(af as unknown as AuthedFetch, before, 'Untitled policy')

    expect(mirror).toHaveLength(3)
    expect(mirror.map((p) => p.id)).toEqual(['polA', 'polB', 'pol-9'])
    expect(result).toEqual(created)
    expect(mirror[0]).toBe(before[0])
    expect(mirror[1]).toBe(before[1])
  })

  it('savePolicy patches the mirror off the SERVER row, never off the policy it sent', async () => {
    // The server re-mints every step id on a PUT draft and bumps the version, so the row the
    // caller composed is already stale by the time the response lands.
    const sent = policy({ id: 'p1', name: 'Capex sign-off', version: 2, nodes: [{ id: 'local-1', type: 'autoapprove' }] })
    const af = vi
      .fn()
      .mockResolvedValue(wire({ id: 'p1', name: 'Capex sign-off', version: 3, steps: [step({ id: 'srv-1', kind: 'autoapprove' })], versions: [ver(3, false, false), ver(2, true, true)] }))

    const { saved, mirror } = await harnessSavePolicy(af as unknown as AuthedFetch, [policy({ id: 'p0' }), sent], sent)

    expect(mirror).toHaveLength(2)
    expect(mirror[1]).toBe(saved)
    expect(mirror[1]).not.toBe(sent)
    expect(mirror[1].version).toBe(3)
    expect(mirror[1].activeVersion).toBe(2)
    expect(mirror[1].nodes.map((n) => n.id)).toEqual(['srv-1'])
    // The sibling is not rebuilt: replacePolicy swaps one row, it does not remap the list.
    expect(mirror[0].id).toBe('p0')
  })

  it('deletePolicy removes by id and never patches from the inert DELETE response', async () => {
    // The DELETE answer is inert (status 'draft', version 0, steps []) — the wrapper resolves
    // undefined precisely so there is no row available to patch from.
    const af = vi.fn().mockResolvedValue(wire({ id: 'p1', status: 'draft', version: 0, steps: [], versions: [] }))
    const before = [policy({ id: 'polA' }), policy({ id: 'polB' }), policy({ id: 'polC' })]

    const mirror = await harnessDeletePolicy(af as unknown as AuthedFetch, before, 'polB')

    expect(mirror).toHaveLength(2)
    expect(mirror.map((p) => p.id)).toEqual(['polA', 'polC'])
    expect(mirror[0]).toBe(before[0])
    expect(mirror[1]).toBe(before[2])
  })

  it('a rejected write leaves the mirror untouched — nothing writes before the await', async () => {
    const boom = new ApiError('http', 'only an admin can change approval policies', 403)
    const af = vi.fn().mockRejectedValue(boom) as unknown as AuthedFetch
    const before = [policy({ id: 'polA' })]

    await expect(harnessCreatePolicy(af, before, 'Untitled policy')).rejects.toBe(boom)
    await expect(harnessSavePolicy(af, before, policy({ id: 'polA' }))).rejects.toBe(boom)
    await expect(harnessDeletePolicy(af, before, 'polA')).rejects.toBe(boom)
    expect(before.map((p) => p.id)).toEqual(['polA'])
  })
})

describe('AC-2 — publishing is the one verb a single-row patch cannot mirror', () => {
  // The active slot is TENANT-wide, so publishing B DEACTIVATES A. Publish's own response
  // describes B alone: patching the mirror from it leaves A still claiming the slot on
  // screen. Only a refetch of the whole list carries the second change.
  it('publish’s response cannot report the deactivation it caused on another policy', async () => {
    const a = policy({ id: 'polA', version: 1, activeVersion: 1, status: 'published' })
    const b = policy({ id: 'polB', version: 1, activeVersion: null, status: 'draft' })
    const mirror = [a, b]
    expect(mirror.filter((p) => p.activeVersion !== null)).toHaveLength(1)

    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire({ id: 'polB', status: 'published', version: 1, sealed: true, versions: [ver(1, true, true)] })) })
    const published = await publishApprovalPolicy(authed(), wireBase, 'polB')
    expect(published.activeVersion).toBe(1)

    const patched = replacePolicy(mirror, published)
    // Two policies in force: a tenant state the server's unique index cannot produce.
    expect(patched.filter((p) => p.activeVersion !== null).map((p) => p.id)).toEqual(['polA', 'polB'])

    mockFetchOnce({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve({
          approval_policies: [
            wire({ id: 'polA', status: 'published', version: 1, versions: [ver(1, true, false)] }),
            wire({ id: 'polB', status: 'published', version: 1, sealed: true, versions: [ver(1, true, true)] }),
          ],
        }),
    })
    const refetched = await listApprovalPolicies(authed(), wireBase)

    expect(refetched).toHaveLength(2)
    expect(refetched.filter((p) => p.activeVersion !== null).map((p) => p.id)).toEqual(['polB'])
    expect(refetched[0].activeVersion).toBeNull()
    expect(policyStanding(refetched[0])).toBe('Not in force')
  })
})
