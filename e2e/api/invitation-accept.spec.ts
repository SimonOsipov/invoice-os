// Invite accept API over the deployed fork: preview, invitee registration, accept and its refusals.
// A fork's sender captures mail, so each token is set through POST /auth/mock/invitation-token (inviteWithToken).
// Every test invites into a workspace of its own run, so the daily invite count stays at zero.
import { test, expect } from '@playwright/test'
import { claimsOf, grantMembership, inviteWithToken, me, mintSignInState, rawFetch, registerFormAccount, registerFresh, setMembershipStatus, signInSession, subjectOf } from './client'
import { assertErrorEnvelope } from './contract-helpers'

// internal/tenancy/accept.go msgInviteNotValid, msgAlreadyMember and msgWrongAddress.
const NOT_VALID = 'this invite is no longer valid'
const ALREADY_MEMBER = 'you already belong to a workspace'
const WRONG_ADDRESS = 'this invite was sent to a different email address'
// internal/gateway/register.go's verification_pending answer, served by POST /auth/invitation/register too.
const PENDING = { status: 'verification_pending' }

function auth(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

const preview = (token: string) => rawFetch('/auth/invitation', { method: 'POST', body: { token } })
const accept = (session: string, token: string) => rawFetch('/api/tenancy/v1/invitations/accept', { method: 'POST', headers: auth(session), body: { token } })

function tenantClaim(token: string): unknown {
  return (claimsOf(token).app_metadata as { tenant_id?: unknown } | undefined)?.tenant_id
}

// A confirmed account with no workspace: forks auto-confirm, so it signs in at once.
async function tenantlessAccount(prefix: string): Promise<{ email: string; session: string }> {
  const account = await registerFresh(prefix)
  const session = (await signInSession(account.email, account.password)).access_token
  expect(tenantClaim(session), 'a tenant-less account carries no tenant claim').toBeUndefined()
  return { email: account.email, session }
}

test.describe.serial('invitation accept (API E2E, over the deployed gateway)', () => {
  let adminToken: string
  let tenantId: string

  test.beforeAll(async () => {
    adminToken = (await registerFormAccount('accept-admin')).token
    tenantId = (await me(adminToken)).tenant.id
  })

  const inviteAddress = (prefix: string) => `${prefix}-${crypto.randomUUID()}@example.com`

  test('invitation accept: the preview names the workspace and role, and a bogus token is no longer valid', async () => {
    const email = inviteAddress('accept-preview')
    const token = await inviteWithToken(adminToken, tenantId, email)

    const live = await preview(token)
    expect(live.status, JSON.stringify(live.body)).toBe(200)
    expect(Object.keys(live.body as object).sort()).toEqual(['email', 'role', 'workspace'])
    expect(live.body).toEqual({ workspace: (await me(adminToken)).tenant.name, role: 'reviewer', email })

    const bogus = await preview(mintSignInState())
    assertErrorEnvelope(bogus, 404, 'a token no invite holds')
    expect((bogus.body as { error: string }).error).toBe(NOT_VALID)
  })

  test('invitation accept: a fresh invitee registers with the token, signs in tenant-less, accepts and the next token carries the workspace', async () => {
    const email = inviteAddress('accept-fresh')
    const token = await inviteWithToken(adminToken, tenantId, email)
    const stillPending = inviteAddress('accept-pending')
    await inviteWithToken(adminToken, tenantId, stillPending)
    const password = crypto.randomUUID().slice(0, 16)

    const registered = await rawFetch('/auth/invitation/register', { method: 'POST', body: { token, password } })
    expect([registered.status, registered.body]).toEqual([202, PENDING])

    const first = (await signInSession(email, password)).access_token
    expect(tenantClaim(first), 'the first sign-in carries no tenant claim').toBeUndefined()

    const accepted = await accept(first, token)
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
    expect((accepted.body as { tenant: { id: string } }).tenant.id).toBe(tenantId)
    expect((accepted.body as { user: { role: string } }).user.role).toBe('reviewer')

    const second = (await signInSession(email, password)).access_token
    expect(tenantClaim(second), 'the next sign-in carries the inviting workspace').toBe(tenantId)
    const identity = await me(second)
    expect(identity.tenant.id).toBe(tenantId)
    expect(identity.user.role).toBe('reviewer')

    const listed = await rawFetch('/api/tenancy/v1/invitations', { headers: auth(adminToken) })
    expect(listed.status, JSON.stringify(listed.body)).toBe(200)
    const pending = (listed.body as { invitations: { email: string }[] }).invitations.map((i) => i.email)
    expect(pending, 'an invite nobody accepted stays listed').toContain(stillPending)
    expect(pending, 'the accepted invite leaves the pending list').not.toContain(email)

    // A spent token is no invite at all, not a conflict.
    const again = await accept(second, token)
    assertErrorEnvelope(again, 404, 'a second accept of the spent token')
    expect((again.body as { error: string }).error).toBe(NOT_VALID)
    const spent = await preview(token)
    assertErrorEnvelope(spent, 404, 'a preview of the spent token')
  })

  test('invitation accept: an account in a workspace gains nothing', async () => {
    const member = await registerFormAccount('accept-member')
    const before = await me(member.token)
    const token = await inviteWithToken(adminToken, tenantId, member.account.email)

    const refused = await accept(member.token, token)
    assertErrorEnvelope(refused, 409, 'accept by an account that has a workspace')
    expect((refused.body as { error: string }).error).toBe(ALREADY_MEMBER)

    expect(await me((await signInSession(member.account.email, member.account.password)).access_token)).toEqual(before)
  })

  test('invitation accept: a suspended member of another workspace is refused alike', async () => {
    const other = await registerFormAccount('accept-other-admin')
    const otherTenant = (await me(other.token)).tenant.id
    const target = await registerFresh('accept-suspended')
    const userId = subjectOf((await signInSession(target.email, target.password)).access_token)
    await grantMembership({ user_id: userId, tenant_id: otherTenant, role: 'reviewer', display_name: 'Accept E2E', email: target.email })
    await setMembershipStatus(other.token, userId, 'suspended')

    const token = await inviteWithToken(adminToken, tenantId, target.email)
    const session = (await signInSession(target.email, target.password)).access_token
    expect(tenantClaim(session), 'a suspended member carries no tenant claim').toBeUndefined()

    const refused = await accept(session, token)
    assertErrorEnvelope(refused, 409, 'accept by a suspended member of another workspace')
    expect((refused.body as { error: string }).error).toBe(ALREADY_MEMBER)
  })

  test('invitation accept: another account cannot use the link', async () => {
    const invited = inviteAddress('accept-invited')
    const token = await inviteWithToken(adminToken, tenantId, invited)
    const stranger = await tenantlessAccount('accept-stranger')

    const refused = await accept(stranger.session, token)
    assertErrorEnvelope(refused, 403, 'accept by an account the invite is not for')
    expect((refused.body as { error: string }).error).toBe(WRONG_ADDRESS)
  })

  test('invitation accept: a tenant-less token still reaches no other tenancy route', async () => {
    const { session } = await tenantlessAccount('accept-tenantless')

    const res = await rawFetch('/api/tenancy/v1/memberships', { headers: auth(session) })

    assertErrorEnvelope(res, 403, 'a tenant-less token on the membership list')
  })
})
