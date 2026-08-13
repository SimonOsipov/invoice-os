// The approval-policy wire and its pure mappers.
//
// STUB — the type declarations are final; every function body still throws, which is what
// policies.test.ts is red against. Delete the `void` lines when you implement one.

import type { Policy, WfNode } from './workflows'

// ---------------------------------------------------------------------------
// Wire types, mirrored field-for-field from internal/approval/policy.go:17-93
// ---------------------------------------------------------------------------
// No omitempty on any response field, and Step/Policy both carry a value-receiver
// MarshalJSON substituting [] for a nil lane — so `steps`, `versions`, `then` and `else`
// are always arrays, never null. Key sets: Policy 8, Step 10, PolicyVersion 5.
//
// `kind` and `status` are `string` here, not the narrowed unions e2e/api/client.ts:657,682
// uses. That mirror and this one differ on purpose: a value the server states is carried
// verbatim and cast at the projection, never defaulted (the members.ts:509,551 precedent).

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

export function nodesFromSteps(steps: readonly PolicyStepWire[]): WfNode[] {
  void steps
  throw new Error('not implemented')
}

export function stepInputsFromNodes(nodes: readonly WfNode[]): StepInputWire[] {
  void nodes
  throw new Error('not implemented')
}

export function toPolicy(wire: PolicyWire): Policy {
  void wire
  throw new Error('not implemented')
}

// ---------------------------------------------------------------------------
// Derived standing
// ---------------------------------------------------------------------------

/** The list row's version line — 'v1 in force', 'v3 in force · v4 draft', 'Never published'. */
export function policyStanding(policy: Policy): string {
  void policy
  throw new Error('not implemented')
}

/** The policy holding the tenant's single active slot, excluding `selfId`. */
export function policyInForce(list: readonly Policy[], selfId: string): Policy | null {
  void list
  void selfId
  throw new Error('not implemented')
}
