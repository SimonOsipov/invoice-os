// The approval-policy wire and its pure mappers.

import type { AuthedFetch } from './portfolio'
import type { BranchNode, CondOp, Policy, PolicyStatus, Sla, WfNode } from './workflows'

// ---------------------------------------------------------------------------
// Wire types, mirrored field-for-field from internal/approval/policy.go:17-93
// ---------------------------------------------------------------------------
// The marshalling facts these types rest on (no omitempty, [] never null, the fixed key
// counts) are stated at that source and in the sibling mirror e2e/api/client.ts. Not
// restated here — three copies of a countable fact is three places for it to rot.
//
// One deliberate difference from that mirror: `kind` and `status` are `string`, not its
// narrowed unions. A value the server states is carried verbatim and cast at the
// projection, never defaulted (the members.ts:508,551 precedent).

export type PolicyStepWire = {
  /** Server-minted uuid, re-minted on EVERY PUT draft — never assert a value across a save. */
  id: string
  kind: string
  workflow_role_key: string | null
  /** null = no deadline, which the SPA carries as the `'0'` sentinel. */
  sla_hours: number | null
  cond_op: string | null
  /** numeric(14,2) read via ::text, so the scale survives ('0.00', not '0'). */
  cond_amount: string | null
  notify_target: string | null
  notify_channel: string | null
  then: PolicyStepWire[]
  else: PolicyStepWire[]
}

export type PolicyVersionWire = {
  version: number
  sealed: boolean
  /** TENANT-wide slot (`approval_policy_versions_one_active ON (tenant_id)`). */
  is_active: boolean
  published_at: string | null
  published_by: string | null
}

export type PolicyWire = {
  id: string
  name: string
  scope: string
  status: string
  /** The version `steps` belongs to = the HIGHEST version, not necessarily the active one. */
  version: number
  sealed: boolean
  steps: PolicyStepWire[]
  /** ORDER BY version DESC, newest first. */
  versions: PolicyVersionWire[]
}

export type PoliciesResponseWire = { approval_policies: PolicyWire[] }

/** stepInput (policy.go:83-93) declares NO id field — a client-supplied one is dropped at decode. */
export type StepInputWire = {
  kind: string
  workflow_role_key?: string | null
  sla_hours?: number | null
  cond_op?: string | null
  cond_amount?: string | null
  notify_target?: string | null
  notify_channel?: string | null
  then?: StepInputWire[]
  else?: StepInputWire[]
}

// ---------------------------------------------------------------------------
// Pure mappers
// ---------------------------------------------------------------------------
// There is NO server column for delegation: `delegate`/`delegateTo` exist only in the
// builder's local node shape. Do not add them to a wire type to close an apparent gap —
// the server would drop them at decode and the round-trip would silently lose the setting.

/** null hours = no deadline, which the builder carries as the `'0'` sentinel, never as 0 hours. */
function slaFromHours(hours: number | null): Sla {
  return hours === null ? '0' : String(hours)
}

function hoursFromSla(sla: Sla): number | null {
  return sla === '0' ? null : Number(sla)
}

// A node carries the SERVER's step id, not a locally minted one, so the re-minting on every
// PUT draft is visible rather than hidden behind an alias — which is why selection clears on
// save. Ids are stable within a session, only between saves.
export function nodesFromSteps(steps: readonly PolicyStepWire[]): WfNode[] {
  const out: WfNode[] = []
  for (const s of steps) {
    if (s.kind === 'condition') {
      out.push({
        id: s.id,
        type: 'condition',
        // The column holds one domain, so this is not a choice the wire can carry.
        field: 'amount',
        // Both are cast/read verbatim: the server refuses a condition missing either.
        op: s.cond_op as CondOp,
        value: Number(s.cond_amount),
        then: branchNodes(s.then),
        else: branchNodes(s.else),
      })
      continue
    }
    const node = branchNode(s)
    if (node) out.push(node)
  }
  return out
}

function branchNodes(steps: readonly PolicyStepWire[]): BranchNode[] {
  const out: BranchNode[] = []
  for (const s of steps) {
    const node = branchNode(s)
    if (node) out.push(node)
  }
  return out
}

/**
 * Null for a kind outside the four, which `policyStepKinds` closes server-side — so no
 * response can reach this today. If a fifth kind is ever added, note the cost first: the
 * step is dropped on read, and `PUT .../draft` sends the WHOLE tree, so the next save
 * DELETES it. Widen `WfNode` in the same change that widens the server's vocabulary.
 */
function branchNode(s: PolicyStepWire): BranchNode | null {
  // `delegateTo` is left OFF, not set to undefined: an own key reads as a stored choice,
  // and `''` is already the "anyone with the role" default.
  if (s.kind === 'approval') {
    return { id: s.id, type: 'approval', role: s.workflow_role_key ?? '', sla: slaFromHours(s.sla_hours), delegate: false }
  }
  if (s.kind === 'notify') return { id: s.id, type: 'notify', target: s.notify_target ?? '', channel: s.notify_channel ?? '' }
  if (s.kind === 'autoapprove') return { id: s.id, type: 'autoapprove' }
  return null
}

export function stepInputsFromNodes(nodes: readonly WfNode[]): StepInputWire[] {
  return nodes.map((n) => stepInput(n))
}

function stepInput(n: WfNode): StepInputWire {
  switch (n.type) {
    case 'approval':
      // `delegate`/`delegateTo` are dropped here — see the note above.
      return { kind: 'approval', workflow_role_key: n.role, sla_hours: hoursFromSla(n.sla) }
    case 'notify':
      return { kind: 'notify', notify_target: n.target, notify_channel: n.channel }
    case 'autoapprove':
      return { kind: 'autoapprove' }
    case 'condition':
      return {
        kind: 'condition',
        cond_op: n.op,
        // numeric(14,2) takes decimal TEXT. A non-amount `field` stringifies to something
        // the column refuses, which is a loud 400 rather than a wrong stored amount.
        cond_amount: String(n.value),
        then: n.then.map((c) => stepInput(c)),
        else: n.else.map((c) => stepInput(c)),
      }
  }
}

export function toPolicy(wire: PolicyWire): Policy {
  return {
    id: wire.id,
    name: wire.name,
    scope: wire.scope,
    status: wire.status as PolicyStatus,
    // No wire source for this; the field is deleted in APPR-09-03.
    updated: '',
    version: wire.version,
    activeVersion: wire.versions.find((v) => v.is_active)?.version ?? null,
    nodes: nodesFromSteps(wire.steps),
  }
}

// ---------------------------------------------------------------------------
// Derived standing
// ---------------------------------------------------------------------------

/** The list row's version line — 'v1 in force', 'v3 in force · v4 draft', 'Never published'. */
export function policyStanding(policy: Policy): string {
  const { version, activeVersion, status } = policy
  if (activeVersion === null) {
    // A 'published' status means the top version is sealed, so it was in force once and
    // then lost the tenant's slot. Only an unsealed v1 has never held it.
    return version === 1 && status === 'draft' ? 'Never published' : 'Not in force'
  }
  if (version === activeVersion) return `v${activeVersion} in force`
  return `v${activeVersion} in force · v${version} draft`
}

/** The policy holding the tenant's single active slot, excluding `selfId`. */
export function policyInForce(list: readonly Policy[], selfId: string): Policy | null {
  return list.find((p) => p.id !== selfId && p.activeVersion !== null) ?? null
}

// ---------------------------------------------------------------------------
// Wire
// ---------------------------------------------------------------------------
// Stubs (APPR-09-02, Stage 2.5). The specs in policies.test.ts are the contract; Stage 3
// replaces every body below. Shape follows roles.ts:342-376 — `base` a parameter, no
// try/catch anywhere, so ApiError reaches the UI unreshaped.

export async function listApprovalPolicies(_f: AuthedFetch, _base: string): Promise<Policy[]> {
  throw new Error('not implemented')
}

export async function createApprovalPolicy(_f: AuthedFetch, _base: string, _name: string): Promise<Policy> {
  throw new Error('not implemented')
}

export async function putApprovalPolicyDraft(_f: AuthedFetch, _base: string, _id: string, _next: Policy): Promise<Policy> {
  throw new Error('not implemented')
}

export async function publishApprovalPolicy(_f: AuthedFetch, _base: string, _id: string): Promise<Policy> {
  throw new Error('not implemented')
}

/** The DELETE answer is INERT (status 'draft', version 0, steps []) — discard it, never patch a row from it. */
export async function deleteApprovalPolicy(_f: AuthedFetch, _base: string, _id: string): Promise<void> {
  throw new Error('not implemented')
}
