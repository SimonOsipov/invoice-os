// Invite API over the deployed fork (tenancy via the gateway). A fork's sender captures, so a mail is
// never sent and delivery reads "sent". A fresh workspace per run keeps the daily invite count at zero.
import { test, expect } from '@playwright/test'
import { acceptById, grantMembership, inviteWithToken, listMine, me, rawFetch, registerFormAccount, registerFresh, signInSession, subjectOf } from './client'
import { assertErrorEnvelope } from './contract-helpers'

interface Invitation {
  id: string
  email: string
  role: string
  status: string
  expires_at: string
  delivery: string
  account?: string
}

// accountmail.InviteValidDays (internal/accountmail/accountmail.go); the spec allows one hour either way.
const INVITE_VALID_DAYS = 7
const HOUR = 3_600_000
const DAY = 24 * HOUR

// internal/gateway/invitation.go msgInviteNotValid; internal/tenancy/tenancy.go statusForErr (ErrInvitationNotFound, ErrInvitationNotPending).
const NOT_VALID = 'this invite is no longer valid'
const NOT_FOUND = 'invitation not found'
const NOT_PENDING = 'this invite is no longer pending'

const withoutAccount = (list: Invitation[]) => list.map(({ account: _account, ...rest }) => rest)

function auth(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

async function invite(token: string, emails: string[], role = 'reviewer') {
  return rawFetch('/api/tenancy/v1/invitations', { method: 'POST', headers: auth(token), body: { emails, role } })
}

const revoke = (token: string, id: string) => rawFetch(`/api/tenancy/v1/invitations/${id}/revoke`, { method: 'POST', headers: auth(token) })

async function listInvitations(token: string): Promise<Invitation[]> {
  const res = await rawFetch('/api/tenancy/v1/invitations', { headers: auth(token) })
  expect(res.status, JSON.stringify(res.body)).toBe(200)
  return (res.body as { invitations: Invitation[] }).invitations
}

test.describe.serial('invitations (API E2E, over the deployed gateway)', () => {
  let adminToken: string
  let tenantId: string
  let newEmail: string
  let registeredEmail: string

  test.beforeAll(async () => {
    adminToken = (await registerFormAccount('invitations')).token
    tenantId = (await me(adminToken)).tenant.id
    newEmail = `invitations-new-${crypto.randomUUID()}@example.com`
    registeredEmail = (await registerFresh('invitations-registered')).email
  })

  test('invitations: an admin invites a new address and a registered one, and both read the same', async () => {
    const before = Date.now()
    const res = await invite(adminToken, [newEmail, registeredEmail])
    const after = Date.now()
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    const items = (res.body as { invitations: Invitation[] }).invitations
    expect(items.map((i) => i.email)).toEqual([newEmail, registeredEmail])
    expect(Object.keys(items[0]).sort()).toEqual(Object.keys(items[1]).sort())
    for (const item of items) {
      expect(item.status).toBe('pending')
      expect(item.role).toBe('reviewer')
      expect(item.delivery).toBe('sent')
      const expires = Date.parse(item.expires_at)
      expect(expires).toBeGreaterThanOrEqual(before + INVITE_VALID_DAYS * DAY - HOUR)
      expect(expires).toBeLessThanOrEqual(after + INVITE_VALID_DAYS * DAY + HOUR)
    }
  })

  test('invitations: the pending list shows both, and a resend restarts the expiry', async () => {
    const listed = await listInvitations(adminToken)
    expect(listed.map((i) => i.email).sort()).toEqual([newEmail, registeredEmail].sort())
    const target = listed.find((i) => i.email === newEmail)!
    const res = await rawFetch(`/api/tenancy/v1/invitations/${target.id}/resend`, { method: 'POST', headers: auth(adminToken) })
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    const resent = res.body as Invitation
    expect(resent.id).toBe(target.id)
    expect(Date.parse(resent.expires_at)).toBeGreaterThanOrEqual(Date.parse(target.expires_at))
  })

  test("invitations: the pending list names each invitee's account state", async () => {
    // Values are Invitation.Account in internal/tenancy/invitations.go. The fork's grant is asynchronous.
    await expect
      .poll(async () => (await listInvitations(adminToken)).find((i) => i.email === registeredEmail)?.account, {
        message: `${registeredEmail} never read confirmed`,
        timeout: 120_000,
      })
      .toBe('confirmed')
    const listed = await listInvitations(adminToken)
    expect(listed.find((i) => i.email === newEmail)?.account, newEmail).toBe('none')
  })

  test('invitations: a malformed address is refused and nothing is created', async () => {
    const before = await listInvitations(adminToken)
    expect(before).toHaveLength(2)
    const res = await invite(adminToken, [`invitations-ok-${crypto.randomUUID()}@example.com`, 'not-an-email'])
    assertErrorEnvelope(res, 400, 'malformed address')
    // internal/tenancy/invitations_handler.go: "invalid email address: " + each address quoted.
    expect((res.body as { error: string }).error).toBe('invalid email address: "not-an-email"')
    // `account` can flip unknown -> confirmed between the reads when the fork's grant lands.
    expect(withoutAccount(await listInvitations(adminToken))).toEqual(withoutAccount(before))
  })

  test('invitations: a preparer is refused', async () => {
    const preparer = await registerFresh('invitations-preparer')
    const first = await signInSession(preparer.email, preparer.password)
    await grantMembership({
      user_id: subjectOf(first.access_token),
      tenant_id: tenantId,
      role: 'preparer',
      display_name: 'Invitations E2E Preparer',
      email: preparer.email,
    })
    // The grant lands after the first sign-in; a second one carries the tenant claim.
    const token = (await signInSession(preparer.email, preparer.password)).access_token
    const pendingId = (await listInvitations(adminToken))[0].id
    const res = await invite(token, [`invitations-refused-${crypto.randomUUID()}@example.com`])
    assertErrorEnvelope(res, 403, 'preparer invite')
    // internal/tenancy/tenancy.go: the 403 text of ErrInviteNotPermitted.
    expect((res.body as { error: string }).error).toBe('only an admin can invite people')
    const listRes = await rawFetch('/api/tenancy/v1/invitations', { headers: auth(token) })
    assertErrorEnvelope(listRes, 403, 'preparer list')
    expect((listRes.body as { error: string }).error).toBe('only an admin can invite people')
    expect(listRes.body).not.toHaveProperty('invitations')
    expect(await listInvitations(adminToken)).toHaveLength(2)
    const revoked = await revoke(token, pendingId)
    assertErrorEnvelope(revoked, 403, 'preparer revoke')
    expect((revoked.body as { error: string }).error).toBe('only an admin can invite people')
    expect(await listInvitations(adminToken)).toHaveLength(2)
  })

  test('invitations: an admin revokes a pending invite, the link dies on every route, and the address can be invited again', async () => {
    const invitee = await registerFresh('invitations-revoked')
    const token = await inviteWithToken(adminToken, tenantId, invitee.email)
    const id = (await listInvitations(adminToken)).find((i) => i.email === invitee.email)!.id
    const session = (await signInSession(invitee.email, invitee.password)).access_token
    const preview = () => rawFetch('/auth/invitation', { method: 'POST', body: { token } })

    expect((await preview()).status, 'the preview before the revoke').toBe(200)
    const res = await revoke(adminToken, id)
    expect(res.status, JSON.stringify(res.body)).toBe(200)
    expect(res.body).toEqual({ id, status: 'revoked' })
    expect((await listInvitations(adminToken)).map((i) => i.id)).not.toContain(id)

    const refusals: [string, { status: number; body: unknown }][] = [
      ['preview', await preview()],
      ['accept by token', await rawFetch('/api/tenancy/v1/invitations/accept', { method: 'POST', headers: auth(session), body: { token } })],
      ['accept by id', await acceptById(session, id)],
      ['invite registration', await rawFetch('/auth/invitation/register', { method: 'POST', body: { token, password: crypto.randomUUID().slice(0, 16) } })],
    ]
    for (const [name, refused] of refusals) {
      assertErrorEnvelope(refused, 404, `${name} of a revoked invite`)
      expect((refused.body as { error: string }).error, name).toBe(NOT_VALID)
    }
    const mine = await listMine(session)
    expect(mine.status, JSON.stringify(mine.body)).toBe(200)
    expect((mine.body as { invitations: unknown[] }).invitations).toEqual([])

    // The account stays and has no workspace: /me is 403 for a tenant-less token.
    const again = (await signInSession(invitee.email, invitee.password)).access_token
    const identity = await rawFetch('/api/tenancy/v1/me', { headers: auth(again) })
    expect(identity.status, JSON.stringify(identity.body)).toBe(403)

    const reinvited = await invite(adminToken, [invitee.email])
    expect(reinvited.status, JSON.stringify(reinvited.body)).toBe(200)
    const fresh = (await listInvitations(adminToken)).filter((i) => i.email === invitee.email)
    expect(fresh).toHaveLength(1)
    expect(fresh[0].id).not.toBe(id)
  })

  test("invitations: a revoke of an accepted or already revoked invite is a 409 and of another workspace's invite a 404", async () => {
    const accepted = await registerFresh('invitations-accepted')
    const acceptToken = await inviteWithToken(adminToken, tenantId, accepted.email)
    const acceptedId = (await listInvitations(adminToken)).find((i) => i.email === accepted.email)!.id
    const session = (await signInSession(accepted.email, accepted.password)).access_token
    const joined = await rawFetch('/api/tenancy/v1/invitations/accept', { method: 'POST', headers: auth(session), body: { token: acceptToken } })
    expect(joined.status, JSON.stringify(joined.body)).toBe(200)
    const spent = await revoke(adminToken, acceptedId)
    assertErrorEnvelope(spent, 409, 'revoke of an accepted invite')
    expect((spent.body as { error: string }).error).toBe(NOT_PENDING)

    const pendingEmail = `invitations-twice-${crypto.randomUUID()}@example.com`
    expect((await invite(adminToken, [pendingEmail])).status).toBe(200)
    const pendingId = (await listInvitations(adminToken)).find((i) => i.email === pendingEmail)!.id
    const other = await registerFormAccount('invitations-other')
    const foreign = await revoke(other.token, pendingId)
    assertErrorEnvelope(foreign, 404, "revoke of another workspace's invite")
    expect((foreign.body as { error: string }).error).toBe(NOT_FOUND)
    expect((await listInvitations(adminToken)).map((i) => i.id), 'the foreign revoke leaves it pending').toContain(pendingId)

    const first = await revoke(adminToken, pendingId)
    expect(first.status, JSON.stringify(first.body)).toBe(200)
    const second = await revoke(adminToken, pendingId)
    assertErrorEnvelope(second, 409, 'a second revoke')
    expect((second.body as { error: string }).error).toBe(NOT_PENDING)
  })
})
