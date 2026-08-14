// Approval policies (Workflows screen) — types and PURE reducers over one policy's node
// tree. The wire and the list-level verbs live in lib/policies.ts; the seed trees are a
// test fixture (lib/policies.fixture.ts).
//
// Ported from the Claude Design prototype Platform.dc.html: the screen markup
// (~L998-1280), the `defaultPolicies()` seed and the `wf*` builder methods
// (~L2285-2412). The prototype's `renderVals()` glue for this screen was lost to a
// 256KiB read truncation, so the derived bindings were reconstructed from the markup's
// binding names — see the port spec.
//
// The prototype MUTATES its policy tree in place (`wfMutate` + `Array.splice`). Every
// reducer here is immutable instead — React state, and the mutating originals would
// alias one tree across renders. The observable semantics are preserved, including the
// `t -= 1` same-lane reindex in `moveNode`.
//
// No reducer touches `status`: a version is sealed by the server's own publish verb, so
// a reducer that demoted a published policy would contradict what the server stored.

import { fmt } from './format'

/**
 * A role's stable key. An ALIAS, not a union: roles are runtime data (lib/roles.ts), and
 * widening the old eight-literal union leaves every signature below byte-identical.
 */
export type RoleKey = string

/**
 * Whole hours, as a STRING — '0' is the sentinel for "no deadline", which is why this
 * is not a number (0 hours and no-deadline would otherwise be the same value).
 *
 * An ALIAS, not a union: `sla_hours` is a plain server int, so a stored policy may carry
 * an hour count outside the four options below and must still render (`slaText`).
 */
export type Sla = string
export const WF_SLA_OPTIONS: readonly Sla[] = ['0', '24', '48', '72']

export type CondField = 'amount'
export type CondOp = '>' | '>=' | '<' | '<='
export const WF_OPS: readonly CondOp[] = ['>', '>=', '<', '<=']

export type ApprovalNode = {
  id: string
  type: 'approval'
  role: RoleKey
  sla: Sla
  delegate: boolean
  /**
   * The named delegate. Offered in BOTH modes — `delegates` reaches the inspector unconditionally
   * (WorkflowBuilder.tsx:524), and the mode forks on `resolve` are gone. OPTIONAL, and
   * the seed never writes it: that is what keeps every existing fixture and the whole-node
   * `toEqual` in workflows.test.ts compiling and passing untouched.
   *
   * `''` and ABSENT both mean "Anyone with the Admin or Reviewer role" — `WfSelect` is `value: string`
   * and cannot emit absence, so the default is the empty-string sentinel (the same idiom the
   * invite modal's `NO_WF_ROLE` uses). Round-tripping the toggle off and on therefore leaves
   * the key present as `''`, which is the default, not a stored choice.
   */
  delegateTo?: string
}
export type NotifyNode = { id: string; type: 'notify'; target: string; channel: string }
export type AutoApproveNode = { id: string; type: 'autoapprove' }

export type ConditionNode = {
  id: string
  type: 'condition'
  field: CondField
  op: CondOp
  /** The naira threshold — `field` has one domain, so this slot has one type. */
  value: number
  then: BranchNode[]
  else: BranchNode[]
}

/**
 * A condition may live only at the root lane, so the tree is exactly two deep. Enforced
 * in `canDrop`, in both `drop*` guards, and structurally by this type — a branch cannot
 * hold a ConditionNode, so nested conditions are unrepresentable rather than merely
 * rejected at runtime.
 */
export type BranchNode = ApprovalNode | NotifyNode | AutoApproveNode
export type WfNode = BranchNode | ConditionNode
export type NodeType = WfNode['type']

export type PolicyStatus = 'published' | 'draft'

export type Policy = {
  id: string
  name: string
  scope: string
  status: PolicyStatus
  /** The version `nodes` belongs to — the HIGHEST version, not necessarily the live one. */
  version: number
  /**
   * The version actually in force, or null when another policy holds the slot. The active
   * slot is TENANT-wide (`approval_policy_versions_one_active ON (tenant_id)`), so at most
   * one policy per tenant may carry a non-null value.
   */
  activeVersion: number | null
  nodes: WfNode[]
}

/**
 * Scope options — the one value the server stores. `normalizeScope`
 * (`internal/approval/policy.go:372-379`) and the column's own CHECK refuse anything else,
 * so a longer list here would only offer routing the product cannot perform.
 */
export const WF_SCOPE_OPTIONS: readonly string[] = ['All invoices']

// ---------------------------------------------------------------------------
// Lanes
// ---------------------------------------------------------------------------

/** 'root', or '<conditionId>:then' / '<conditionId>:else'. */
export type LaneKey = string
/** A drop target: `${laneKey}#${insertionIndex}`. */
export type Loc = string

export function parseLoc(loc: Loc): { laneKey: LaneKey; index: number } {
  const [laneKey, idx] = loc.split('#')
  return { laneKey, index: Number(idx) }
}

export function getLane(policy: Policy, laneKey: LaneKey): WfNode[] {
  if (laneKey === 'root') return policy.nodes
  const [id, branch] = laneKey.split(':')
  const node = policy.nodes.find((n) => n.id === id)
  // Missing node falls back to root, matching the prototype's wfGetLane.
  if (!node || node.type !== 'condition') return policy.nodes
  return branch === 'else' ? node.else : node.then
}

export function findNode(policy: Policy, id: string): { node: WfNode; laneKey: LaneKey; index: number } | null {
  for (let i = 0; i < policy.nodes.length; i++) {
    if (policy.nodes[i].id === id) return { node: policy.nodes[i], laneKey: 'root', index: i }
  }
  // Only ever descends one level — nested conditions do not exist.
  for (const n of policy.nodes) {
    if (n.type !== 'condition') continue
    for (const br of ['then', 'else'] as const) {
      const arr = br === 'then' ? n.then : n.else
      for (let j = 0; j < arr.length; j++) {
        if (arr[j].id === id) return { node: arr[j], laneKey: `${n.id}:${br}`, index: j }
      }
    }
  }
  return null
}

/** A condition can only be placed at the root lane. */
export function canDrop(type: NodeType | null, laneKey: LaneKey): boolean {
  return !(type === 'condition' && laneKey !== 'root')
}

/** Rebuild `policy` with `lane` replaced by `next`. */
function withLane(policy: Policy, laneKey: LaneKey, next: WfNode[]): Policy {
  if (laneKey === 'root') return { ...policy, nodes: next }
  const [id, branch] = laneKey.split(':')
  return {
    ...policy,
    nodes: policy.nodes.map((n) => {
      if (n.id !== id || n.type !== 'condition') return n
      return branch === 'else' ? { ...n, else: next as BranchNode[] } : { ...n, then: next as BranchNode[] }
    }),
  }
}

function spliced(arr: readonly WfNode[], index: number, remove: number, insert?: WfNode): WfNode[] {
  const next = arr.slice()
  if (insert) next.splice(index, remove, insert)
  else next.splice(index, remove)
  return next
}

// ---------------------------------------------------------------------------
// Node + policy reducers
// ---------------------------------------------------------------------------

let nodeSeq = 1000
/** Fresh node id. Module-level counter, mirroring the prototype's `_wfNid`. */
export function newNodeId(): string {
  return `wn${nodeSeq++}`
}

let policySeq = 100
export function newPolicyId(): string {
  return `poln${policySeq++}`
}

export function newNode(type: NodeType): WfNode {
  const id = newNodeId()
  if (type === 'condition') return { id, type, field: 'amount', op: '>', value: 100_000_000, then: [], else: [] }
  if (type === 'notify') return { id, type, target: 'Tax Team', channel: 'Email' }
  if (type === 'autoapprove') return { id, type }
  return { id, type: 'approval', role: 'fin_mgr', sla: '48', delegate: false }
}

export function newPolicy(): Policy {
  return { id: newPolicyId(), name: 'Untitled policy', scope: 'All invoices', status: 'draft', version: 1, activeVersion: null, nodes: [] }
}

export function insertNode(policy: Policy, laneKey: LaneKey, index: number, node: WfNode): Policy {
  if (!canDrop(node.type, laneKey)) return policy
  const lane = getLane(policy, laneKey)
  return withLane(policy, laneKey, spliced(lane, index, 0, node))
}

export function deleteNode(policy: Policy, id: string): Policy {
  const found = findNode(policy, id)
  if (!found) return policy
  const lane = getLane(policy, found.laneKey)
  return withLane(policy, found.laneKey, spliced(lane, found.index, 1))
}

/**
 * Remove-then-insert. The `t -= 1` correction applies only when the source and target
 * lanes are the same AND the source sat before the target — without it, dragging a node
 * downward inside one lane lands it one slot short. A condition dropped outside root is
 * a no-op (the prototype re-inserts it at its original index; returning `policy`
 * unchanged is the same observable result).
 */
export function moveNode(policy: Policy, id: string, laneKey: LaneKey, index: number): Policy {
  const found = findNode(policy, id)
  if (!found) return policy
  if (!canDrop(found.node.type, laneKey)) return policy

  const srcLane = getLane(policy, found.laneKey)
  const removed = withLane(policy, found.laneKey, spliced(srcLane, found.index, 1))

  let t = index
  if (laneKey === found.laneKey && found.index < t) t -= 1

  const dstLane = getLane(removed, laneKey)
  return withLane(removed, laneKey, spliced(dstLane, t, 0, found.node))
}

/** Click-to-append from the palette: always the tail of the root lane. */
export function appendNode(policy: Policy, type: NodeType): { policy: Policy; nodeId: string } {
  const node = newNode(type)
  return { policy: insertNode(policy, 'root', policy.nodes.length, node), nodeId: node.id }
}

/**
 * Editable fields across every node kind. `id`/`type` are omitted deliberately — a node
 * never changes kind in place (the inspector deletes and re-adds instead), and keeping
 * the `type` literals out is also what stops this intersection collapsing to `never`.
 */
export type NodePatch = Partial<Omit<ApprovalNode, 'id' | 'type'> & Omit<ConditionNode, 'id' | 'type'> & Omit<NotifyNode, 'id' | 'type'>>

export function updateNode(policy: Policy, id: string, patch: NodePatch): Policy {
  const found = findNode(policy, id)
  if (!found) return policy
  const lane = getLane(policy, found.laneKey)
  const merged = { ...lane[found.index], ...patch } as WfNode
  return withLane(policy, found.laneKey, spliced(lane, found.index, 1, merged))
}

export function clearSteps(policy: Policy): Policy {
  return { ...policy, nodes: [] }
}

export function renamePolicy(policy: Policy, name: string): Policy {
  return { ...policy, name }
}

export function rescopePolicy(policy: Policy, scope: string): Policy {
  return { ...policy, scope }
}

export function replacePolicy(list: readonly Policy[], next: Policy): Policy[] {
  return list.map((p) => (p.id === next.id ? next : p))
}

export function removePolicy(list: readonly Policy[], id: string): Policy[] {
  return list.filter((p) => p.id !== id)
}

// ---------------------------------------------------------------------------
// Derived labels
// ---------------------------------------------------------------------------

export function countApprovals(nodes: readonly WfNode[]): number {
  let c = 0
  for (const n of nodes) {
    if (n.type === 'approval') c++
    else if (n.type === 'condition') c += countApprovals(n.then) + countApprovals(n.else)
  }
  return c
}

export function countConditions(nodes: readonly WfNode[]): number {
  return nodes.filter((n) => n.type === 'condition').length
}

/** e.g. "3 approvals · 2 conditions" — the list row's summary line. */
export function policySummary(policy: Policy): string {
  const a = countApprovals(policy.nodes)
  const c = countConditions(policy.nodes)
  const parts = [`${a} ${a === 1 ? 'approval' : 'approvals'}`]
  if (c > 0) parts.push(`${c} ${c === 1 ? 'condition' : 'conditions'}`)
  return parts.join(' · ')
}

export function opLabel(op: CondOp): string {
  return { '>': 'greater than', '>=': 'at least', '<': 'less than', '<=': 'at most' }[op]
}

/**
 * The human sentence a condition card shows under its title. Copy is verbatim from the
 * prototype's `wfRuleText`. `CondField` is one literal, so the `'Condition'` fallback is
 * unreachable — kept so an added field degrades to a label rather than to a wrong amount
 * comparison.
 */
export function ruleText(n: ConditionNode): string {
  if (n.field === 'amount') return `Amount ${opLabel(n.op)} ${fmt(Number(n.value))}`
  return 'Condition'
}

export function slaText(sla: Sla): string {
  return sla === '0' ? 'no deadline' : `within ${sla}h`
}

// ---------------------------------------------------------------------------
// Scenario simulator
// ---------------------------------------------------------------------------

export type SimContext = { amount: number }

export const SIM_DEFAULT: SimContext = { amount: 750_000_000 }

export function evalCondition(n: ConditionNode, ctx: SimContext): boolean {
  // `|| 0` mirrors the prototype: a half-typed amount in the inspector must not turn
  // the whole comparison into NaN (which is false for BOTH `>` and `<=`).
  const a = Number(ctx.amount) || 0
  const v = Number(n.value) || 0
  if (n.op === '>') return a > v
  if (n.op === '>=') return a >= v
  if (n.op === '<') return a < v
  return a <= v
}

/**
 * One row of the scenario result. An `autoapprove` node produces a step of its own —
 * the simulator renders it as a distinct "no sign-off needed" row (`s.isAuto` in the
 * prototype markup), so dropping it would make that row unreachable.
 */
export type SimStep = { node: BranchNode; viaCondition: boolean }
export type SimResult = { steps: SimStep[]; auto: boolean }

/**
 * Walks the policy against one scenario and returns the steps that would actually run.
 * `auto` is STICKY — once an autoapprove is reached it stays set, but the walk continues
 * and later steps are still listed (the prototype does not truncate).
 */
export function simulate(policy: Policy, ctx: SimContext): SimResult {
  const steps: SimStep[] = []
  let auto = false
  const take = (list: readonly BranchNode[], viaCondition: boolean) => {
    for (const n of list) {
      if (n.type === 'autoapprove') auto = true
      steps.push({ node: n, viaCondition })
    }
  }
  for (const n of policy.nodes) {
    if (n.type === 'condition') take(evalCondition(n, ctx) ? n.then : n.else, true)
    else take([n], false)
  }
  return { steps, auto }
}
