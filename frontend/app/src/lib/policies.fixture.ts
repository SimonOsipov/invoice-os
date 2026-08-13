// The seed policy trees. TEST-ONLY: no production module imports this file — App.tsx
// fetches the tenant's real policies. They stay as a fixture because the roles, members
// and workflows specs are all written against these five trees.

import type { ApprovalNode, AutoApproveNode, BranchNode, CondOp, ConditionNode, NotifyNode, Policy, RoleKey, Sla, WfNode } from './workflows'

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
    // Only polF1 and polH1 hold an active version — a second per store would model a tenant
    // the server cannot produce. Policy.activeVersion states the constraint.
    version: 1,
    activeVersion: 1,
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
    version: 1,
    activeVersion: null,
    nodes: [A('f2n1', 'fin_mgr', '48'), C('f2n2', '>', 500_000_000, [A('f2n3', 'fin_dir', '48')]), A('f2n4', 'compliance', '24')],
  },
  {
    id: 'polF3',
    name: 'Government supply (B2G)',
    scope: 'Document type · B2G',
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [A('f3n1', 'fin_dir', '48'), C('f3n2', '>', 1_000_000_000, [A('f3n3', 'cfo', '72')]), A('f3n4', 'compliance', '24')],
  },
]

export const SEED_INHOUSE_POLICIES: readonly Policy[] = [
  {
    id: 'polH1',
    name: 'Company approval policy',
    scope: 'All invoices',
    status: 'published',
    version: 1,
    activeVersion: 1,
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
    version: 1,
    activeVersion: null,
    nodes: [
      A('h2n1', 'line_mgr', '48'),
      A('h2n2', 'fin_dir', '48'),
      C('h2n3', '>', 1_000_000_000, [A('h2n4', 'cfo', '72'), A('h2n5', 'ceo', '72')]),
    ],
  },
]

/** The prototype's two-workspace shape, kept so the specs can name a firm and an in-house tree. */
export type PolicyStore = Record<'firm' | 'inhouse', Policy[]>

export function seedPolicies(): PolicyStore {
  return { firm: clonePolicies(SEED_FIRM_POLICIES), inhouse: clonePolicies(SEED_INHOUSE_POLICIES) }
}

function clonePolicies(list: readonly Policy[]): Policy[] {
  return list.map((p) => ({ ...p, nodes: p.nodes.map(cloneNode) }))
}

function cloneNode(n: WfNode): WfNode {
  return n.type === 'condition' ? { ...n, then: n.then.map((c) => ({ ...c })), else: n.else.map((c) => ({ ...c })) } : { ...n }
}
