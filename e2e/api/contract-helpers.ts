import { expect } from '@playwright/test'
import {
  listApprovalPolicies,
  publishApprovalPolicy,
  putApprovalPolicyDraft,
  type ApprovalPoliciesResponse,
  type ApprovalPolicy,
  type ApprovalPolicyDraftInput,
  type ApprovalStep,
  type ApprovalStepInput,
} from './client'

// The raw result shape rawFetch() returns and every contract assertion consumes.
export type RawResult = { status: number; body: unknown }

// assertErrorEnvelope(): the shared error-path assertion — a rejected request must
// carry the EXPECTED status and a body that is EXACTLY the shared envelope shape
// {error: <string>}: a plain object with one key, `error`, whose value is a string.
// Extracted VERBATIM from the five contract specs (M4-16-01) — behaviour + messages unchanged.
export function assertErrorEnvelope(result: RawResult, expectedStatus: number, label: string): void {
  expect(result.status, `${label}: expected HTTP ${expectedStatus}`).toBe(expectedStatus)
  expect(result.body, `${label}: expected a parsed JSON object body`).toBeInstanceOf(Object)
  const body = result.body as Record<string, unknown>
  expect(Object.keys(body), `${label}: expected exactly one key, 'error'`).toEqual(['error'])
  expect(typeof body.error, `${label}: expected body.error to be a string`).toBe('string')
}

// assertUnauthorizedEnvelope(): the 401 specialization auth-contract.spec.ts used
// (its message was "expected HTTP 401" — identical to delegating with expectedStatus=401).
export function assertUnauthorizedEnvelope(result: RawResult, label: string): void {
  assertErrorEnvelope(result, 401, label)
}

// The firm tenant's seeded active policy (internal/demopolicy's firmPlan).
const FIRM_POLICY_NAME = 'Standard approval policy'

interface ApprovalPolicyTransport {
  list: (token: string) => Promise<ApprovalPoliciesResponse>
  putDraft: (token: string, id: string, body: ApprovalPolicyDraftInput) => Promise<ApprovalPolicy>
  publish: (token: string, id: string) => Promise<ApprovalPolicy>
}

// mapApprovalSteps(): a GET response tree can't be PUT back unchanged -- ApprovalStep
// carries a server-minted `id`, ApprovalStepInput has none. Recurses into then/else so a
// nested lane is never silently dropped.
export function mapApprovalSteps(steps: ApprovalStep[]): ApprovalStepInput[] {
  return steps.map((s) => ({
    kind: s.kind,
    workflow_role_key: s.workflow_role_key,
    sla_hours: s.sla_hours,
    cond_op: s.cond_op,
    cond_amount: s.cond_amount,
    notify_target: s.notify_target,
    notify_channel: s.notify_channel,
    then: mapApprovalSteps(s.then),
    else: mapApprovalSteps(s.else),
  }))
}

// isGovernedTree(): an empty or truncated tree publishes clean and reads governed while
// every invoice auto-approves (engine.go arms a run already closed). fin_mgr first,
// compliance last, per firmPlan.
function isGovernedTree(steps: ApprovalStep[]): boolean {
  if (steps.length === 0) return false
  return steps[0].workflow_role_key === 'fin_mgr' && steps[steps.length - 1].workflow_role_key === 'compliance'
}

// ensureFirmPolicyActive(): restores the firm tenant's seeded policy to the tenant's one
// active slot. A bare re-publish 409s -- the seeded version is already sealed -- so this is
// PUT draft (of the seeded policy's OWN tree, read fresh, never hard-coded) then publish.
//
// Convergent and idempotent: safe to call from a crashed-run self-heal. The mutation half
// is swallowed -- a failed PUT/publish is fine if the tenant already reads converged (e.g.
// a retry's second call). The verification half is an independent read and is the oracle:
// it throws, naming every active version found, rather than trust the mutation's own
// (possibly stale) response.
export async function ensureFirmPolicyActive(
  token: string,
  transport: ApprovalPolicyTransport = { list: listApprovalPolicies, putDraft: putApprovalPolicyDraft, publish: publishApprovalPolicy },
): Promise<ApprovalPolicy> {
  const { approval_policies: before } = await transport.list(token)
  const named = before.filter((p) => p.name === FIRM_POLICY_NAME)
  if (named.length === 0) {
    throw new Error(`ensureFirmPolicyActive: no live policy named "${FIRM_POLICY_NAME}"`)
  }
  if (named.length > 1) {
    throw new Error(`ensureFirmPolicyActive: ${named.length} live policies named "${FIRM_POLICY_NAME}", expected exactly one`)
  }
  const source = named[0]
  if (!isGovernedTree(source.steps)) {
    throw new Error(
      `ensureFirmPolicyActive: seeded policy "${FIRM_POLICY_NAME}" (${source.id}) tree is empty or truncated -- restoring it would leave the firm tenant ungated`,
    )
  }

  try {
    await transport.putDraft(token, source.id, { steps: mapApprovalSteps(source.steps) })
    await transport.publish(token, source.id)
  } catch {
    // cleanup half -- a failed mutation is fine as long as the read below shows convergence
  }

  const { approval_policies: after } = await transport.list(token)
  const active = after.filter((p) => p.versions.some((v) => v.is_active))
  if (active.length !== 1 || active[0].name !== FIRM_POLICY_NAME) {
    const found = active.map((p) => p.name).join(', ') || 'none'
    throw new Error(
      `ensureFirmPolicyActive: firm tenant did not converge on exactly one active "${FIRM_POLICY_NAME}" -- active versions found: ${found} (${active.length})`,
    )
  }

  const result = active[0]
  if (!isGovernedTree(result.steps)) {
    throw new Error(
      `ensureFirmPolicyActive: restored active policy "${FIRM_POLICY_NAME}" (${result.id}) tree is empty or truncated -- firm tenant is ungated`,
    )
  }
  return result
}
