// A person who approved an earlier step of a run is refused the next one, unless they are the only approver.
// Runs over the deployed gateway. One fresh workspace, serial tests.
import { test, expect } from '@playwright/test'
import {
  acceptById,
  createApprovalPolicy,
  createEntity,
  createInvoice,
  createWorkflowRole,
  getAuditLog,
  getInvoice,
  getInvoiceApproval,
  inviteWithToken,
  listInvoices,
  listMine,
  me,
  publishApprovalPolicy,
  putApprovalPolicyDraft,
  rawFetch,
  registerFormAccount,
  registerFresh,
  signInSession,
  staffWorkflowRole,
  subjectOf,
  validateInvoice,
  type ApprovalRun,
  type PendingInvite,
} from './client'
import { freshPolicyName, freshRoleTitle, freshTin } from './fixtures'

// internal/approval/creator.go EarlierApproverRefusedReason.
const EARLIER_APPROVER_REASON = 'You approved an earlier step of this invoice, so another approver must approve this step.'
// internal/approval/creator.go ApprovedAs: the plain approval and the sole-approver repeat.
const APPROVED_AS_APPROVER = 'approver'
const APPROVED_AS_REPEAT = 'repeat_approved'
// internal/approval/decision.go commitDecisionTx audit event.
const EVENT_APPROVED = 'invoice.approval_approved'

const auth = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })

interface Member {
  userId: string
  name: string
  token: string
}

// A fresh account invited as `role` and joined with a name of its own; copied from membership-removal.spec.ts.
async function joined(adminToken: string, tenantId: string, role: 'admin' | 'preparer' | 'reviewer', label: string): Promise<Member> {
  const account = await registerFresh('distinct')
  await inviteWithToken(adminToken, tenantId, account.email, role, 'all')
  const name = `${label} ${crypto.randomUUID().slice(0, 8)}`
  const session = (await signInSession(account.email, account.password)).access_token
  const res = await listMine(session)
  expect(res.status, JSON.stringify(res.body)).toBe(200)
  const [invite] = (res.body as { invitations: PendingInvite[] }).invitations
  const accepted = await acceptById(session, invite.id, name)
  expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
  const token = (await signInSession(account.email, account.password)).access_token
  return { userId: subjectOf(token), name, token }
}

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

test.describe.serial('distinct approvers across steps (API E2E, over the deployed gateway)', () => {
  let admin: string
  let adminId: string
  let tenantId: string
  let preparer: Member
  let entityId: string
  let roleKey: string

  const decide = (token: string, invoiceId: string, body: { decision: 'approved' | 'rejected'; reason?: string }) =>
    rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, { method: 'POST', headers: auth(token), body })

  // The preparer authors the invoice, so the admin is never its creator.
  async function armedInvoice(label: string): Promise<string> {
    const created = await createInvoice(preparer.token, { entity_id: entityId, ...cleanInvoiceFields(`INV-DISTINCT-${label}-${freshTin()}`) })
    const validated = await validateInvoice(preparer.token, created.id)
    expect(validated.status, 'the clean fixture promotes draft -> validated, arming a run').toBe('validated')
    expect((await getInvoiceApproval(admin, created.id)).state).toBe('open')
    return created.id
  }

  const approvedAs = async (invoiceId: string) => {
    const log = await getAuditLog(admin, { invoice_id: invoiceId, event: [EVENT_APPROVED], limit: 100 })
    return log.events
      .slice()
      .sort((a, b) => a.created_at.localeCompare(b.created_at))
      .map((e) => (e.payload as { approved_as?: string }).approved_as)
  }

  test.beforeAll(async () => {
    admin = (await registerFormAccount('distinct-admin')).token
    adminId = subjectOf(admin)
    tenantId = (await me(admin)).tenant.id
    preparer = await joined(admin, tenantId, 'preparer', 'Preparer P')
    entityId = (await createEntity(admin, { name: `Distinct entity ${freshPolicyName()}`, tin: freshTin() })).id
    roleKey = (await createWorkflowRole(admin, { title: freshRoleTitle() })).key
    const policyId = (await createApprovalPolicy(admin, { name: freshPolicyName() })).id
    await putApprovalPolicyDraft(admin, policyId, {
      steps: [
        { kind: 'approval', workflow_role_key: roleKey },
        { kind: 'approval', workflow_role_key: roleKey },
      ],
    })
    await publishApprovalPolicy(admin, policyId)
  })

  test('a sole approver approves both steps and the audit says repeat_approved', async () => {
    await staffWorkflowRole(admin, roleKey, [adminId])
    const invoiceId = await armedInvoice('SOLE')

    const first = await decide(admin, invoiceId, { decision: 'approved' })
    expect(first.status, JSON.stringify(first.body)).toBe(200)
    expect((first.body as ApprovalRun).state).toBe('open')
    const second = await decide(admin, invoiceId, { decision: 'approved' })
    expect(second.status, JSON.stringify(second.body)).toBe(200)
    expect((second.body as ApprovalRun).state).toBe('approved')

    expect(await approvedAs(invoiceId)).toEqual([APPROVED_AS_APPROVER, APPROVED_AS_REPEAT])
  })

  test('an earlier approver is refused on the next step, on every door', async () => {
    const reviewer = await joined(admin, tenantId, 'reviewer', 'Reviewer V')
    await staffWorkflowRole(admin, roleKey, [adminId, reviewer.userId])
    const invoiceId = await armedInvoice('REFUSED')

    const first = await decide(admin, invoiceId, { decision: 'approved' })
    expect(first.status, JSON.stringify(first.body)).toBe(200)

    const detail = await getInvoice(admin, invoiceId)
    expect(detail.can_approve, 'detail: an earlier approver cannot approve').toBe(false)
    expect(detail.approve_blocked_by_creator, 'detail wire reads refused').toBe(true)
    expect(detail.approve_blocked_reason).toBe(EARLIER_APPROVER_REASON)

    const row = (await listInvoices(admin, { entity_id: entityId, awaiting_approval: true })).invoices.find((i) => i.id === invoiceId)
    expect(row, 'the invoice is in the awaiting-approval list').toBeDefined()
    expect(row!.can_approve, 'list: an earlier approver cannot approve').toBe(false)
    expect(row!.approve_blocked_by_creator, 'list wire reads refused').toBe(true)
    expect(row!.approve_blocked_reason).toBe(EARLIER_APPROVER_REASON)

    const refused = await decide(admin, invoiceId, { decision: 'approved' })
    expect(refused.status, JSON.stringify(refused.body)).toBe(403)
    expect((refused.body as { error: string }).error).toBe(EARLIER_APPROVER_REASON)

    const approved = await decide(reviewer.token, invoiceId, { decision: 'approved' })
    expect(approved.status, JSON.stringify(approved.body)).toBe(200)
    expect((approved.body as ApprovalRun).state).toBe('approved')
    expect(await approvedAs(invoiceId)).toEqual([APPROVED_AS_APPROVER, APPROVED_AS_APPROVER])
  })
})
