// A member deletes their own account over the deployed gateway: leave, hand on, delete the workspace.
// Every test builds a fresh workspace, so no row is shared across tests or retries.
import { test, expect } from '@playwright/test'
import {
  acceptById,
  cancelDeletion,
  deleteMyAccount,
  getAuditLog,
  inviteWithToken,
  listMine,
  me,
  memberships,
  previewAccountDeletion,
  registerFormAccount,
  registerFresh,
  scheduleDeletion,
  signInSession,
  subjectOf,
  type PendingInvite,
} from './client'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'

// internal/platform/db/tenant.go NotActiveMemberMessage.
const NOT_ACTIVE = 'your membership in this workspace is not active'
// internal/tenancy/tenancy.go statusForErr: ErrDeletionOutcomeChanged and ErrWorkspaceNameMismatch.
const OUTCOME_CHANGED = 'the workspace changed; review the page and confirm again'
const NAME_MISMATCH = 'the workspace name does not match'
// internal/actor/actor.go RemovedName.
const DELETED_SUFFIX = ' (deleted)'
// internal/tenancy/account_deletion.go audit.Record event names.
const EVENT_DELETED = 'membership.account_deleted'
const EVENT_HANDED_ON = 'membership.admin_handed_on'
// internal/tenancy/account_deletion.go AccountDeletionOutcome constants.
const LEAVE = 'leave'
const HAND_ON = 'hand_on'
const DELETE_WORKSPACE = 'delete_workspace'
// internal/tenancy/workspace_deletion.go: the grace period.
const GRACE_MS = 30 * 86_400_000
const SLACK_MS = 2 * 60_000

const uniqueName = (label: string): string => `${label} ${crypto.randomUUID().slice(0, 8)}`
const errorOf = (res: RawResult): string => (res.body as { error: string }).error
const outcomeOf = (res: RawResult): unknown => (res.body as { outcome: unknown }).outcome

interface Workspace {
  adminToken: string
  tenantId: string
  adminId: string
  workspaceName: string
  email: string
  password: string
}

async function workspace(prefix: string): Promise<Workspace> {
  const reg = await registerFormAccount(prefix)
  return { adminToken: reg.token, tenantId: (await me(reg.token)).tenant.id, adminId: subjectOf(reg.token), workspaceName: reg.account.workspaceName, email: reg.account.email, password: reg.account.password }
}

interface Member {
  email: string
  password: string
  userId: string
  name: string
  token: string
}

// A fresh account invited as `role` and joined with a name of its own; `token` carries the workspace claim.
async function joined(ws: Workspace, role: 'admin' | 'preparer', label: string): Promise<Member> {
  const account = await registerFresh('selfdelete')
  await inviteWithToken(ws.adminToken, ws.tenantId, account.email, role)
  const name = uniqueName(label)
  const session = (await signInSession(account.email, account.password)).access_token
  const { invitations } = (await listMine(session)).body as { invitations: PendingInvite[] }
  const accepted = await acceptById(session, invitations[0].id, name)
  expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
  const token = (await signInSession(account.email, account.password)).access_token
  return { ...account, userId: subjectOf(token), name, token }
}

test.describe.serial('account deletion (API E2E, over the deployed gateway)', () => {
  test('account deletion: a member leaves at once and is erased', async () => {
    const ws = await workspace('selfdelete-leave')
    const preparer = await joined(ws, 'preparer', 'Leaver')

    const preview = await previewAccountDeletion(preparer.token)
    expect(preview.status, JSON.stringify(preview.body)).toBe(200)
    expect(outcomeOf(preview)).toBe(LEAVE)

    const res = await deleteMyAccount(preparer.token, { outcome: LEAVE })
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    expect(res.body).toEqual({ outcome: LEAVE })

    const again = await deleteMyAccount(preparer.token, { outcome: LEAVE })
    assertErrorEnvelope(again, 403, 'a second delete with the same token')
    expect(errorOf(again)).toBe(NOT_ACTIVE)

    const row = (await memberships(ws.adminToken)).memberships.find((m) => m.user_id === preparer.userId)
    expect(row?.status).toBe('removed')
    expect(row?.email, 'the address is cleared').toBeNull()

    const log = await getAuditLog(ws.adminToken, { event: [EVENT_DELETED], limit: 100 })
    const own = log.events.filter((e) => e.actor === preparer.userId)
    expect(own, 'one deleted row for the preparer').toHaveLength(1)
    expect(own[0].actor_name).toBe(`${preparer.name}${DELETED_SUFFIX}`)

    // The preview worker runs the erase job against the preview GoTrue. A timeout here is a DEL-03 wiring defect.
    await expect
      .poll(async () => signInSession(preparer.email, preparer.password).then(() => 'signed-in', () => 'rejected'), { intervals: [5_000], timeout: 120_000 })
      .toBe('rejected')
  })

  test('account deletion: the last admin hands admin to the first joined member', async () => {
    const ws = await workspace('selfdelete-handon')
    const x = await joined(ws, 'preparer', 'Heir')
    const y = await joined(ws, 'preparer', 'Other')

    const preview = await previewAccountDeletion(ws.adminToken)
    expect(preview.status, JSON.stringify(preview.body)).toBe(200)
    expect(preview.body).toEqual({ outcome: HAND_ON, heir_name: x.name })

    const res = await deleteMyAccount(ws.adminToken, { outcome: HAND_ON })
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    expect(outcomeOf(res)).toBe(HAND_ON)

    expect((await me(x.token)).user.role).toBe('admin')
    expect((await me(y.token)).user.role).not.toBe('admin')

    const fresh = (await signInSession(x.email, x.password)).access_token
    const log = await getAuditLog(fresh, { event: [EVENT_HANDED_ON], limit: 100 })
    expect(log.events.filter((e) => e.actor === ws.adminId), 'one hand-on row by the deleted admin').toHaveLength(1)
  })

  test('account deletion: the last member schedules the workspace and keeps the account', async () => {
    const ws = await workspace('selfdelete-sole')
    try {
      const preview = await previewAccountDeletion(ws.adminToken)
      expect(preview.status, JSON.stringify(preview.body)).toBe(200)
      expect(outcomeOf(preview)).toBe(DELETE_WORKSPACE)

      const leave = await deleteMyAccount(ws.adminToken, { outcome: LEAVE })
      assertErrorEnvelope(leave, 409, 'leave as the sole member')
      expect(errorOf(leave)).toBe(OUTCOME_CHANGED)
      expect((await me(ws.adminToken)).tenant.deletion_due_at, 'the workspace stays unscheduled').toBeNull()

      const wrong = await scheduleDeletion(ws.adminToken, `${ws.workspaceName} x`)
      assertErrorEnvelope(wrong, 400, 'a wrong workspace name')
      expect(errorOf(wrong)).toBe(NAME_MISMATCH)

      const before = Date.now()
      const ok = await scheduleDeletion(ws.adminToken, ws.workspaceName)
      expect(ok.status, JSON.stringify(ok.body)).toBe(200)
      const due = (ok.body as { deletion_due_at: string }).deletion_due_at
      const span = new Date(due).getTime() - before
      expect(span).toBeGreaterThan(GRACE_MS - SLACK_MS)
      expect(span).toBeLessThan(GRACE_MS + SLACK_MS)
      expect((await me(ws.adminToken)).tenant.deletion_due_at).toBe(due)

      const session = (await signInSession(ws.email, ws.password)).access_token
      expect((await me(session)).tenant.deletion_due_at).toBe(due)
    } finally {
      await cancelDeletion(ws.adminToken)
    }
    const cancelled = await cancelDeletion(ws.adminToken)
    expect(cancelled.status, JSON.stringify(cancelled.body)).toBe(200)
    expect((await me(ws.adminToken)).tenant.deletion_due_at).toBeNull()
  })

  test('account deletion: a stale outcome is refused', async () => {
    const ws = await workspace('selfdelete-stale')
    await joined(ws, 'preparer', 'Stays')

    const res = await deleteMyAccount(ws.adminToken, { outcome: LEAVE })
    assertErrorEnvelope(res, 409, 'leave as the sole admin')
    expect(errorOf(res)).toBe(OUTCOME_CHANGED)
    expect((await me(ws.adminToken)).user.role, 'the caller stays an active admin').toBe('admin')
  })

  test('account deletion: the body cannot name another member', async () => {
    const ws = await workspace('selfdelete-victim')
    const p = await joined(ws, 'preparer', 'Caller')
    const q = await joined(ws, 'preparer', 'Bystander')

    const res = await deleteMyAccount(p.token, { outcome: LEAVE, user_id: q.userId })
    expect(res.status, JSON.stringify(res.body)).toBe(200)

    const status = new Map((await memberships(ws.adminToken)).memberships.map((m) => [m.user_id, m.status]))
    expect(status.get(p.userId)).toBe('removed')
    expect(status.get(q.userId)).toBe('active')
  })
})
