// The reconciliation sweep over the deployed fleet: a validated invoice whose approval step has no staffed role holder is flagged as drift.
import { test, expect } from '@playwright/test'
import {
  login,
  createEntity,
  createInvoice,
  validateInvoice,
  editInvoice,
  createWorkflowRole,
  deleteWorkflowRole,
  createApprovalPolicy,
  putApprovalPolicyDraft,
  publishApprovalPolicy,
  deleteApprovalPolicy,
  listApprovalPolicies,
  reorderApprovalPolicies,
  getAuditLog,
  PERSONAS,
  type AuditEvent,
} from './client'
import { freshTin, freshRoleTitle, freshPolicyName } from './fixtures'

// internal/reconciliation/reconciliation.go recordDriftAudit, ApprovalBlockedUnstaffed
const DRIFT_EVENT = 'reconciliation.drift_detected'
const APPROVAL_BLOCKED_UNSTAFFED = 'approval_blocked_unstaffed'

// sweep.go defaultReconcileInterval is 5 minutes; previews leave RECONCILE_INTERVAL unset.
// ceiling: one tick (5 min) plus 60 s; a skipped leader tick or a sweep over 60 s fails this test. Widen SWEEP_WAIT_MS if it does.
const SWEEP_WAIT_MS = 360_000

// Own copy (no cross-spec imports).
function cleanInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// Own copy (no cross-spec imports): puts `ids` at the head of the workspace order.
async function moveToFront(tok: string, ids: string[]): Promise<void> {
  const { approval_policies } = await listApprovalPolicies(tok)
  const rest = approval_policies.map((p) => p.id).filter((id) => !ids.includes(id))
  await reorderApprovalPolicies(tok, [...ids, ...rest])
}

test.describe('reconciliation sweep (API E2E, over the deployed gateway)', () => {
  // A retry repeats a timed wait: it adds up to 8 min and no signal.
  test.describe.configure({ retries: 0 })

  test('reconciliation: the sweep flags a validated invoice waiting on an unstaffed role as approval_blocked_unstaffed', async () => {
    test.setTimeout(480_000)
    const token = await login(PERSONAS.A)

    const tin = freshTin()
    const entity = await createEntity(token, { name: `Zz RECON ${tin}`, tin })
    let roleKey: string | undefined
    let policyId: string | undefined
    let invoiceId: string | undefined
    let pollError: unknown
    try {
      // A role with no members; PublishPolicy refuses only a deleted role or an inactive delegate.
      const role = await createWorkflowRole(token, { title: freshRoleTitle() })
      roleKey = role.key
      const policy = await createApprovalPolicy(token, { name: freshPolicyName(), scopes: { entity_ids: [entity.id] } })
      policyId = policy.id
      await putApprovalPolicyDraft(token, policy.id, { steps: [{ kind: 'approval', workflow_role_key: role.key }] })
      await moveToFront(token, [policy.id])
      await publishApprovalPolicy(token, policy.id)

      const number = `INV-RECON-${tin}`
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(number) })
      invoiceId = created.id
      expect((await validateInvoice(token, created.id)).status).toBe('validated')

      let drift: AuditEvent | undefined
      await expect
        .poll(
          async () => {
            const log = await getAuditLog(token, { invoice_id: created.id, event: [DRIFT_EVENT] })
            drift = log.events.find((e) => (e.payload as { drift_kind?: string }).drift_kind === APPROVAL_BLOCKED_UNSTAFFED)
            return drift !== undefined
          },
          {
            intervals: [10_000],
            timeout: SWEEP_WAIT_MS,
            message:
              'no approval_blocked_unstaffed row within 6 min: is the worker sweep running, and is RECONCILE_INTERVAL unset on this preview?',
          },
        )
        .toBe(true)

      expect(drift!.event).toBe(DRIFT_EVENT)
      expect(drift!.actor).toBe('system')
      expect(drift!.payload).toMatchObject({
        invoice_id: created.id,
        invoice_number: number,
        drift_kind: APPROVAL_BLOCKED_UNSTAFFED,
      })
      // No job row, so the key is absent (driftPayload).
      expect(drift!.payload).not.toHaveProperty('submission_job_id')
    } catch (err) {
      pollError = err
    } finally {
      // Cleanup errors never replace the poll's error. The edit demotes the invoice to draft and Store.Edit cancels its live run.
      const steps: (() => Promise<unknown>)[] = []
      if (invoiceId) steps.push(() => editInvoice(token, invoiceId!, { line_items: [{ description: 'Changed', quantity: '1', unit_price: '1000', line_total: '1000' }] }))
      if (policyId) steps.push(() => deleteApprovalPolicy(token, policyId!))
      if (roleKey) steps.push(() => deleteWorkflowRole(token, roleKey!))
      for (const step of steps) {
        try {
          await step()
        } catch (err) {
          pollError ??= err
        }
      }
    }
    if (pollError) throw pollError
  })
})
