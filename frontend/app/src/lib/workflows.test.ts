import { describe, expect, it } from 'vitest'

import {
  appendNode,
  canDrop,
  clearSteps,
  countApprovals,
  countConditions,
  deleteNode,
  evalCondition,
  findNode,
  getLane,
  insertNode,
  moveNode,
  newNode,
  newPolicy,
  opLabel,
  parseLoc,
  policySummary,
  removePolicy,
  renamePolicy,
  replacePolicy,
  rescopePolicy,
  ruleText,
  SIM_DEFAULT,
  simulate,
  slaText,
  updateNode,
  WF_OPS,
  WF_SCOPE_OPTIONS,
  WF_SLA_OPTIONS,
  type ApprovalNode,
  type ConditionNode,
  type NotifyNode,
  type Policy,
  type SimContext,
  type WfNode,
} from './workflows'
import * as Workflows from './workflows'
import { SEED_FIRM_POLICIES, SEED_INHOUSE_POLICIES, seedPolicies } from './policies.fixture'

// --- fixtures ---------------------------------------------------------------
// Every reducer test starts from a fresh clone, never from the frozen seed constants.
const polF1 = () => seedPolicies().firm[0]
const polH1 = () => seedPolicies().inhouse[0]
const asCond = (p: Policy, i: number) => p.nodes[i] as ConditionNode
const ids = (nodes: readonly WfNode[]) => nodes.map((n) => n.id)

const ALL_SEED = [...SEED_FIRM_POLICIES, ...SEED_INHOUSE_POLICIES]
const ALL_CONDS = ALL_SEED.flatMap((p) => p.nodes.filter((n): n is ConditionNode => n.type === 'condition'))
const ALL_NODES: WfNode[] = ALL_SEED.flatMap((p) => p.nodes.flatMap((n) => (n.type === 'condition' ? [n, ...n.then, ...n.else] : [n])))
const ALL_APPROVALS = ALL_NODES.filter((n): n is ApprovalNode => n.type === 'approval')

const approvalNode = (id: string): ApprovalNode => ({ id, type: 'approval', role: 'cfo', sla: '24', delegate: false })

describe('seed integrity — firm (§2.1)', () => {
  it('ships exactly the three firm policies, in order, with their list-row metadata', () => {
    expect(SEED_FIRM_POLICIES).toHaveLength(3)
    expect(SEED_FIRM_POLICIES.map((p) => [p.id, p.name, p.scope, p.status])).toEqual([
      ['polF1', 'Standard approval policy', 'All invoices', 'published'],
      ['polF2', 'Cross-border & FX', 'Foreign-currency invoices', 'published'],
      ['polF3', 'Government supply (B2G)', 'Document type · B2G', 'draft'],
    ])
  })

  it('polF1 is approval → cond(>250M) → cond(>1B) → approval', () => {
    const p = SEED_FIRM_POLICIES[0]
    expect(p.nodes.map((n) => n.type)).toEqual(['approval', 'condition', 'condition', 'approval'])
    expect(p.nodes[0]).toMatchObject({ type: 'approval', role: 'fin_mgr', sla: '48', delegate: true })
    expect(p.nodes[3]).toMatchObject({ type: 'approval', role: 'compliance', sla: '24', delegate: false })

    const c1 = asCond(p, 1)
    expect([c1.field, c1.op, c1.value]).toEqual(['amount', '>', 250_000_000])
    expect(c1.then).toHaveLength(1)
    expect(c1.then[0]).toMatchObject({ type: 'approval', role: 'fin_dir', sla: '48', delegate: false })
    expect(c1.else).toEqual([])

    // The only seeded branch holding two steps at the firm side: CFO sign-off then a notify.
    const c2 = asCond(p, 2)
    expect([c2.field, c2.op, c2.value]).toEqual(['amount', '>', 1_000_000_000])
    expect(c2.then.map((n) => n.type)).toEqual(['approval', 'notify'])
    expect(c2.then[0]).toMatchObject({ role: 'cfo', sla: '72' })
    expect(c2.then[1]).toMatchObject({ type: 'notify', target: 'Audit Committee', channel: 'Email' })
    expect(c2.else).toEqual([])
  })

  it('polF2 and polF3 are the same 3-node shape with different roles and thresholds', () => {
    const [, f2, f3] = SEED_FIRM_POLICIES
    for (const p of [f2, f3]) expect(p.nodes.map((n) => n.type)).toEqual(['approval', 'condition', 'approval'])

    expect(f2.nodes[0]).toMatchObject({ role: 'fin_mgr', sla: '48', delegate: false })
    expect(asCond(f2, 1)).toMatchObject({ op: '>', value: 500_000_000, else: [] })
    expect(asCond(f2, 1).then[0]).toMatchObject({ role: 'fin_dir', sla: '48' })
    expect(f2.nodes[2]).toMatchObject({ role: 'compliance', sla: '24' })

    expect(f3.nodes[0]).toMatchObject({ role: 'fin_dir', sla: '48', delegate: false })
    expect(asCond(f3, 1)).toMatchObject({ op: '>', value: 1_000_000_000, else: [] })
    expect(asCond(f3, 1).then[0]).toMatchObject({ role: 'cfo', sla: '72' })
    expect(f3.nodes[2]).toMatchObject({ role: 'compliance', sla: '24' })
  })
})

describe('seed integrity — inhouse (§2.2)', () => {
  it('ships exactly the two in-house policies, in order, with their list-row metadata', () => {
    expect(SEED_INHOUSE_POLICIES).toHaveLength(2)
    expect(SEED_INHOUSE_POLICIES.map((p) => [p.id, p.name, p.scope, p.status])).toEqual([
      ['polH1', 'Company approval policy', 'All invoices', 'published'],
      ['polH2', 'Capital expenditure', 'Capex & fixed assets', 'draft'],
    ])
  })

  it('polH1 is approval → cond(>500M) → cond(>1B, with an else) → notify', () => {
    const p = SEED_INHOUSE_POLICIES[0]
    expect(p.nodes.map((n) => n.type)).toEqual(['approval', 'condition', 'condition', 'notify'])
    expect(p.nodes[0]).toMatchObject({ role: 'line_mgr', sla: '48', delegate: true })
    expect(asCond(p, 1)).toMatchObject({ field: 'amount', op: '>', value: 500_000_000, else: [] })
    expect(asCond(p, 1).then[0]).toMatchObject({ role: 'fin_dir', sla: '48' })
    expect(asCond(p, 2)).toMatchObject({ field: 'amount', op: '>', value: 1_000_000_000 })
    expect(asCond(p, 2).then[0]).toMatchObject({ role: 'cfo', sla: '72' })
    expect(p.nodes[3]).toMatchObject({ type: 'notify', target: 'Tax Team', channel: 'In-app' })
  })

  it('polH2 is the only policy whose branch carries two approvals', () => {
    const p = SEED_INHOUSE_POLICIES[1]
    expect(p.nodes.map((n) => n.type)).toEqual(['approval', 'approval', 'condition'])
    expect(p.nodes[0]).toMatchObject({ role: 'line_mgr', sla: '48', delegate: false })
    expect(p.nodes[1]).toMatchObject({ role: 'fin_dir', sla: '48', delegate: false })
    expect(asCond(p, 2)).toMatchObject({ op: '>', value: 1_000_000_000, else: [] })
    expect(asCond(p, 2).then.map((n) => (n as ApprovalNode).role)).toEqual(['cfo', 'ceo'])
  })
})

describe('seed integrity — whole-seed invariants (§2, §8.7)', () => {
  // §8.7: the else lane is empty in 4 of the 5 seed policies. If a second one ever grows an
  // else, the default screen stops being the one that demonstrates both lane states.
  it('polH1’s third node holds the ONLY non-empty else in the seed, and it is an autoapprove', () => {
    const withElse = ALL_CONDS.filter((c) => c.else.length > 0)
    expect(withElse.map((c) => c.id)).toEqual(['h1n4'])
    expect(withElse[0].else).toEqual([{ id: 'h1n6', type: 'autoapprove' }])
    expect(withElse[0]).toBe(asCond(SEED_INHOUSE_POLICIES[0], 2))
  })

  it('autoapprove appears exactly once in the whole seed', () => {
    expect(ALL_NODES.filter((n) => n.type === 'autoapprove').map((n) => n.id)).toEqual(['h1n6'])
  })

  it('only the two first-step approvals carry delegate', () => {
    expect(ALL_APPROVALS.filter((a) => a.delegate).map((a) => a.id)).toEqual(['f1n1', 'h1n1'])
    expect(SEED_FIRM_POLICIES[0].nodes[0]).toMatchObject({ id: 'f1n1', delegate: true })
    expect(SEED_INHOUSE_POLICIES[0].nodes[0]).toMatchObject({ id: 'h1n1', delegate: true })
  })

  it('every seeded condition is an amount / “>” threshold', () => {
    expect(ALL_CONDS.map((c) => [c.id, c.field, c.op, c.value])).toEqual([
      ['f1n2', 'amount', '>', 250_000_000],
      ['f1n4', 'amount', '>', 1_000_000_000],
      ['f2n2', 'amount', '>', 500_000_000],
      ['f3n2', 'amount', '>', 1_000_000_000],
      ['h1n2', 'amount', '>', 500_000_000],
      ['h1n4', 'amount', '>', 1_000_000_000],
      ['h2n3', 'amount', '>', 1_000_000_000],
    ])
  })

  it('node ids are unique across the whole seed', () => {
    expect(new Set(ALL_NODES.map((n) => n.id)).size).toBe(ALL_NODES.length)
    expect(new Set(ALL_SEED.map((p) => p.id)).size).toBe(ALL_SEED.length)
  })

  // A seeded sla outside the inspector's own option list would render a blank select.
  // The role-key half of this check now lives in roles.test.ts's per-mode
  // "every seeded approval step names a role that exists in that mode" (WF_ROLES is gone).
  it('every seeded approval uses a listed SLA', () => {
    for (const a of ALL_APPROVALS) expect(WF_SLA_OPTIONS).toContain(a.sla)
  })
})

describe('AC-1 — workflows.ts no longer exports a role list (Core AC 5)', () => {
  it('WF_ROLES and roleOf are gone; seedPolicies is unchanged', () => {
    expect('WF_ROLES' in Workflows).toBe(false)
    expect('roleOf' in Workflows).toBe(false)
    // `Role` is a type — no runtime trace to assert; pnpm -r typecheck (AC-11) covers it.
    expect(seedPolicies().firm.map((p) => p.id)).toEqual(['polF1', 'polF2', 'polF3'])
    expect(seedPolicies().inhouse.map((p) => p.id)).toEqual(['polH1', 'polH2'])
  })
})

describe('AC-4 — lib/workflows.ts is pure reducers over a tree, and nothing else', () => {
  it('exports no seed, no store and no publish reducer', () => {
    const names = Object.keys(Workflows)
    // Control needle: a module that failed to load answers false to every check below,
    // which reads exactly like a clean file.
    expect(names.length).toBeGreaterThan(20)
    expect(names).toContain('insertNode')
    expect(names).toContain('replacePolicy')
    expect(names).toContain('removePolicy')

    expect('SEED_FIRM_POLICIES' in Workflows).toBe(false)
    expect('SEED_INHOUSE_POLICIES' in Workflows).toBe(false)
    expect('seedPolicies' in Workflows).toBe(false)
    // Saving no longer publishes — the server seals a version, on its own verb.
    expect('publishPolicy' in Workflows).toBe(false)
    expect('touch' in Workflows).toBe(false)
  })

  it('no reducer stamps `updated`, and none demotes a published policy', () => {
    const p = polF1()
    expect(p.status).toBe('published')
    const produced: Policy[] = [
      insertNode(p, 'root', 0, approvalNode('n1')),
      deleteNode(p, 'f1n2'),
      moveNode(p, 'f1n1', 'root', 2),
      updateNode(p, 'f1n1', { sla: '24' }),
      appendNode(p, 'approval').policy,
      clearSteps(p),
      renamePolicy(p, 'Renamed'),
      rescopePolicy(p, 'Consumer invoices (B2C)'),
    ]
    expect(produced).toHaveLength(8)
    for (const next of produced) {
      expect(Object.hasOwn(next, 'updated')).toBe(false)
      expect(next.status).toBe('published')
    }
    expect(Object.hasOwn(newPolicy(), 'updated')).toBe(false)
  })
})

describe('AC-3 — the list shape App.tsx’s ctx verbs patch (§4.4)', () => {
  // `savePolicy` and `deletePolicy` go through replacePolicy / removePolicy above. Append
  // has no reducer and needs none; what it does need is the ORDER, because the server
  // answers ORDER BY created_at, id and a prepend would put the new row where a refetch
  // will not.
  it('createPolicy appends, because the server orders by created_at then id', () => {
    const list = seedPolicies().firm.slice(0, 2)
    expect(list).toHaveLength(2)
    const created = { ...newPolicy(), id: 'pol-server-9' }

    const after = [...list, created]

    expect(after.map((p) => p.id)).toEqual(['polF1', 'polF2', 'pol-server-9'])
    expect(after[0]).toBe(list[0])
    expect(after[1]).toBe(list[1])
  })
})

describe('WF_SCOPE_OPTIONS (§2.4)', () => {
  // The control is a plain <select value={policy.scope}>, so a seeded scope missing from
  // this list makes that policy's select render blank. Derived, never hardcoded.
  it('covers every scope string the seed actually uses', () => {
    const seeded = [...new Set(ALL_SEED.map((p) => p.scope))]
    expect(seeded.length).toBeGreaterThan(0)
    for (const scope of seeded) expect(WF_SCOPE_OPTIONS).toContain(scope)
  })

  it('covers the default scope a brand-new policy is created with', () => {
    expect(WF_SCOPE_OPTIONS).toContain(newPolicy().scope)
  })

  it('is the 6 distinct entries the markup’s placeholder count pins', () => {
    expect(WF_SCOPE_OPTIONS).toHaveLength(6)
    expect(new Set(WF_SCOPE_OPTIONS).size).toBe(6)
  })
})

describe('seedPolicies', () => {
  it('hands out both modes with the seeded policy counts', () => {
    const store = seedPolicies()
    expect(store.firm).toHaveLength(3)
    expect(store.inhouse).toHaveLength(2)
    expect(store.firm.map((p) => p.id)).toEqual(['polF1', 'polF2', 'polF3'])
    expect(store.inhouse.map((p) => p.id)).toEqual(['polH1', 'polH2'])
  })

  // The aliasing bug the immutable reducers exist to prevent: the prototype mutates its
  // tree in place, so a shallow seed copy would let one client's edit rewrite the seed
  // (and therefore every other client's starting state).
  it('returns independent copies — a nested branch mutation never leaks to a later call', () => {
    const a = seedPolicies()
    const cond = a.firm[0].nodes[1] as ConditionNode
    cond.then.push(approvalNode('intruder'))
    ;(cond.then[0] as ApprovalNode).role = 'ceo'
    a.firm[0].name = 'hacked'
    a.firm[0].nodes.pop()
    ;(a.inhouse[0].nodes[2] as ConditionNode).else.length = 0

    const b = seedPolicies()
    expect(b.firm[0].name).toBe('Standard approval policy')
    expect(b.firm[0].nodes).toHaveLength(4)
    expect(ids((b.firm[0].nodes[1] as ConditionNode).then)).toEqual(['f1n3'])
    expect(((b.firm[0].nodes[1] as ConditionNode).then[0] as ApprovalNode).role).toBe('fin_dir')
    expect((b.inhouse[0].nodes[2] as ConditionNode).else).toHaveLength(1)
  })

  it('does not corrupt the exported seed constants either', () => {
    const a = seedPolicies()
    ;(a.firm[0].nodes[1] as ConditionNode).then.push(approvalNode('intruder'))
    a.firm[0].nodes[0].id = 'rewritten'
    ;(a.inhouse[0].nodes[2] as ConditionNode).else.pop()

    expect(SEED_FIRM_POLICIES[0].name).toBe('Standard approval policy')
    expect(SEED_FIRM_POLICIES[0].nodes[0].id).toBe('f1n1')
    expect((SEED_FIRM_POLICIES[0].nodes[1] as ConditionNode).then).toHaveLength(1)
    expect((SEED_INHOUSE_POLICIES[0].nodes[2] as ConditionNode).else).toHaveLength(1)
  })

  it('clones every node object, not just the policy wrappers', () => {
    const a = seedPolicies()
    expect(a.firm[0]).not.toBe(SEED_FIRM_POLICIES[0])
    expect(a.firm[0].nodes[1]).not.toBe(SEED_FIRM_POLICIES[0].nodes[1])
    expect((a.firm[0].nodes[1] as ConditionNode).then[0]).not.toBe((SEED_FIRM_POLICIES[0].nodes[1] as ConditionNode).then[0])
  })
})

describe('parseLoc / getLane / findNode (§4.3)', () => {
  it('parseLoc splits a drop target into its lane and insertion index', () => {
    expect(parseLoc('root#0')).toEqual({ laneKey: 'root', index: 0 })
    expect(parseLoc('f1n2:then#1')).toEqual({ laneKey: 'f1n2:then', index: 1 })
    expect(parseLoc('h1n4:else#0')).toEqual({ laneKey: 'h1n4:else', index: 0 })
  })

  it('getLane resolves root, then and else', () => {
    const p = polH1()
    expect(getLane(p, 'root')).toBe(p.nodes)
    expect(ids(getLane(p, 'h1n2:then'))).toEqual(['h1n3'])
    expect(ids(getLane(p, 'h1n4:else'))).toEqual(['h1n6'])
    expect(ids(getLane(p, 'h1n2:else'))).toEqual([])
  })

  // Documented fallback, matching the prototype's wfGetLane: a lane key naming a node that
  // is gone (or was never a condition) resolves to the root lane rather than throwing.
  it('getLane falls back to the root lane for an unknown or non-condition lane key', () => {
    const p = polH1()
    expect(getLane(p, 'ghost:then')).toBe(p.nodes)
    expect(getLane(p, 'h1n1:then')).toBe(p.nodes) // h1n1 is an approval, not a condition
    expect(getLane(p, 'h1n3:then')).toBe(p.nodes) // h1n3 lives inside a branch — wfFind never sees it
  })

  it('findNode locates a root node with its index', () => {
    const p = polF1()
    expect(findNode(p, 'f1n1')).toEqual({ node: p.nodes[0], laneKey: 'root', index: 0 })
    expect(findNode(p, 'f1n7')).toEqual({ node: p.nodes[3], laneKey: 'root', index: 3 })
  })

  it('findNode locates then- and else-lane nodes with the right laneKey and index', () => {
    const p = polH1()
    expect(findNode(p, 'h1n3')).toMatchObject({ laneKey: 'h1n2:then', index: 0 })
    expect(findNode(p, 'h1n6')).toMatchObject({ laneKey: 'h1n4:else', index: 0 })
    expect(findNode(p, 'h1n6')!.node).toBe((p.nodes[2] as ConditionNode).else[0])
    // Second item in a two-step branch — the index must not collapse to 0.
    expect(findNode(seedPolicies().firm[0], 'f1n6')).toMatchObject({ laneKey: 'f1n4:then', index: 1 })
  })

  it('findNode returns null for an id that is not in the policy', () => {
    expect(findNode(polF1(), 'h1n1')).toBeNull()
    expect(findNode(polF1(), '')).toBeNull()
  })
})

describe('canDrop', () => {
  it('allows a condition only at the root lane', () => {
    expect(canDrop('condition', 'root')).toBe(true)
    expect(canDrop('condition', 'f1n2:then')).toBe(false)
    expect(canDrop('condition', 'h1n4:else')).toBe(false)
  })

  it('allows every other node type in every lane', () => {
    for (const type of ['approval', 'notify', 'autoapprove'] as const) {
      for (const lane of ['root', 'f1n2:then', 'h1n4:else']) expect(canDrop(type, lane)).toBe(true)
    }
  })

  it('treats “no drag in progress” as droppable (the slot decides on the type, not on null)', () => {
    expect(canDrop(null, 'f1n2:then')).toBe(true)
  })
})

describe('insertNode', () => {
  it('inserts at the given index in the root lane and returns a new policy', () => {
    const before = polF1()
    const after = insertNode(before, 'root', 1, approvalNode('new1'))
    expect(ids(after.nodes)).toEqual(['f1n1', 'new1', 'f1n2', 'f1n4', 'f1n7'])
    expect(after).not.toBe(before)
    expect(ids(before.nodes)).toEqual(['f1n1', 'f1n2', 'f1n4', 'f1n7'])
  })

  it('inserts into a branch lane without touching the sibling lane or the other nodes', () => {
    const before = polH1()
    const after = insertNode(before, 'h1n4:then', 0, approvalNode('new1'))
    expect(ids((after.nodes[2] as ConditionNode).then)).toEqual(['new1', 'h1n5'])
    expect(ids((after.nodes[2] as ConditionNode).else)).toEqual(['h1n6'])
    expect(ids((before.nodes[2] as ConditionNode).then)).toEqual(['h1n5'])
    // Siblings keep identity — only the touched condition is rebuilt.
    expect(after.nodes[0]).toBe(before.nodes[0])
    expect(after.nodes[1]).toBe(before.nodes[1])
    expect(after.nodes[2]).not.toBe(before.nodes[2])
  })

  it('appends when the index is the lane length (the tail slot)', () => {
    const before = polF1()
    expect(ids(insertNode(before, 'root', before.nodes.length, approvalNode('tail')).nodes).at(-1)).toBe('tail')
  })

  it('rejects a condition dropped outside root, unchanged', () => {
    const before = polH1()
    const cond: WfNode = { id: 'c9', type: 'condition', field: 'amount', op: '>', value: 1, then: [], else: [] }
    expect(insertNode(before, 'h1n2:then', 0, cond)).toBe(before)
  })
})

describe('deleteNode', () => {
  it('editing a node leaves status alone — the server decides what a save does to a version', () => {
    const before = polF1()
    expect(before.status).toBe('published')
    const after = deleteNode(before, 'f1n2')
    expect(ids(after.nodes)).toEqual(['f1n1', 'f1n4', 'f1n7'])
    expect(after).not.toBe(before)
    expect(ids(before.nodes)).toEqual(['f1n1', 'f1n2', 'f1n4', 'f1n7'])
    // The reducer edits a tree; sealing a version is the server's own verb.
    expect(after.status).toBe('published')
    expect(Object.hasOwn(after, 'updated')).toBe(false)
  })

  it('removes a branch node, leaving the condition and its other lane in place', () => {
    const before = polH1()
    const after = deleteNode(before, 'h1n6')
    expect((after.nodes[2] as ConditionNode).else).toEqual([])
    expect(ids((after.nodes[2] as ConditionNode).then)).toEqual(['h1n5'])
    expect((before.nodes[2] as ConditionNode).else).toHaveLength(1)
  })

  it('deleting a condition takes its whole subtree with it', () => {
    const after = deleteNode(polF1(), 'f1n4')
    expect(ids(after.nodes)).toEqual(['f1n1', 'f1n2', 'f1n7'])
    expect(findNode(after, 'f1n5')).toBeNull()
    expect(findNode(after, 'f1n6')).toBeNull()
  })

  it('is a no-op for an unknown id', () => {
    const before = polF1()
    expect(deleteNode(before, 'nope')).toBe(before)
  })
})

describe('appendNode (§8.4 — palette click always lands at the root tail)', () => {
  it('appends to the root tail and reports the new node id', () => {
    const before = polF1()
    const { policy, nodeId } = appendNode(before, 'approval')
    expect(ids(policy.nodes)).toEqual(['f1n1', 'f1n2', 'f1n4', 'f1n7', nodeId])
    expect(policy).not.toBe(before)
    expect(before.nodes).toHaveLength(4)
  })

  it('appends to the root tail even for a condition (the one type that is root-only)', () => {
    const { policy, nodeId } = appendNode(polH1(), 'condition')
    expect(policy.nodes.at(-1)).toMatchObject({ id: nodeId, type: 'condition', field: 'amount', op: '>', value: 100_000_000, then: [], else: [] })
  })

  it('creates each node kind with the prototype’s defaults (§1)', () => {
    expect(appendNode(polF1(), 'approval').policy.nodes.at(-1)).toMatchObject({ type: 'approval', role: 'fin_mgr', sla: '48', delegate: false })
    expect(appendNode(polF1(), 'notify').policy.nodes.at(-1)).toMatchObject({ type: 'notify', target: 'Tax Team', channel: 'Email' })
    expect(appendNode(polF1(), 'autoapprove').policy.nodes.at(-1)).toEqual({ id: expect.any(String), type: 'autoapprove' })
  })

  it('never reissues a node id', () => {
    const a = appendNode(polF1(), 'approval')
    const b = appendNode(a.policy, 'approval')
    expect(b.nodeId).not.toBe(a.nodeId)
    expect(new Set(ids(b.policy.nodes)).size).toBe(b.policy.nodes.length)
  })
})

describe('moveNode (§4.3 — remove-then-insert with the t -= 1 correction)', () => {
  // Without the correction, dragging a node DOWN inside one lane lands it one slot short,
  // because the removal already shifted every later index left by one.
  it('same-lane downward move lands at the intended slot', () => {
    const after = moveNode(polF1(), 'f1n1', 'root', 2)
    expect(ids(after.nodes)).toEqual(['f1n2', 'f1n1', 'f1n4', 'f1n7'])
    expect(findNode(after, 'f1n1')).toMatchObject({ laneKey: 'root', index: 1 })
  })

  it('same-lane move to the tail slot lands last', () => {
    expect(ids(moveNode(polF1(), 'f1n1', 'root', 4).nodes)).toEqual(['f1n2', 'f1n4', 'f1n7', 'f1n1'])
  })

  it('same-lane upward move takes no correction', () => {
    const after = moveNode(polF1(), 'f1n7', 'root', 1)
    expect(ids(after.nodes)).toEqual(['f1n1', 'f1n7', 'f1n2', 'f1n4'])
    expect(findNode(after, 'f1n7')).toMatchObject({ laneKey: 'root', index: 1 })
  })

  it('dropping a node on its own slot leaves the order alone', () => {
    expect(ids(moveNode(polF1(), 'f1n2', 'root', 1).nodes)).toEqual(['f1n1', 'f1n2', 'f1n4', 'f1n7'])
  })

  it('reorders inside a branch lane with the same correction', () => {
    const after = moveNode(polF1(), 'f1n5', 'f1n4:then', 2)
    expect(ids((after.nodes[2] as ConditionNode).then)).toEqual(['f1n6', 'f1n5'])
  })

  it('moves a root node into a condition’s then lane', () => {
    const before = polF1()
    const after = moveNode(before, 'f1n7', 'f1n2:then', 1)
    expect(ids(after.nodes)).toEqual(['f1n1', 'f1n2', 'f1n4'])
    expect(ids((after.nodes[1] as ConditionNode).then)).toEqual(['f1n3', 'f1n7'])
    expect(ids(before.nodes)).toEqual(['f1n1', 'f1n2', 'f1n4', 'f1n7'])
  })

  it('moves it back out of the branch to the root lane', () => {
    const parked = moveNode(polF1(), 'f1n7', 'f1n2:then', 1)
    const back = moveNode(parked, 'f1n7', 'root', 0)
    expect(ids(back.nodes)).toEqual(['f1n7', 'f1n1', 'f1n2', 'f1n4'])
    expect(ids((back.nodes[2] as ConditionNode).then)).toEqual(['f1n3'])
    expect(findNode(back, 'f1n7')).toMatchObject({ laneKey: 'root', index: 0 })
  })

  it('moves between the two lanes of the same condition', () => {
    const after = moveNode(polH1(), 'h1n6', 'h1n4:then', 0)
    expect(ids((after.nodes[2] as ConditionNode).then)).toEqual(['h1n6', 'h1n5'])
    expect((after.nodes[2] as ConditionNode).else).toEqual([])
  })

  it('rejects a condition moved into a branch lane, policy unchanged', () => {
    const before = polF1()
    expect(moveNode(before, 'f1n2', 'f1n4:then', 0)).toBe(before)
    expect(moveNode(before, 'f1n2', 'f1n4:else', 0)).toBe(before)
    expect(ids(before.nodes)).toEqual(['f1n1', 'f1n2', 'f1n4', 'f1n7'])
  })

  it('is a no-op for an unknown id', () => {
    const before = polF1()
    expect(moveNode(before, 'nope', 'root', 0)).toBe(before)
  })
})

describe('updateNode', () => {
  it('patches one field and leaves the rest of the node intact', () => {
    const before = polF1()
    const after = updateNode(before, 'f1n1', { sla: '24' })
    expect(after.nodes[0]).toEqual({ id: 'f1n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: true })
    expect(before.nodes[0]).toMatchObject({ sla: '48' })
  })

  it('patches a condition’s field and value together (the domain switch)', () => {
    const after = updateNode(polF1(), 'f1n2', { field: 'docType', value: 'B2G' })
    expect(after.nodes[1]).toMatchObject({ type: 'condition', field: 'docType', value: 'B2G', op: '>' })
    // Branch contents survive a domain switch — the inspector only rewrites the test.
    expect(ids((after.nodes[1] as ConditionNode).then)).toEqual(['f1n3'])
  })

  it('leaves siblings and the other lane untouched when patching a branch node', () => {
    const before = polH1()
    const after = updateNode(before, 'h1n5', { role: 'ceo' })
    const cond = after.nodes[2] as ConditionNode
    expect(cond.then[0]).toMatchObject({ id: 'h1n5', role: 'ceo', sla: '72' })
    expect(cond.else).toEqual([{ id: 'h1n6', type: 'autoapprove' }])
    expect(after.nodes[0]).toBe(before.nodes[0])
    expect(after.nodes[1]).toBe(before.nodes[1])
    expect(after.nodes[3]).toBe(before.nodes[3])
  })

  it('is a no-op for an unknown id', () => {
    const before = polF1()
    expect(updateNode(before, 'nope', { sla: '24' })).toBe(before)
  })
})

describe('policy-level reducers', () => {
  it('clearSteps empties the canvas without touching the policy’s identity', () => {
    const before = polF1()
    const after = clearSteps(before)
    expect(after.nodes).toEqual([])
    expect(after).toMatchObject({ id: 'polF1', name: 'Standard approval policy', scope: 'All invoices' })
    expect(before.nodes).toHaveLength(4)
  })

  it('renamePolicy rewrites only the name', () => {
    const before = polF1()
    expect(before.status).toBe('published')
    const after = renamePolicy(before, 'Renamed')
    expect(after).toEqual({ ...before, name: 'Renamed' })
    expect(before.name).toBe('Standard approval policy')
  })

  it('rescopePolicy rewrites only the scope', () => {
    const before = polF1()
    const after = rescopePolicy(before, 'Consumer invoices (B2C)')
    expect(after).toEqual({ ...before, scope: 'Consumer invoices (B2C)' })
    expect(before.scope).toBe('All invoices')
  })

  it('replacePolicy swaps by id, keeping order, length and sibling identity', () => {
    const list = seedPolicies().firm
    const next = renamePolicy(list[1], 'Swapped')
    const after = replacePolicy(list, next)
    expect(after.map((p) => p.id)).toEqual(['polF1', 'polF2', 'polF3'])
    expect(after[1]).toBe(next)
    expect(after[0]).toBe(list[0])
    expect(after[2]).toBe(list[2])
    expect(list[1].name).toBe('Cross-border & FX')
  })

  it('replacePolicy ignores a policy that is not in the list', () => {
    const list = seedPolicies().firm
    expect(replacePolicy(list, { ...newPolicy(), id: 'ghost' })).toEqual(list)
  })

  it('removePolicy drops only the named policy', () => {
    const list = seedPolicies().firm
    expect(removePolicy(list, 'polF2').map((p) => p.id)).toEqual(['polF1', 'polF3'])
    expect(removePolicy(list, 'ghost')).toHaveLength(3)
    expect(list).toHaveLength(3)
  })

  it('newPolicy starts as an empty draft', () => {
    expect(newPolicy()).toMatchObject({ name: 'Untitled policy', scope: 'All invoices', status: 'draft', nodes: [] })
    expect(newPolicy().id).not.toBe(newPolicy().id)
  })
})

describe('derived labels (§5.1)', () => {
  it('countApprovals recurses into BOTH branches, not just the taken one', () => {
    const cond: WfNode = {
      id: 'c1',
      type: 'condition',
      field: 'amount',
      op: '>',
      value: 1,
      then: [approvalNode('a1')],
      else: [approvalNode('a2')],
    }
    expect(countApprovals([cond])).toBe(2)
    expect(countApprovals([cond, approvalNode('a3')])).toBe(3)
  })

  it('countApprovals over the seed', () => {
    expect(ALL_SEED.map((p) => countApprovals(p.nodes))).toEqual([4, 3, 3, 3, 4])
  })

  it('countApprovals ignores notify and autoapprove nodes', () => {
    expect(countApprovals([])).toBe(0)
    expect(countApprovals((SEED_INHOUSE_POLICIES[0].nodes[2] as ConditionNode).else)).toBe(0)
  })

  it('countConditions counts the ROOT lane only', () => {
    expect(ALL_SEED.map((p) => countConditions(p.nodes))).toEqual([2, 1, 1, 2, 1])
    // A nested condition is unrepresentable in the type, but the count must not walk into
    // branches even if one somehow appeared there.
    const nested = [
      { id: 'c1', type: 'condition', field: 'amount', op: '>', value: 1, then: [{ id: 'c2', type: 'condition', field: 'amount', op: '>', value: 2, then: [], else: [] }], else: [] },
    ] as unknown as WfNode[]
    expect(countConditions(nested)).toBe(1)
  })

  it('policySummary drops the conditions clause when there are none, and pluralises both counts', () => {
    expect(policySummary(SEED_FIRM_POLICIES[0])).toBe('4 approvals · 2 conditions')
    expect(policySummary(SEED_FIRM_POLICIES[1])).toBe('3 approvals · 1 condition')
    expect(policySummary(SEED_INHOUSE_POLICIES[0])).toBe('3 approvals · 2 conditions')
    expect(policySummary(SEED_INHOUSE_POLICIES[1])).toBe('4 approvals · 1 condition')
    expect(policySummary(clearSteps(polF1()))).toBe('0 approvals')
    expect(policySummary({ ...newPolicy(), nodes: [approvalNode('a1')] })).toBe('1 approval')
  })

  it('opLabel has a phrase for every operator', () => {
    expect(WF_OPS.map(opLabel)).toEqual(['greater than', 'at least', 'less than', 'at most'])
  })

  // Copy is verbatim from the prototype's wfRuleText — "Amount greater than …", not
  // "Amount is greater than …" — because opLabel already supplies the verb phrase.
  it('ruleText renders the amount domain as a naira threshold sentence', () => {
    expect(ruleText(SEED_FIRM_POLICIES[0].nodes[1] as ConditionNode)).toBe('Amount greater than ₦250,000,000')
    const c = { ...(SEED_FIRM_POLICIES[0].nodes[1] as ConditionNode) }
    expect(ruleText({ ...c, op: '>=' })).toBe('Amount at least ₦250,000,000')
    expect(ruleText({ ...c, op: '<' })).toBe('Amount less than ₦250,000,000')
    expect(ruleText({ ...c, op: '<=', value: 1_000_000_000 })).toBe('Amount at most ₦1,000,000,000')
  })

  it('ruleText renders the docType domain, ignoring the now-meaningless operator', () => {
    const base = { ...(SEED_FIRM_POLICIES[0].nodes[1] as ConditionNode), field: 'docType' as const }
    expect(ruleText({ ...base, value: 'B2G' })).toBe('Document type is B2G')
    expect(ruleText({ ...base, value: 'B2C', op: '<=' })).toBe('Document type is B2C')
  })

  it('ruleText renders the newCustomer domain as either side of one boolean', () => {
    const base = { ...(SEED_FIRM_POLICIES[0].nodes[1] as ConditionNode), field: 'newCustomer' as const }
    expect(ruleText({ ...base, value: true })).toBe('Customer is new / unverified')
    expect(ruleText({ ...base, value: false })).toBe('Customer is existing')
  })

  it('slaText treats “0” as the no-deadline sentinel, not as zero hours', () => {
    expect(slaText('0')).toBe('no deadline')
    expect(slaText('24')).toBe('within 24h')
    expect(slaText('48')).toBe('within 48h')
    expect(slaText('72')).toBe('within 72h')
  })

  // The server accepts any sla_hours, so a policy can carry an SLA outside WF_SLA_OPTIONS.
  // Its RED was tsc rejecting '36' before Sla widened — vitest strips types. It still bites
  // at runtime: an slaText that blanked an off-vocabulary value would fail here.
  it('slaText renders an off-vocabulary SLA rather than blanking', () => {
    expect(slaText('36')).toBe('within 36h')
    expect(slaText('0')).toBe('no deadline')
  })
})

describe('evalCondition (§6.1)', () => {
  const ctx = (over: Partial<SimContext> = {}): SimContext => ({ ...SIM_DEFAULT, ...over })
  const amountCond = (op: ConditionNode['op'], value: number): ConditionNode => ({ id: 'c', type: 'condition', field: 'amount', op, value, then: [], else: [] })

  it('compares the scenario amount against the threshold, boundary included', () => {
    const at = ctx({ amount: 500_000_000 })
    expect(evalCondition(amountCond('>', 500_000_000), at)).toBe(false)
    expect(evalCondition(amountCond('>=', 500_000_000), at)).toBe(true)
    expect(evalCondition(amountCond('<', 500_000_000), at)).toBe(false)
    expect(evalCondition(amountCond('<=', 500_000_000), at)).toBe(true)
  })

  it('compares strictly above and below the threshold', () => {
    expect(evalCondition(amountCond('>', 250_000_000), ctx({ amount: 250_000_001 }))).toBe(true)
    expect(evalCondition(amountCond('>=', 250_000_000), ctx({ amount: 249_999_999 }))).toBe(false)
    expect(evalCondition(amountCond('<', 250_000_000), ctx({ amount: 1 }))).toBe(true)
    expect(evalCondition(amountCond('<=', 250_000_000), ctx({ amount: 250_000_001 }))).toBe(false)
  })

  it('matches the docType domain exactly, ignoring the operator', () => {
    const c: ConditionNode = { id: 'c', type: 'condition', field: 'docType', op: '>', value: 'B2G', then: [], else: [] }
    expect(evalCondition(c, ctx({ docType: 'B2G' }))).toBe(true)
    expect(evalCondition(c, ctx({ docType: 'B2B' }))).toBe(false)
    expect(evalCondition(c, ctx({ docType: 'B2C' }))).toBe(false)
    // The scenario amount is irrelevant in this domain.
    expect(evalCondition(c, ctx({ docType: 'B2G', amount: 0 }))).toBe(true)
  })

  it('matches the newCustomer domain on both sides of the boolean', () => {
    const isNew: ConditionNode = { id: 'c', type: 'condition', field: 'newCustomer', op: '>', value: true, then: [], else: [] }
    expect(evalCondition(isNew, ctx({ newCustomer: true }))).toBe(true)
    expect(evalCondition(isNew, ctx({ newCustomer: false }))).toBe(false)
    expect(evalCondition({ ...isNew, value: false }, ctx({ newCustomer: false }))).toBe(true)
    expect(evalCondition({ ...isNew, value: false }, ctx({ newCustomer: true }))).toBe(false)
  })

  it('the shipped default scenario is ₦750,000,000 · B2B · returning customer', () => {
    expect(SIM_DEFAULT).toEqual({ amount: 750_000_000, docType: 'B2B', newCustomer: false })
  })
})

describe('simulate (§6.2)', () => {
  it('walks polF1 under the default scenario: the >250M branch is taken, the >1B one is not', () => {
    const { steps, auto } = simulate(polF1(), SIM_DEFAULT)
    expect(ids(steps.map((s) => s.node))).toEqual(['f1n1', 'f1n3', 'f1n7'])
    expect(steps.map((s) => s.viaCondition)).toEqual([false, true, false])
    expect(auto).toBe(false)
    // §6.4: the summary line for exactly this scenario reads "3 APPROVALS REQUIRED".
    expect(steps.filter((s) => s.node.type === 'approval')).toHaveLength(3)
  })

  it('takes the then branch when the threshold is cleared', () => {
    const { steps } = simulate(polF1(), { ...SIM_DEFAULT, amount: 2_000_000_000 })
    expect(ids(steps.map((s) => s.node))).toEqual(['f1n1', 'f1n3', 'f1n5', 'f1n6', 'f1n7'])
  })

  it('takes the (empty) else branch when no threshold is cleared', () => {
    const { steps, auto } = simulate(polF1(), { ...SIM_DEFAULT, amount: 1_000 })
    expect(ids(steps.map((s) => s.node))).toEqual(['f1n1', 'f1n7'])
    expect(auto).toBe(false)
  })

  // polH1 is the only seed policy with a non-empty else, so it is the only one that can
  // reach an autoapprove at all (§8.7).
  // The autoapprove gets a step of its OWN as well as setting the flag (prototype
  // wfSimulate pushes `{kind:'auto'}`). The simulator renders it as a distinct row, so
  // emitting only the flag would make that row unreachable.
  it('reaches polH1’s autoapprove below ₦1,000,000,000, emits it as a step, and sets auto', () => {
    const { steps, auto } = simulate(polH1(), SIM_DEFAULT)
    expect(auto).toBe(true)
    expect(ids(steps.map((s) => s.node))).toEqual(['h1n1', 'h1n3', 'h1n6', 'h1n7'])
    expect(steps.find((s) => s.node.id === 'h1n6')!.node.type).toBe('autoapprove')
    // It came out of the condition's else lane, so it is flagged as conditional.
    expect(steps.find((s) => s.node.id === 'h1n6')!.viaCondition).toBe(true)
  })

  it('auto does NOT truncate the walk — the notify after it still runs', () => {
    const { steps, auto } = simulate(polH1(), SIM_DEFAULT)
    expect(auto).toBe(true)
    const last = steps.at(-1)!
    expect(last.node).toMatchObject({ id: 'h1n7', type: 'notify' })
    expect((last.node as NotifyNode).target).toBe('Tax Team')
    expect(last.viaCondition).toBe(false)
  })

  it('above ₦1,000,000,000 polH1 takes the CFO branch instead, and auto stays false', () => {
    const { steps, auto } = simulate(polH1(), { ...SIM_DEFAULT, amount: 1_500_000_000 })
    expect(ids(steps.map((s) => s.node))).toEqual(['h1n1', 'h1n3', 'h1n5', 'h1n7'])
    expect(auto).toBe(false)
  })

  it('marks branch-sourced steps with viaCondition and root steps without it', () => {
    const { steps } = simulate(polH1(), { ...SIM_DEFAULT, amount: 1_500_000_000 })
    expect(steps.map((s) => [s.node.id, s.viaCondition])).toEqual([
      ['h1n1', false],
      ['h1n3', true],
      ['h1n5', true],
      ['h1n7', false],
    ])
  })

  it('an empty policy simulates to nothing at all', () => {
    expect(simulate(clearSteps(polF1()), SIM_DEFAULT)).toEqual({ steps: [], auto: false })
  })

  it('reads the tree, never rewrites it', () => {
    const p = polH1()
    const snapshot = JSON.stringify(p)
    simulate(p, { ...SIM_DEFAULT, amount: 5 })
    expect(JSON.stringify(p)).toBe(snapshot)
  })
})

// ---------------------------------------------------------------------------
// delegateTo — MEMB-01-08's single schema addition
// ---------------------------------------------------------------------------
//
// `ApprovalNode.delegateTo` is optional and the seed never writes it. That is not an
// accident of style: it is what keeps the whole-node `toEqual` above (`updateNode > patches
// one field…`) passing, because vitest's `toEqual` ignores an `undefined` property but NOT a
// present one. Writing `delegateTo: ''` into the seed constructor makes that spec fail; these
// pin the invariant so a later seed edit cannot quietly reintroduce it.

describe('delegateTo', () => {
  const firstApproval = (p: Policy) => p.nodes.find((n) => n.type === 'approval') as ApprovalNode

  it('is ABSENT — not undefined — on every seeded approval node, in both modes', () => {
    let visited = 0
    const walk = (ns: readonly WfNode[]) => {
      for (const n of ns) {
        if (n.type === 'approval') {
          visited += 1
          expect(Object.hasOwn(n, 'delegateTo')).toBe(false)
        }
        if (n.type === 'condition') {
          walk(n.then)
          walk(n.else)
        }
      }
    }
    const seeds = seedPolicies()
    ;[...seeds.firm, ...seeds.inhouse].forEach((p) => walk(p.nodes))
    // Guards against a vacuous pass: the walk must actually have reached every approval,
    // condition lanes included (10 firm + 7 in-house).
    expect(visited).toBe(ALL_APPROVALS.length)
    expect(visited).toBe(17)
  })

  it('is absent on a freshly created approval node', () => {
    const n = newNode('approval') as ApprovalNode
    expect(Object.hasOwn(n, 'delegateTo')).toBe(false)
    expect(n.delegate).toBe(false)
  })

  it('rides the ordinary NodePatch path onto a root-lane node', () => {
    const before = polH1()
    const after = updateNode(before, firstApproval(before).id, { delegateTo: 'Tunde Adeyemi' })
    expect(firstApproval(after).delegateTo).toBe('Tunde Adeyemi')
    expect(firstApproval(after)).toMatchObject({ role: firstApproval(before).role, sla: firstApproval(before).sla })
  })

  it('rides the same path onto a node inside a condition lane', () => {
    const after = updateNode(polH1(), 'h1n5', { delegateTo: 'Ibrahim Bello' })
    expect((asCond(after, 2).then[0] as ApprovalNode).delegateTo).toBe('Ibrahim Bello')
  })

  it("stores the '' sentinel as a real value — '' and absent both mean the default", () => {
    const p = updateNode(polH1(), 'h1n1', { delegateTo: 'Tunde Adeyemi' })
    const cleared = updateNode(p, 'h1n1', { delegateTo: '' })
    expect((cleared.nodes[0] as ApprovalNode).delegateTo).toBe('')
    expect(Object.hasOwn(cleared.nodes[0], 'delegateTo')).toBe(true)
  })

  it('delegateTo rides the ordinary patch path and leaves status alone', () => {
    const before = polH1()
    expect(before.status).toBe('published')
    const after = updateNode(before, 'h1n1', { delegateTo: 'Tunde Adeyemi' })
    expect((after.nodes[0] as ApprovalNode).delegateTo).toBe('Tunde Adeyemi')
    expect(after.status).toBe('published')
    expect(Object.hasOwn(after, 'updated')).toBe(false)
  })

  it('never mutates the policy it was given', () => {
    const before = polH1()
    const snapshot = JSON.stringify(before)
    updateNode(before, 'h1n1', { delegateTo: 'Tunde Adeyemi' })
    expect(JSON.stringify(before)).toBe(snapshot)
  })

  it('survives an unrelated later patch to the same node', () => {
    const withDelegate = updateNode(polH1(), 'h1n1', { delegateTo: 'Tunde Adeyemi' })
    const thenSla = updateNode(withDelegate, 'h1n1', { sla: '24' })
    expect(thenSla.nodes[0]).toMatchObject({ sla: '24', delegateTo: 'Tunde Adeyemi' })
  })

  it('is untouched by toggling the `delegate` boolean off', () => {
    const on = updateNode(polH1(), 'h1n1', { delegate: true, delegateTo: 'Tunde Adeyemi' })
    const off = updateNode(on, 'h1n1', { delegate: false })
    // The inspector hides the picker but does not clear the choice — turning delegation
    // back on restores the same delegate rather than silently resetting to "anyone".
    expect((off.nodes[0] as ApprovalNode).delegateTo).toBe('Tunde Adeyemi')
  })
})
