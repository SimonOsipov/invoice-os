// Approval policies (Workflows screen).
//
// Ported from the Claude Design prototype Platform.dc.html: the screen markup
// (~L998-1280), the `defaultPolicies()` seed and the `wf*` builder methods
// (~L2285-2412). The prototype's `renderVals()` glue for this screen was lost to a
// 256KiB read truncation, so the derived bindings were reconstructed from the markup's
// binding names — see the port spec. Anything reconstructed is marked below.
//
// Everything here is mock data + pure functions. There is no approvals endpoint (the
// backend has no approval concept at all — the invoice lifecycle is
// draft/validated/queued/submitted/accepted/rejected/failed, with nothing "awaiting
// approval"), so this screen is deliberately shaped so that swapping these constants
// for a fetch changes no component.
//
// The prototype MUTATES its policy tree in place (`wfMutate` + `Array.splice`). Every
// reducer here is immutable instead — React state, and the mutating originals would
// alias the seed constants across clients. The observable semantics are preserved,
// including the `t -= 1` same-lane reindex in `moveNode`.

import { fmt } from './format'

/** Approver roles, in the order the inspector's role select lists them. */
export type RoleKey = 'preparer' | 'line_mgr' | 'fin_mgr' | 'controller' | 'fin_dir' | 'compliance' | 'cfo' | 'ceo'

export type Role = { key: RoleKey; title: string; line: string }

export const WF_ROLES: readonly Role[] = [
  { key: 'preparer', title: 'Preparer', line: 'Accounts Payable' },
  { key: 'line_mgr', title: 'Line Manager', line: 'Requesting dept.' },
  { key: 'fin_mgr', title: 'Finance Manager', line: 'Finance' },
  { key: 'controller', title: 'Financial Controller', line: 'Finance' },
  { key: 'fin_dir', title: 'Finance Director', line: 'Finance' },
  { key: 'compliance', title: 'Compliance Officer', line: 'Tax & Compliance' },
  { key: 'cfo', title: 'CFO', line: 'Executive' },
  { key: 'ceo', title: 'CEO', line: 'Executive' },
]

/** Unknown key falls back to a generic approver rather than rendering a raw id. */
export function roleOf(key: string): Role {
  return WF_ROLES.find((r) => r.key === key) ?? { key: key as RoleKey, title: 'Approver', line: '' }
}

/**
 * Whole hours, as a STRING — '0' is the sentinel for "no deadline", which is why this
 * is not a number (0 hours and no-deadline would otherwise be the same value).
 */
export type Sla = '0' | '24' | '48' | '72'
export const WF_SLA_OPTIONS: readonly Sla[] = ['0', '24', '48', '72']

export type WfDocType = 'B2B' | 'B2G' | 'B2C'
export type CondField = 'amount' | 'docType' | 'newCustomer'
export type CondOp = '>' | '>=' | '<' | '<='
export const WF_OPS: readonly CondOp[] = ['>', '>=', '<', '<=']

export type ApprovalNode = {
  id: string
  type: 'approval'
  role: RoleKey
  sla: Sla
  delegate: boolean
  /**
   * IN-HOUSE only — the named delegate, when `delegate` is on (MEMB-01 §11.3). OPTIONAL, and
   * the seed never writes it: that is what keeps every existing fixture and the whole-node
   * `toEqual` in workflows.test.ts compiling and passing untouched.
   *
   * `''` and ABSENT both mean "Anyone with the Reviewer role" — `WfSelect` is `value: string`
   * and cannot emit absence, so the default is the empty-string sentinel (the same idiom
   * MemberParts' `NO_POSITION` uses). Round-tripping the toggle off and on therefore leaves
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
  /**
   * One slot, three domains, switched on `field` — amount is a naira number, docType a
   * WfDocType, newCustomer a boolean. The prototype stores all three here and every
   * reader switches on `field`; keeping that shape means the inspector's field select
   * needs no migration step when it flips domains.
   */
  value: number | WfDocType | boolean
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
  /** A human string ('2 days ago'), not a date — the prototype has no clock here. */
  updated: string
  nodes: WfNode[]
}

/** Which workspace's policy set: the firm's, or the in-house company's. */
export type WorkflowMode = 'firm' | 'inhouse'

/**
 * Scope options. The four scopes the seed actually uses are FORCED — the control is a
 * plain select over `policy.scope`, so a seeded scope missing from this list would make
 * that policy's select render blank. The last two are reconstructed.
 */
export const WF_SCOPE_OPTIONS: readonly string[] = [
  'All invoices',
  'Foreign-currency invoices',
  'Document type · B2G',
  'Capex & fixed assets',
  'Consumer invoices (B2C)',
  'Credit notes & adjustments',
]

// ---------------------------------------------------------------------------
// Seed policies
// ---------------------------------------------------------------------------
// Node ids are literal here rather than generated. The prototype's `id()` counter runs
// across BOTH modes from one closure, and its condition children get lower ids than
// their parent (JS evaluates `C(...)`'s arguments before its body). Nothing keys off
// that ordering, so these are simply stable per-mode ids.

const A = (id: string, role: RoleKey, sla: Sla, delegate = false): ApprovalNode => ({ id, type: 'approval', role, sla, delegate })
const N = (id: string, target: string, channel: string): NotifyNode => ({ id, type: 'notify', target, channel })
const AU = (id: string): AutoApproveNode => ({ id, type: 'autoapprove' })
const C = (id: string, op: CondOp, value: number, then: BranchNode[] = [], els: BranchNode[] = []): ConditionNode => ({
  id,
  type: 'condition',
  field: 'amount',
  op,
  value,
  then,
  else: els,
})

export const SEED_FIRM_POLICIES: readonly Policy[] = [
  {
    id: 'polF1',
    name: 'Standard approval policy',
    scope: 'All invoices',
    status: 'published',
    updated: '2 days ago',
    nodes: [
      A('f1n1', 'fin_mgr', '48', true),
      C('f1n2', '>', 250_000_000, [A('f1n3', 'fin_dir', '48')]),
      C('f1n4', '>', 1_000_000_000, [A('f1n5', 'cfo', '72'), N('f1n6', 'Audit Committee', 'Email')]),
      A('f1n7', 'compliance', '24'),
    ],
  },
  {
    id: 'polF2',
    name: 'Cross-border & FX',
    scope: 'Foreign-currency invoices',
    status: 'published',
    updated: '1 week ago',
    nodes: [A('f2n1', 'fin_mgr', '48'), C('f2n2', '>', 500_000_000, [A('f2n3', 'fin_dir', '48')]), A('f2n4', 'compliance', '24')],
  },
  {
    id: 'polF3',
    name: 'Government supply (B2G)',
    scope: 'Document type · B2G',
    status: 'draft',
    updated: '3 weeks ago',
    nodes: [A('f3n1', 'fin_dir', '48'), C('f3n2', '>', 1_000_000_000, [A('f3n3', 'cfo', '72')]), A('f3n4', 'compliance', '24')],
  },
]

export const SEED_INHOUSE_POLICIES: readonly Policy[] = [
  {
    id: 'polH1',
    name: 'Company approval policy',
    scope: 'All invoices',
    status: 'published',
    updated: 'yesterday',
    nodes: [
      A('h1n1', 'line_mgr', '48', true),
      C('h1n2', '>', 500_000_000, [A('h1n3', 'fin_dir', '48')]),
      // The only seeded autoapprove, and the only non-empty else in the whole seed.
      C('h1n4', '>', 1_000_000_000, [A('h1n5', 'cfo', '72')], [AU('h1n6')]),
      N('h1n7', 'Tax Team', 'In-app'),
    ],
  },
  {
    id: 'polH2',
    name: 'Capital expenditure',
    scope: 'Capex & fixed assets',
    status: 'draft',
    updated: '5 days ago',
    nodes: [
      A('h2n1', 'line_mgr', '48'),
      A('h2n2', 'fin_dir', '48'),
      C('h2n3', '>', 1_000_000_000, [A('h2n4', 'cfo', '72'), A('h2n5', 'ceo', '72')]),
    ],
  },
]

/**
 * Policies by workspace, NOT by client. This follows the prototype, where the store is
 * `{firm, inhouse}` and `wfCurrent()` reads `policies[mode]` — switching company in firm
 * mode does not change the policy set. That is also why the Workflows nav item sits in
 * the FIRM-WIDE sidebar group rather than the client-scoped one (Sidebar.tsx), which is
 * a deliberate deviation from the prototype's own `clientScoped` list: a firm-wide
 * dataset under a "CLIENT" scope header would be mislabelled.
 */
export type PolicyStore = Record<WorkflowMode, Policy[]>

export function seedPolicies(): PolicyStore {
  return { firm: clonePolicies(SEED_FIRM_POLICIES), inhouse: clonePolicies(SEED_INHOUSE_POLICIES) }
}

function clonePolicies(list: readonly Policy[]): Policy[] {
  return list.map((p) => ({ ...p, nodes: p.nodes.map(cloneNode) }))
}

function cloneNode(n: WfNode): WfNode {
  return n.type === 'condition' ? { ...n, then: n.then.map((c) => ({ ...c })), else: n.else.map((c) => ({ ...c })) } : { ...n }
}

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
  return { id: newPolicyId(), name: 'Untitled policy', scope: 'All invoices', status: 'draft', updated: 'just now', nodes: [] }
}

/**
 * Every write stamps `updated` AND demotes a published policy back to draft, matching
 * the prototype's single write path (`wfMutate`: `pol.updated = 'just now'; if
 * (pol.status === 'published') pol.status = 'draft'`). Editing a live policy must not
 * leave it labelled PUBLISHED while it no longer matches what was published — Save is
 * what re-publishes it (`publishPolicy`).
 */
function touch(policy: Policy): Policy {
  return { ...policy, updated: 'just now', status: policy.status === 'published' ? 'draft' : policy.status }
}

export function insertNode(policy: Policy, laneKey: LaneKey, index: number, node: WfNode): Policy {
  if (!canDrop(node.type, laneKey)) return policy
  const lane = getLane(policy, laneKey)
  return touch(withLane(policy, laneKey, spliced(lane, index, 0, node)))
}

export function deleteNode(policy: Policy, id: string): Policy {
  const found = findNode(policy, id)
  if (!found) return policy
  const lane = getLane(policy, found.laneKey)
  return touch(withLane(policy, found.laneKey, spliced(lane, found.index, 1)))
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
  return touch(withLane(removed, laneKey, spliced(dstLane, t, 0, found.node)))
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
  return touch(withLane(policy, found.laneKey, spliced(lane, found.index, 1, merged)))
}

export function clearSteps(policy: Policy): Policy {
  return touch({ ...policy, nodes: [] })
}

// Name and scope go through `touch` like every other edit — the prototype routes
// wfSetName/wfSetScope through wfMutate, so renaming a published policy demotes it too.
export function renamePolicy(policy: Policy, name: string): Policy {
  return touch({ ...policy, name })
}

export function rescopePolicy(policy: Policy, scope: string): Policy {
  return touch({ ...policy, scope })
}

/** Saving is what publishes a draft — the prototype has no separate publish action. */
export function publishPolicy(policy: Policy): Policy {
  return { ...policy, status: 'published', updated: 'just now' }
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
 * prototype's `wfRuleText`, including the `'Condition'` fallback for a field this build
 * does not know — unreachable through the typed API, kept so an added field degrades to
 * a label rather than to a wrong amount comparison.
 */
export function ruleText(n: ConditionNode): string {
  if (n.field === 'amount') return `Amount ${opLabel(n.op)} ${fmt(Number(n.value))}`
  if (n.field === 'docType') return `Document type is ${String(n.value)}`
  if (n.field === 'newCustomer') return n.value ? 'Customer is new / unverified' : 'Customer is existing'
  return 'Condition'
}

export function slaText(sla: Sla): string {
  return sla === '0' ? 'no deadline' : `within ${sla}h`
}

// ---------------------------------------------------------------------------
// Scenario simulator
// ---------------------------------------------------------------------------

export type SimContext = { amount: number; docType: WfDocType; newCustomer: boolean }

export const SIM_DEFAULT: SimContext = { amount: 750_000_000, docType: 'B2B', newCustomer: false }

export function evalCondition(n: ConditionNode, ctx: SimContext): boolean {
  if (n.field === 'amount') {
    // `|| 0` mirrors the prototype: a half-typed amount in the inspector must not turn
    // the whole comparison into NaN (which is false for BOTH `>` and `<=`).
    const a = Number(ctx.amount) || 0
    const v = Number(n.value) || 0
    if (n.op === '>') return a > v
    if (n.op === '>=') return a >= v
    if (n.op === '<') return a < v
    return a <= v
  }
  if (n.field === 'docType') return ctx.docType === n.value
  if (n.field === 'newCustomer') return !!ctx.newCustomer === !!n.value
  // Unknown field takes the else lane rather than silently comparing amounts.
  return false
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
