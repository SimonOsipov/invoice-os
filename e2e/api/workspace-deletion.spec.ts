// Workspace deletion over the deployed gateway: schedule, the lock, the revoked-looking invites, cancel.
// Every workspace is registered here; the seeded tenants are never scheduled.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test, expect } from '@playwright/test'
import {
  acceptById,
  apiBase,
  cancelDeletion,
  cancelDeletionByLink,
  getAuditLog,
  inviteWithToken,
  listMine,
  me,
  memberships,
  mintSignInState,
  rawFetch,
  registerFormAccount,
  registerFresh,
  scheduleDeletion,
  setDeletionLinkToken,
  signInSession,
  subjectOf,
  type PendingInvite,
} from './client'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'
import { freshTin } from './fixtures'

// internal/platform/db/tenant.go WorkspaceLockedMessage.
const WORKSPACE_LOCKED = 'this workspace is scheduled for deletion'
// internal/tenancy/tenancy.go statusForErr: ErrDeleteWorkspaceNotPermitted, ErrCancelDeletionNotPermitted, ErrWorkspaceNameMismatch.
const DELETE_NOT_PERMITTED = 'only an admin can delete the workspace'
const CANCEL_NOT_PERMITTED = 'only an admin can cancel the deletion'
const NAME_MISMATCH = 'the workspace name does not match'
// internal/tenancy invite routes: the revoked-invite 404 text.
const NOT_VALID = 'this invite is no longer valid'
// internal/tenancy ErrCancelLinkNotValid, the cancel-by-link 403 text.
const LINK_NOT_VALID = 'this cancel link is no longer valid'
// internal/tenancy/workspace_deletion.go audit.Record event names.
const EVENT_SCHEDULED = 'workspace.deletion_scheduled'
const EVENT_CANCELLED = 'workspace.deletion_cancelled'

const PDF_BYTES = new Uint8Array(readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents/native_invoice.pdf')))
const DAY_MS = 86_400_000

const auth = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })
const errorOf = (res: RawResult): string => (res.body as { error: string }).error
const dueOf = (res: RawResult): string => (res.body as { deletion_due_at: string }).deletion_due_at

async function multipart(path: string, token: string, form: FormData): Promise<RawResult> {
  const res = await fetch(`${apiBase()}${path}`, { method: 'POST', headers: auth(token), body: form })
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

test.describe.serial('workspace deletion (API E2E, over the deployed gateway)', () => {
  let adminToken: string
  let workspaceName: string
  let tenantId: string
  let adminId: string
  let preparerToken: string
  let invitee: { email: string; password: string }
  let inviteeToken: string
  let inviteeSession: string
  let inviteId: string
  let due: string

  test.beforeAll(async () => {
    const reg = await registerFormAccount('deletion-admin')
    adminToken = reg.token
    workspaceName = reg.account.workspaceName
    tenantId = (await me(adminToken)).tenant.id
    adminId = subjectOf(adminToken)

    const preparer = await registerFresh('deletion-preparer')
    await inviteWithToken(adminToken, tenantId, preparer.email, 'preparer')
    const session = (await signInSession(preparer.email, preparer.password)).access_token
    const [pending] = (((await listMine(session)).body as { invitations: PendingInvite[] }).invitations)
    const accepted = await acceptById(session, pending.id, 'Deletion Preparer')
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
    preparerToken = (await signInSession(preparer.email, preparer.password)).access_token

    // Invited before the lock; the spec holds the token.
    invitee = await registerFresh('deletion-invitee')
    inviteeToken = await inviteWithToken(adminToken, tenantId, invitee.email, 'reviewer')
    inviteeSession = (await signInSession(invitee.email, invitee.password)).access_token
    const rows = ((await listMine(inviteeSession)).body as { invitations: PendingInvite[] }).invitations
    expect(rows, 'the invite is listed before the lock').toHaveLength(1)
    inviteId = rows[0].id
  })

  test('workspace deletion: a preparer cannot schedule and a wrong name is refused', async () => {
    const refused = await scheduleDeletion(preparerToken, workspaceName)
    assertErrorEnvelope(refused, 403, 'a preparer schedules')
    expect(errorOf(refused)).toBe(DELETE_NOT_PERMITTED)

    const wrong = await scheduleDeletion(adminToken, `${workspaceName} x`)
    assertErrorEnvelope(wrong, 400, 'a wrong workspace name')
    expect(errorOf(wrong)).toBe(NAME_MISMATCH)

    expect((await me(adminToken)).tenant.deletion_due_at, 'nothing was scheduled').toBeNull()
  })

  test('workspace deletion: an admin schedules and every member reads the due date', async () => {
    const res = await scheduleDeletion(adminToken, workspaceName)
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    due = dueOf(res)
    const drift = Date.parse(due) - (Date.now() + 30 * DAY_MS)
    expect(Math.abs(drift), 'due date is now + 30 days within 2 minutes').toBeLessThanOrEqual(120_000)

    expect((await me(adminToken)).tenant.deletion_due_at, 'the admin reads the due date').toBe(due)
    expect((await me(preparerToken)).tenant.deletion_due_at, 'the preparer reads the same date').toBe(due)
  })

  test('workspace deletion: a locked workspace refuses reads, writes, uploads, imports and submissions', async () => {
    const upload = new FormData()
    upload.set('file', new Blob([PDF_BYTES], { type: 'application/pdf' }), 'locked.pdf')
    const importForm = new FormData()
    importForm.set('entity_id', crypto.randomUUID())
    importForm.set('mapping', '{}')
    importForm.set('document_id', crypto.randomUUID())

    const refusals: [string, RawResult][] = [
      ['GET memberships', await rawFetch('/api/tenancy/v1/memberships', { headers: auth(adminToken) })],
      ['POST invitations', await rawFetch('/api/tenancy/v1/invitations', { method: 'POST', headers: auth(adminToken), body: { emails: [`locked-${crypto.randomUUID()}@example.com`], role: 'reviewer' } })],
      ['POST entities', await rawFetch('/api/portfolio/v1/entities', { method: 'POST', headers: auth(adminToken), body: { name: `Locked ${freshTin()}`, tin: freshTin() } })],
      ['POST documents', await multipart('/api/submission/v1/documents', adminToken, upload)],
      ['POST imports', await multipart('/api/invoice/v1/imports', adminToken, importForm)],
      ['POST invoices/submissions', await rawFetch('/api/invoice/v1/invoices/submissions', { method: 'POST', headers: auth(adminToken), body: { invoice_ids: [crypto.randomUUID()], idempotency_key: crypto.randomUUID() } })],
    ]
    for (const [label, res] of refusals) {
      assertErrorEnvelope(res, 403, label)
      expect(errorOf(res), label).toBe(WORKSPACE_LOCKED)
    }
  })

  test('workspace deletion: an invite of a locked workspace reads as revoked on every route', async () => {
    const refusals: [string, RawResult][] = [
      ['preview', await rawFetch('/auth/invitation', { method: 'POST', body: { token: inviteeToken } })],
      ['accept by token', await rawFetch('/api/tenancy/v1/invitations/accept', { method: 'POST', headers: auth(inviteeSession), body: { token: inviteeToken } })],
      ['accept by id', await acceptById(inviteeSession, inviteId)],
      ['invite registration', await rawFetch('/auth/invitation/register', { method: 'POST', body: { token: inviteeToken, password: crypto.randomUUID().slice(0, 16) } })],
    ]
    for (const [label, res] of refusals) {
      assertErrorEnvelope(res, 404, `${label} of a locked workspace's invite`)
      expect(errorOf(res), label).toBe(NOT_VALID)
    }
    const mine = await listMine(inviteeSession)
    expect(mine.status, JSON.stringify(mine.body)).toBe(200)
    expect((mine.body as { invitations: PendingInvite[] }).invitations.map((i) => i.id)).not.toContain(inviteId)
  })

  test('workspace deletion: only an admin cancels', async () => {
    const refused = await cancelDeletion(preparerToken)
    assertErrorEnvelope(refused, 403, 'a preparer cancels')
    expect(errorOf(refused)).toBe(CANCEL_NOT_PERMITTED)
    expect((await me(adminToken)).tenant.deletion_due_at, 'the due date stays').toBe(due)
  })

  test('workspace deletion: an admin cancels, the workspace unlocks and the log holds both transitions', async () => {
    const res = await cancelDeletion(adminToken)
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    expect(res.body).toEqual({ deletion_due_at: null })
    expect((await me(adminToken)).tenant.deletion_due_at).toBeNull()

    const created = await rawFetch('/api/portfolio/v1/entities', { method: 'POST', headers: auth(adminToken), body: { name: `Unlocked ${freshTin()}`, tin: freshTin() } })
    expect(created.status, JSON.stringify(created.body)).toBe(201)

    const rows = async () => {
      const log = await getAuditLog(adminToken, { event: [EVENT_SCHEDULED, EVENT_CANCELLED], limit: 100 })
      return log.events.filter((e) => e.actor === adminId).map((e) => e.event).sort()
    }
    expect(await rows(), 'one row per transition').toEqual([EVENT_CANCELLED, EVENT_SCHEDULED])

    const again = await cancelDeletion(adminToken)
    expect(again.status, JSON.stringify(again.body)).toBe(200)
    expect(await rows(), 'a second cancel adds no row').toEqual([EVENT_CANCELLED, EVENT_SCHEDULED])
  })

  test('workspace deletion: the same invite works again after the cancel', async () => {
    const preview = await rawFetch('/auth/invitation', { method: 'POST', body: { token: inviteeToken } })
    expect(preview.status, JSON.stringify(preview.body)).toBe(200)
    expect(JSON.stringify(preview.body), 'the preview names the workspace').toContain(workspaceName)

    const accepted = await rawFetch('/api/tenancy/v1/invitations/accept', { method: 'POST', headers: auth(inviteeSession), body: { token: inviteeToken } })
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)

    const inviteeId = subjectOf(inviteeSession)
    expect((await memberships(adminToken)).memberships.map((m) => m.user_id), 'the roster lists the invitee').toContain(inviteeId)
  })
})

test.describe.serial('cancel link (API E2E, over the deployed gateway)', () => {
  let adminToken: string
  let workspaceName: string
  let tenantId: string
  let adminBToken: string
  let adminBId: string
  let preparerToken: string
  let outsiderToken: string

  // Invites a fresh account into A's workspace and returns its session in that workspace.
  async function join(prefix: string, role: 'admin' | 'preparer'): Promise<string> {
    const who = await registerFresh(prefix)
    await inviteWithToken(adminToken, tenantId, who.email, role)
    const session = (await signInSession(who.email, who.password)).access_token
    const [pending] = ((await listMine(session)).body as { invitations: PendingInvite[] }).invitations
    const accepted = await acceptById(session, pending.id, 'Cancel Link Member')
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
    return (await signInSession(who.email, who.password)).access_token
  }

  // Schedules A's workspace, runs fn, and unlocks it again whatever fn did.
  async function whileLocked(fn: () => Promise<void>): Promise<void> {
    const res = await scheduleDeletion(adminToken, workspaceName)
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    try {
      await fn()
    } finally {
      await cancelDeletion(adminToken)
    }
  }

  const dueOfA = async (): Promise<string | null> => (await me(adminToken)).tenant.deletion_due_at

  test.beforeAll(async () => {
    const reg = await registerFormAccount('cancel-link-admin')
    adminToken = reg.token
    workspaceName = reg.account.workspaceName
    tenantId = (await me(adminToken)).tenant.id
    adminBToken = await join('cancel-link-admin-b', 'admin')
    adminBId = subjectOf(adminBToken)
    preparerToken = await join('cancel-link-preparer', 'preparer')
    outsiderToken = (await registerFormAccount('cancel-link-outsider')).token
  })

  test('cancel link: the scheduler gets no link and each other admin gets one', async () => {
    await whileLocked(async () => {
      await expect(setDeletionLinkToken(tenantId, adminBId, mintSignInState())).resolves.toBeUndefined()
      for (const [label, id] of [['the scheduler', subjectOf(adminToken)], ['a preparer', subjectOf(preparerToken)]]) {
        const err = await setDeletionLinkToken(tenantId, id, mintSignInState()).then(() => null, (e: Error) => e)
        expect(err?.message, `${label} has no link`).toContain('404')
      }
    })
  })

  test('cancel link: a preparer, another workspace\'s admin, a wrong token and no session are refused', async () => {
    await whileLocked(async () => {
      const linkToken = mintSignInState()
      await setDeletionLinkToken(tenantId, adminBId, linkToken)

      const preparer = await cancelDeletionByLink(preparerToken, linkToken)
      assertErrorEnvelope(preparer, 403, 'a preparer cancels by link')
      expect(errorOf(preparer)).toBe(CANCEL_NOT_PERMITTED)

      const outsider = await cancelDeletionByLink(outsiderToken, linkToken)
      assertErrorEnvelope(outsider, 403, "another workspace's admin cancels by link")
      expect(errorOf(outsider)).toBe(LINK_NOT_VALID)

      const wrong = await cancelDeletionByLink(adminBToken, mintSignInState())
      assertErrorEnvelope(wrong, 403, 'a wrong token')
      expect(errorOf(wrong)).toBe(LINK_NOT_VALID)

      const anonymous = await rawFetch('/api/tenancy/v1/workspace/deletion/cancel-by-link', { method: 'POST', body: { token: linkToken } })
      assertErrorEnvelope(anonymous, 401, 'no session')

      expect(await dueOfA(), 'the workspace stays locked').not.toBeNull()
    })
  })

  test('cancel link: another admin cancels with the link and the log names them', async () => {
    await whileLocked(async () => {
      const linkToken = mintSignInState()
      await setDeletionLinkToken(tenantId, adminBId, linkToken)

      const res = await cancelDeletionByLink(adminBToken, linkToken)
      expect(res.status, JSON.stringify(res.body)).toBe(200)
      expect(res.body).toEqual({ deletion_due_at: null })
      expect(await dueOfA()).toBeNull()

      const log = await getAuditLog(adminToken, { event: [EVENT_CANCELLED], limit: 100 })
      expect(log.events.some((e) => e.event === EVENT_CANCELLED && e.actor === adminBId), 'a cancelled row names B').toBe(true)
    })
  })

  test('cancel link: a used link and a link of an earlier deletion are void', async () => {
    const used = mintSignInState()
    await whileLocked(async () => {
      await setDeletionLinkToken(tenantId, adminBId, used)
      const first = await cancelDeletionByLink(adminBToken, used)
      expect(first.status, JSON.stringify(first.body)).toBe(200)

      const reused = await cancelDeletionByLink(adminBToken, used)
      assertErrorEnvelope(reused, 403, 'a used token')
      expect(errorOf(reused)).toBe(LINK_NOT_VALID)
    })

    await whileLocked(async () => {
      const old = await cancelDeletionByLink(adminBToken, used)
      assertErrorEnvelope(old, 403, 'a token of an earlier deletion')
      expect(errorOf(old)).toBe(LINK_NOT_VALID)
      expect(await dueOfA(), 'the old token cancelled nothing').not.toBeNull()

      const fresh = mintSignInState()
      await setDeletionLinkToken(tenantId, adminBId, fresh)
      const res = await cancelDeletionByLink(adminBToken, fresh)
      expect(res.status, JSON.stringify(res.body)).toBe(200)
    })
  })
})
