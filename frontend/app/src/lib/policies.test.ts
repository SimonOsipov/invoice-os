import { describe, expect, it } from 'vitest'

import {
  nodesFromSteps,
  policyInForce,
  policyStanding,
  stepInputsFromNodes,
  toPolicy,
  type PolicyStepWire,
  type PolicyVersionWire,
  type PolicyWire,
} from './policies'
import { seedPolicies } from './policies.fixture'
import type { ApprovalNode, ConditionNode, NotifyNode, Policy } from './workflows'

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
