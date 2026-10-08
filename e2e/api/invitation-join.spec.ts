// Join-by-email API over the deployed gateway: list the caller's pending invites, accept one by id.
// Every test invites from workspaces of its own run, so addresses and ids never collide.
import { test, expect } from '@playwright/test'
import { acceptById, claimsOf, inviteWithToken, listMine, login, me, PERSONAS, rawFetch, registerFormAccount, registerFresh, signInSession, type PendingInvite } from './client'
import { assertErrorEnvelope } from './contract-helpers'

// internal/tenancy/accept.go msgInviteNotValid and msgAlreadyMember.
const NOT_VALID = 'this invite is no longer valid'
const ALREADY_MEMBER = 'you already belong to a workspace'

function auth(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

function tenantClaim(token: string): unknown {
  return (claimsOf(token).app_metadata as { tenant_id?: unknown } | undefined)?.tenant_id
}

async function pendingFor(adminToken: string, email: string): Promise<string[]> {
  const res = await rawFetch('/api/tenancy/v1/invitations', { headers: auth(adminToken) })
  expect(res.status, JSON.stringify(res.body)).toBe(200)
  return (res.body as { invitations: { email: string; status: string }[] }).invitations.filter((i) => i.email === email).map((i) => i.status)
}

async function mine(token: string): Promise<PendingInvite[]> {
  const res = await listMine(token)
  expect(res.status, JSON.stringify(res.body)).toBe(200)
  return (res.body as { invitations: PendingInvite[] }).invitations
}

async function tenantlessSession(email: string, password: string): Promise<string> {
  const token = (await signInSession(email, password)).access_token
  expect(tenantClaim(token), 'a tenant-less account carries no tenant claim').toBeUndefined()
  return token
}

interface Workspace {
  adminToken: string
  tenantId: string
  name: string
}

async function workspace(prefix: string): Promise<Workspace> {
  const adminToken = (await registerFormAccount(prefix)).token
  const identity = await me(adminToken)
  return { adminToken, tenantId: identity.tenant.id, name: identity.tenant.name }
}

test.describe.serial('invitation join (API E2E, over the deployed gateway)', () => {
  let first: Workspace
  let second: Workspace

  test.beforeAll(async () => {
    first = await workspace('join-admin-1')
    second = await workspace('join-admin-2')
  })

  test('invitation join: the list names both workspaces that invited a fresh account', async () => {
    const account = await registerFresh('join-list')
    await inviteWithToken(first.adminToken, first.tenantId, account.email)
    await inviteWithToken(second.adminToken, second.tenantId, account.email)

    const res = await listMine(await tenantlessSession(account.email, account.password))
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    const invites = (res.body as { invitations: PendingInvite[] }).invitations
    expect(invites).toHaveLength(2)
    for (const invite of invites) expect(Object.keys(invite).sort()).toEqual(['expires_at', 'id', 'inviter', 'role', 'workspace'])
    expect(invites.map((i) => i.workspace).sort()).toEqual([first.name, second.name].sort())
    expect(invites.every((i) => i.role === 'reviewer')).toBe(true)
  })

  test('invitation join: joining one by id leaves the other pending', async () => {
    const account = await registerFresh('join-one')
    await inviteWithToken(first.adminToken, first.tenantId, account.email)
    await inviteWithToken(second.adminToken, second.tenantId, account.email)
    const session = await tenantlessSession(account.email, account.password)
    const target = (await mine(session)).find((i) => i.workspace === first.name)
    expect(target, 'the invite of the first workspace is listed').toBeDefined()

    const joined = await acceptById(session, target!.id)
    expect(joined.status, JSON.stringify(joined.body)).toBe(200)
    expect((joined.body as { tenant: { id: string } }).tenant.id).toBe(first.tenantId)

    expect(tenantClaim((await signInSession(account.email, account.password)).access_token), 'the next sign-in carries the joined workspace').toBe(first.tenantId)
    expect(await pendingFor(second.adminToken, account.email), 'the other workspace still shows the invite').toEqual(['pending'])
  })

  test('invitation join: another account cannot list or accept an invite in either tenant', async () => {
    const owner = await registerFresh('join-owner')
    await inviteWithToken(first.adminToken, first.tenantId, owner.email)
    await inviteWithToken(second.adminToken, second.tenantId, owner.email)
    const ids = (await mine(await tenantlessSession(owner.email, owner.password))).map((i) => i.id)
    expect(ids).toHaveLength(2)

    const other = await registerFresh('join-other')
    const session = await tenantlessSession(other.email, other.password)
    expect(await mine(session), 'another address lists none of them').toEqual([])
    for (const id of ids) {
      const res = await acceptById(session, id)
      assertErrorEnvelope(res, 404, `accepting ${id} as another address`)
      expect((res.body as { error: string }).error).toBe(NOT_VALID)
    }
    expect(await pendingFor(first.adminToken, owner.email)).toEqual(['pending'])
    expect(await pendingFor(second.adminToken, owner.email)).toEqual(['pending'])
  })

  test('invitation join: a token without a verified email is refused at the edge', async () => {
    const token = await login({ ...PERSONAS.A, subject: crypto.randomUUID(), tenantId: '' })
    const list = await listMine(token)
    assertErrorEnvelope(list, 403, 'list with an unverified tenant-less token')
    expect((list.body as { error: string }).error).toBe('forbidden')
    const accepted = await acceptById(token, crypto.randomUUID())
    assertErrorEnvelope(accepted, 403, 'accept with an unverified tenant-less token')
    expect((accepted.body as { error: string }).error).toBe('forbidden')
  })

  test('invitation join: a caller with a workspace gets already-member', async () => {
    const res = await listMine(first.adminToken)
    assertErrorEnvelope(res, 409, 'list with a tenant-bearing token')
    expect((res.body as { error: string }).error).toBe(ALREADY_MEMBER)
  })
})
