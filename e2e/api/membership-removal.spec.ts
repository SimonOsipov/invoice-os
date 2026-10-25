// Member removal over the deployed gateway: remove, history, the removed person's next sign-in, re-invite, another workspace.
// Every test joins members of its own, so removed rows never collide across tests or retries.
import { test, expect } from '@playwright/test'
import {
  acceptById,
  claimsOf,
  getAuditLog,
  inviteWithToken,
  listMine,
  me,
  memberships,
  rawFetch,
  registerFormAccount,
  registerFresh,
  removeMember,
  signInSession,
  subjectOf,
  type Membership,
  type PendingInvite,
} from './client'
import { assertErrorEnvelope } from './contract-helpers'

// internal/platform/db/tenant.go NotActiveMemberMessage.
const NOT_ACTIVE = 'your membership in this workspace is not active'
// internal/tenancy/tenancy.go: ErrRemoveNotPermitted, ErrRemovedNotTransitionable and ErrLastActiveAdmin.
const REMOVE_NOT_PERMITTED = 'only an admin can remove a member'
const REMOVED_NOT_TRANSITIONABLE = 'a removed member must be invited again'
const LAST_ADMIN_SENTENCE = "this is the tenant's last active admin — make another member an active admin first"
// internal/actor/actor.go RemovedName.
const DELETED_SUFFIX = ' (deleted)'
// internal/audit event types: store.go RemoveMember and acceptInvite.
const EVENT_REMOVED = 'membership.removed'
const EVENT_ACCEPTED = 'invitation.accepted'

const auth = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })
const tenantClaim = (token: string): unknown => (claimsOf(token).app_metadata as { tenant_id?: unknown } | undefined)?.tenant_id
const uniqueName = (label: string): string => `${label} ${crypto.randomUUID().slice(0, 8)}`

interface Workspace {
  adminToken: string
  tenantId: string
  adminId: string
}

async function workspace(prefix: string): Promise<Workspace> {
  const adminToken = (await registerFormAccount(prefix)).token
  return { adminToken, tenantId: (await me(adminToken)).tenant.id, adminId: subjectOf(adminToken) }
}

interface Member {
  email: string
  password: string
  userId: string
  name: string
  token: string
}

// A fresh account invited with `role`, joined by id with a name of its own; `token` carries the workspace claim.
async function joined(ws: Workspace, role: 'admin' | 'preparer' | 'reviewer', label: string): Promise<Member> {
  const account = await registerFresh('removal')
  await inviteWithToken(ws.adminToken, ws.tenantId, account.email, role)
  const name = uniqueName(label)
  const session = (await signInSession(account.email, account.password)).access_token
  const [invite] = await mine(session)
  const accepted = await acceptById(session, invite.id, name)
  expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
  const token = (await signInSession(account.email, account.password)).access_token
  expect(tenantClaim(token), 'the joined account carries the workspace claim').toBe(ws.tenantId)
  return { ...account, userId: subjectOf(token), name, token }
}

async function mine(token: string): Promise<PendingInvite[]> {
  const res = await listMine(token)
  expect(res.status, JSON.stringify(res.body)).toBe(200)
  return (res.body as { invitations: PendingInvite[] }).invitations
}

async function removeOk(ws: Workspace, userId: string): Promise<Membership> {
  const res = await removeMember(ws.adminToken, userId)
  expect(res.status, JSON.stringify(res.body)).toBe(200)
  return res.body as Membership
}

async function rowOf(ws: Workspace, userId: string): Promise<Membership> {
  const row = (await memberships(ws.adminToken)).memberships.find((m) => m.user_id === userId)
  expect(row, 'the roster holds the row').toBeDefined()
  return row!
}

test.describe.serial('membership removal (API E2E, over the deployed gateway)', () => {
  let first: Workspace
  let second: Workspace

  test.beforeAll(async () => {
    first = await workspace('removal-admin-1')
    second = await workspace('removal-admin-2')
  })

  test('membership removal: an admin removes a joined member and history says (deleted)', async () => {
    const member = await joined(first, 'preparer', 'Remove Me')
    const removed = await removeOk(first, member.userId)
    expect(removed.user_id).toBe(member.userId)
    expect(removed.status).toBe('removed')
    expect(removed.email, 'removal clears the address').toBeNull()
    expect(removed.display_name, 'the name stays').toBe(member.name)

    const read = await rawFetch('/api/tenancy/v1/memberships', { headers: auth(member.token) })
    assertErrorEnvelope(read, 403, 'the removed member reads the roster')
    expect((read.body as { error: string }).error).toBe(NOT_ACTIVE)

    const log = await getAuditLog(first.adminToken, { event: [EVENT_REMOVED, EVENT_ACCEPTED] })
    const ofMember = (e: { payload: unknown }) => (e.payload as { user_id?: string } | null)?.user_id === member.userId
    expect(log.events.filter((e) => e.event === EVENT_REMOVED && ofMember(e)), 'one removal row names the member').toHaveLength(1)
    const accepted = log.events.filter((e) => e.event === EVENT_ACCEPTED && e.actor === member.userId)
    expect(accepted, 'the earlier accept row').toHaveLength(1)
    expect(accepted[0].actor_name).toBe(`${member.name}${DELETED_SUFFIX}`)
  })

  test('membership removal: a preparer cannot remove, the last admin cannot be removed, a removed row cannot be reactivated', async () => {
    const preparer = await joined(first, 'preparer', 'Preparer')
    const target = await joined(first, 'reviewer', 'Target')

    const refused = await removeMember(preparer.token, target.userId)
    assertErrorEnvelope(refused, 403, 'a preparer removes a member')
    expect((refused.body as { error: string }).error).toBe(REMOVE_NOT_PERMITTED)
    expect((await rowOf(first, target.userId)).status, 'the refused remove changed nothing').toBe('active')

    const sole = await removeMember(first.adminToken, first.adminId)
    assertErrorEnvelope(sole, 409, 'the sole active admin removes themselves')
    expect((sole.body as { error: string }).error).toBe(LAST_ADMIN_SENTENCE)

    await removeOk(first, target.userId)
    const patch = await rawFetch(`/api/tenancy/v1/memberships/${target.userId}`, { method: 'PATCH', headers: auth(first.adminToken), body: { status: 'active' } })
    assertErrorEnvelope(patch, 409, 'PATCH on a removed row')
    expect((patch.body as { error: string }).error).toBe(REMOVED_NOT_TRANSITIONABLE)
    expect((await rowOf(first, target.userId)).status).toBe('removed')
  })

  test('membership removal: the removed member signs in tenant-less and is told removed', async () => {
    const member = await joined(first, 'reviewer', 'Told Removed')
    await removeOk(first, member.userId)

    const next = (await signInSession(member.email, member.password)).access_token
    expect(tenantClaim(next), 'the next token carries no workspace').toBeUndefined()
    const res = await listMine(next)
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    expect(res.body).toEqual({ invitations: [], removed: true })
  })

  test('membership removal: a re-invite revives the same row with the new name', async () => {
    const member = await joined(first, 'reviewer', 'Before')
    await removeOk(first, member.userId)

    await inviteWithToken(first.adminToken, first.tenantId, member.email, 'preparer')
    const session = (await signInSession(member.email, member.password)).access_token
    const [invite] = await mine(session)
    const name = uniqueName('After')
    const accepted = await acceptById(session, invite.id, name)
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)

    const row = await rowOf(first, member.userId)
    expect(row.status).toBe('active')
    expect(row.display_name).toBe(name)
    expect(row.role).toBe('preparer')
  })

  test('membership removal: the removed member joins another workspace', async () => {
    const member = await joined(first, 'reviewer', 'Moves On')
    await removeOk(first, member.userId)

    await inviteWithToken(second.adminToken, second.tenantId, member.email)
    const session = (await signInSession(member.email, member.password)).access_token
    const [invite] = await mine(session)
    const accepted = await acceptById(session, invite.id, uniqueName('Moved'))
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
    expect((accepted.body as { tenant: { id: string } }).tenant.id).toBe(second.tenantId)
    expect(tenantClaim((await signInSession(member.email, member.password)).access_token)).toBe(second.tenantId)
  })

  test('membership removal: a removed creator can provision a new workspace', async () => {
    const creator = await registerFormAccount('removal-creator')
    const old = await me(creator.token)
    const other = await joined({ adminToken: creator.token, tenantId: old.tenant.id, adminId: subjectOf(creator.token) }, 'admin', 'Second Admin')
    const res = await removeMember(other.token, subjectOf(creator.token))
    expect(res.status, JSON.stringify(res.body)).toBe(200)

    const tenantless = (await signInSession(creator.account.email, creator.account.password)).access_token
    expect(tenantClaim(tenantless)).toBeUndefined()
    const created = await rawFetch('/api/tenancy/v1/workspaces', {
      method: 'POST',
      headers: auth(tenantless),
      body: { workspace_name: uniqueName('Reborn'), display_name: uniqueName('Creator'), kind: 'firm' },
    })
    expect(created.status, JSON.stringify(created.body)).toBe(201)
    const tenantId = (created.body as { tenant: { id: string } }).tenant.id
    expect(tenantId, 'a workspace other than the old one').not.toBe(old.tenant.id)
    expect(tenantClaim((await signInSession(creator.account.email, creator.account.password)).access_token)).toBe(tenantId)
  })
})
