// A step delegate decides a published step, over the deployed gateway (DELEG-01). One workspace, serial tests.
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
  listMine,
  me,
  publishApprovalPolicy,
  putApprovalPolicyDraft,
  rawFetch,
  registerFormAccount,
  registerFresh,
  setMemberRole,
  signInSession,
  subjectOf,
  validateInvoice,
  type ApprovalRun,
  type PendingInvite,
} from './client'
import { freshPolicyName, freshRoleTitle, freshTin } from './fixtures'

// internal/approval/handlers.go decisionStatusForErr: ErrNotRoleHolder.
const NOT_ROLE_HOLDER = 'you do not hold the workflow role this step is waiting on'
// internal/approval/policy.go policyStatusForErr: delegateIneligible.
const guardSentence = (name: string, step: string) => `${name} can no longer be a delegate on ${step}. Pick another delegate or switch delegation off.`
// internal/approval/decision.go commitDecisionTx audit events.
const EVENT_APPROVED = 'invoice.approval_approved'
const EVENT_REJECTED = 'invoice.approval_rejected'

const auth = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })

interface Member {
  userId: string
  name: string
  token: string
}

// A fresh account invited as `role` and joined with a name of its own; copied from membership-removal.spec.ts.
async function joined(adminToken: string, tenantId: string, role: 'admin' | 'preparer' | 'reviewer', label: string): Promise<Member> {
  const account = await registerFresh('delegation')
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

test.describe.serial('step delegation (API E2E, over the deployed gateway)', () => {
  let admin: string
  let d: Member
  let r: Member
  let entityId: string
  let roleKey: string
  let roleTitle: string
  let policyId: string

  const decide = (token: string, invoiceId: string, body: { decision: 'approved' | 'rejected'; reason?: string }) =>
    rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, { method: 'POST', headers: auth(token), body })

  // A validated invoice arms a run on the policy in force.
  async function armedInvoice(label: string): Promise<string> {
    const created = await createInvoice(admin, { entity_id: entityId, ...cleanInvoiceFields(`INV-DELEG-${label}-${freshTin()}`) })
    const validated = await validateInvoice(admin, created.id)
    expect(validated.status, 'the clean fixture promotes draft -> validated, arming a run').toBe('validated')
    expect((await getInvoiceApproval(admin, created.id)).state).toBe('open')
    return created.id
  }

  const delegateStep = (delegate: string | null) => [
    { kind: 'approval', workflow_role_key: roleKey, allow_delegation: true, delegate_user_id: delegate },
  ]

  test.beforeAll(async () => {
    admin = (await registerFormAccount('delegation-admin')).token
    const tenantId = (await me(admin)).tenant.id
    d = await joined(admin, tenantId, 'reviewer', 'Delegate D')
    r = await joined(admin, tenantId, 'reviewer', 'Bystander R')
    entityId = (await createEntity(admin, { name: `Delegation entity ${freshPolicyName()}`, tin: freshTin() })).id
    roleTitle = freshRoleTitle()
    roleKey = (await createWorkflowRole(admin, { title: roleTitle })).key
    policyId = (await createApprovalPolicy(admin, { name: freshPolicyName() })).id
  })

  test('a named delegate approves a published step; a bystander is refused', async () => {
    await putApprovalPolicyDraft(admin, policyId, { steps: delegateStep(d.userId) })
    await publishApprovalPolicy(admin, policyId)
    const invoiceId = await armedInvoice('A')

    const refused = await decide(r.token, invoiceId, { decision: 'approved' })
    expect(refused.status, JSON.stringify(refused.body)).toBe(403)
    expect((refused.body as { error: string }).error).toBe(NOT_ROLE_HOLDER)
    expect((await getInvoice(d.token, invoiceId)).can_approve, 'the delegate may approve').toBe(true)

    const approved = await decide(d.token, invoiceId, { decision: 'approved' })
    expect(approved.status, JSON.stringify(approved.body)).toBe(200)
    const run = approved.body as ApprovalRun
    expect(run.state).toBe('approved')
    expect(run.decisions[0].actor).toBe(d.userId)
    expect(run.decisions[0].as_delegate).toBe(true)

    const log = await getAuditLog(admin, { invoice_id: invoiceId, event: [EVENT_APPROVED], limit: 100 })
    expect(log.events, 'one approval row').toHaveLength(1)
    expect(log.events[0].actor).toBe(d.userId)
    expect(log.events[0].payload).toMatchObject({ as_delegate: true, acted_for_role_key: roleKey, acted_for_role_title: roleTitle })
  })

  test('anyone with the Admin or Reviewer role can reject', async () => {
    await putApprovalPolicyDraft(admin, policyId, { steps: delegateStep(null) })
    await publishApprovalPolicy(admin, policyId)
    const invoiceId = await armedInvoice('B')

    const rejected = await decide(r.token, invoiceId, { decision: 'rejected', reason: 'Wrong buyer' })
    expect(rejected.status, JSON.stringify(rejected.body)).toBe(200)
    const run = rejected.body as ApprovalRun
    expect(run.state).toBe('rejected')
    expect(run.decisions[0].actor).toBe(r.userId)
    expect(run.decisions[0].as_delegate).toBe(true)

    const log = await getAuditLog(admin, { invoice_id: invoiceId, event: [EVENT_REJECTED], limit: 100 })
    expect(log.events).toHaveLength(1)
    expect(log.events[0].payload).toMatchObject({ as_delegate: true, acted_for_role_title: roleTitle })
  })

  test('publish refuses a delegate who can no longer be one', async () => {
    const demoted = await setMemberRole(admin, d.userId, 'preparer')
    expect(demoted.status, JSON.stringify(demoted.body)).toBe(200)

    await putApprovalPolicyDraft(admin, policyId, { steps: delegateStep(d.userId) })
    const refused = await rawFetch(`/api/invoice/v1/approval-policies/${policyId}/publish`, { method: 'POST', headers: auth(admin) })
    expect(refused.status, JSON.stringify(refused.body)).toBe(409)
    expect((refused.body as { error: string }).error).toBe(guardSentence(d.name, roleTitle))

    await putApprovalPolicyDraft(admin, policyId, { steps: delegateStep(null) })
    const published = await rawFetch(`/api/invoice/v1/approval-policies/${policyId}/publish`, { method: 'POST', headers: auth(admin) })
    expect(published.status, JSON.stringify(published.body)).toBe(200)
  })
})
