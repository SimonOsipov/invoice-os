// Invite API over the deployed fork (tenancy via the gateway). A fork's sender captures, so a mail is
// never sent and delivery reads "sent". A fresh workspace per run keeps the daily invite count at zero.
import { test, expect } from '@playwright/test'
import { grantMembership, me, rawFetch, registerFormAccount, registerFresh, signInSession, subjectOf } from './client'
import { assertErrorEnvelope } from './contract-helpers'

interface Invitation {
  id: string
  email: string
  role: string
  status: string
  expires_at: string
  delivery: string
}

// accountmail.InviteValidDays (internal/accountmail/accountmail.go); the spec allows one hour either way.
const INVITE_VALID_DAYS = 7
const HOUR = 3_600_000
const DAY = 24 * HOUR

function auth(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

async function invite(token: string, emails: string[], role = 'reviewer') {
  return rawFetch('/api/tenancy/v1/invitations', { method: 'POST', headers: auth(token), body: { emails, role } })
}

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

  test('invitations: a malformed address is refused and nothing is created', async () => {
    const before = await listInvitations(adminToken)
    expect(before).toHaveLength(2)
    const res = await invite(adminToken, [`invitations-ok-${crypto.randomUUID()}@example.com`, 'not-an-email'])
    assertErrorEnvelope(res, 400, 'malformed address')
    // internal/tenancy/invitations_handler.go: "invalid email address: " + each address quoted.
    expect((res.body as { error: string }).error).toBe('invalid email address: "not-an-email"')
    expect(await listInvitations(adminToken)).toEqual(before)
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
    const res = await invite(token, [`invitations-refused-${crypto.randomUUID()}@example.com`])
    assertErrorEnvelope(res, 403, 'preparer invite')
    // internal/tenancy/tenancy.go: the 403 text of ErrInviteNotPermitted.
    expect((res.body as { error: string }).error).toBe('only an admin can invite people')
    expect(await listInvitations(adminToken)).toHaveLength(2)
  })
})
