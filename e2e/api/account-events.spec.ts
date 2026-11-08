// Account events over the deployed preview: the gateway records, the worker moves, the reader shows.
// Every assertion is containment of rows this spec caused: its own workspace, fresh addresses.
// Only the preview IP rule (rightmost X-Forwarded-For entry) runs here; the hosted rule is TestClientIP_Table.
import { test, expect } from '@playwright/test'
import {
  claimsOf,
  getAuditLog,
  grantMembership,
  inviteWithToken,
  mintSignInState,
  provisionRealAccount,
  rawFetch,
  registerFresh,
  requestPasswordReset,
  subjectOf,
  type AuditEvent,
  type RealAccount,
} from './client'

const CHROME_MAC =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36'

// internal/gateway/account_events.go, the constant named beside each value.
const SIGNED_IN = 'account.signed_in' // EventSignedIn
const SIGN_IN_FAILED = 'account.sign_in_failed' // EventSignInFailed
const SIGN_IN_BLOCKED = 'account.sign_in_blocked' // EventSignInBlocked
const SIGNED_OUT = 'account.signed_out' // EventSignedOut
const RESET_REQUESTED = 'account.password_reset_requested' // EventPasswordResetRequested

// internal/tenancy/store.go, the audit.Record calls.
const PROVISIONED = 'workspace.provisioned'
const ACCEPTED = 'invitation.accepted'

// internal/accountevents/device.go: the label for CHROME_MAC.
const CHROME_ON_MACOS = 'Chrome on macOS'
// internal/gateway/signin_throttle.go SignInMaxFailures.
const MAX_FAILURES = 10
const IP_PATTERN = /^(\d{1,3}(\.\d{1,3}){3}|[0-9a-fA-F:]*:[0-9a-fA-F:.]*)$/
const POLL = { timeout: 30_000, intervals: [1_000] }

interface Session {
  access_token: string
  refresh_token: string
}

// POST /auth/sign-in then /auth/exchange with a browser user agent.
async function signIn(email: string, password: string): Promise<Session> {
  const state = mintSignInState()
  const headers = { 'User-Agent': CHROME_MAC }
  const sign = await rawFetch('/auth/sign-in', { method: 'POST', headers, body: { email, password, state } })
  if (sign.status !== 200) throw new Error(`sign-in answered ${sign.status}: ${JSON.stringify(sign.body)}`)
  const code = (sign.body as { code: string }).code
  const ex = await rawFetch('/auth/exchange', { method: 'POST', headers, body: { code, state } })
  if (ex.status !== 200) throw new Error(`exchange answered ${ex.status}: ${JSON.stringify(ex.body)}`)
  return ex.body as Session
}

function signInAttempt(email: string, password: string): Promise<{ status: number; body: unknown }> {
  return rawFetch('/auth/sign-in', { method: 'POST', headers: { 'User-Agent': CHROME_MAC }, body: { email, password, state: mintSignInState() } })
}

async function signOut(session: Session): Promise<void> {
  const res = await rawFetch('/auth/sign-out', { method: 'POST', headers: { 'User-Agent': CHROME_MAC }, body: { refresh_token: session.refresh_token } })
  expect(res.status, JSON.stringify(res.body)).toBe(204)
}

async function rows(token: string, event: string): Promise<AuditEvent[]> {
  const res = await getAuditLog(token, { event: [event], limit: 100 })
  return res.events
}

const rowsOf = (events: AuditEvent[], actor: string): AuditEvent[] => events.filter((e) => e.actor === actor)

interface Payload {
  ip_address: string
  device: string
}
const payloadOf = (e: AuditEvent): Payload => e.payload as Payload

async function chromeSignIns(token: string, actor: string): Promise<AuditEvent[]> {
  return rowsOf(await rows(token, SIGNED_IN), actor).filter((e) => payloadOf(e).device === CHROME_ON_MACOS)
}

interface Member {
  email: string
  password: string
  id: string
  token: string // minted after the grant, so it carries the tenant claim
}

async function member(tenantId: string, prefix: string): Promise<Member> {
  const acct = await registerFresh(prefix)
  const id = subjectOf((await signIn(acct.email, acct.password)).access_token)
  await grantMembership({ user_id: id, tenant_id: tenantId, role: 'preparer', display_name: `Acct events ${prefix}`, email: acct.email })
  return { ...acct, id, token: (await signIn(acct.email, acct.password)).access_token }
}

test.describe.configure({ mode: 'serial' })

test.describe('account events (API E2E, over the deployed preview)', () => {
  let admin: RealAccount
  let adminToken: string
  let adminId: string
  let tenantId: string
  let preparer: Member
  let blocked: Member

  // Sign-out is global: it revokes every session of the account, so reads need a new token.
  async function refreshAdminToken(): Promise<void> {
    adminToken = (await signIn(admin.email, admin.password)).access_token
  }

  test.beforeAll(async () => {
    admin = await provisionRealAccount('acct-events')
    const session = await signIn(admin.email, admin.password)
    adminToken = session.access_token
    adminId = subjectOf(adminToken)
    tenantId = (claimsOf(adminToken).app_metadata as { tenant_id: string }).tenant_id
    preparer = await member(tenantId, 'acct-events-p')
    // K is used only by the blocked case.
    blocked = await member(tenantId, 'acct-events-k')
    // The setup sign-in above also uses the Chrome UA; wait until it is moved so test 1 counts only its own.
    await expect.poll(async () => (await chromeSignIns(adminToken, adminId)).length, POLL).toBeGreaterThan(0)
  })

  test('a sign-in is recorded with device and IP', async () => {
    const before = (await chromeSignIns(adminToken, adminId)).length
    await signIn(admin.email, admin.password)
    await expect.poll(async () => (await chromeSignIns(adminToken, adminId)).length, POLL).toBeGreaterThan(before)
    const mine = await chromeSignIns(adminToken, adminId)
    for (const e of mine) {
      expect(payloadOf(e).ip_address).toMatch(IP_PATTERN)
      expect(payloadOf(e).ip_address).not.toBe('Unknown IP')
      expect(e.company_scope).toBe('workspace')
    }
  })

  test('workspace created and invite accepted carry the device and IP', async () => {
    const headers = (token: string) => ({ Authorization: `Bearer ${token}`, 'User-Agent': CHROME_MAC })
    const detailOf = (e: AuditEvent): Record<string, unknown> => e.payload as Record<string, unknown>
    const expectDetail = (e: AuditEvent) => {
      expect(payloadOf(e).device).toBe(CHROME_ON_MACOS)
      expect(payloadOf(e).ip_address).toMatch(IP_PATTERN)
      expect(payloadOf(e).ip_address).not.toBe('Unknown IP')
    }
    const poll = async (token: string, event: string, actor: string): Promise<AuditEvent[]> => {
      await expect.poll(async () => rowsOf(await rows(token, event), actor).length, { ...POLL, message: event }).toBeGreaterThan(0)
      return rowsOf(await rows(token, event), actor)
    }

    // (1) The registrant provisions an in-house workspace: admin and actor.
    const reg = await registerFresh('acct-events-ws')
    const regId = subjectOf((await signIn(reg.email, reg.password)).access_token)
    const created = await rawFetch('/api/tenancy/v1/workspaces', {
      method: 'POST',
      headers: headers((await signIn(reg.email, reg.password)).access_token),
      body: { workspace_name: `Acct events ${crypto.randomUUID().slice(0, 8)}`, display_name: 'Acct events ws', kind: 'in_house' },
    })
    expect(created.status, JSON.stringify(created.body)).toBe(201)
    const newTenantId = (created.body as { tenant: { id: string } }).tenant.id
    const regToken = (await signIn(reg.email, reg.password)).access_token
    const provisioned = await poll(regToken, PROVISIONED, regId)
    for (const e of provisioned) expectDetail(e)

    // (2) An invitee accepts: the registrant (admin) and the invitee (actor) both read the detail.
    const inv = await registerFresh('acct-events-inv')
    const invSession = (await signIn(inv.email, inv.password)).access_token
    const invId = subjectOf(invSession)
    const inviteToken = await inviteWithToken(regToken, newTenantId, inv.email)
    const accepted = await rawFetch('/api/tenancy/v1/invitations/accept', { method: 'POST', headers: headers(invSession), body: { token: inviteToken } })
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
    const asAdmin = await poll(regToken, ACCEPTED, invId)
    for (const e of asAdmin) expectDetail(e)
    const invToken = (await signIn(inv.email, inv.password)).access_token
    const asActor = await poll(invToken, ACCEPTED, invId)
    expect(asActor.map((e) => e.id)).toEqual(asAdmin.map((e) => e.id))
    for (const e of asActor) expect(detailOf(e)).toEqual(detailOf(asAdmin.find((a) => a.id === e.id) as AuditEvent))

    // (3) O is in-house full scope, neither admin nor actor: both rows, no detail, no search hit.
    const o = await member(newTenantId, 'acct-events-ws-o')
    const oProvisioned = await poll(o.token, PROVISIONED, regId)
    const oAccepted = await poll(o.token, ACCEPTED, invId)
    expect(oProvisioned.map((e) => e.id)).toEqual(provisioned.map((e) => e.id))
    expect(oAccepted.map((e) => e.id)).toEqual(asAdmin.map((e) => e.id))
    for (const e of [...oProvisioned, ...oAccepted]) {
      expect(detailOf(e)).not.toHaveProperty('ip_address')
      expect(detailOf(e)).not.toHaveProperty('device')
    }
    const ip = payloadOf(provisioned[0]).ip_address
    expect((await getAuditLog(o.token, { event: [PROVISIONED, ACCEPTED], q: ip })).total).toBe(0)
  })

  test('a wrong password, a sign-out and a reset request are recorded', async () => {
    const wrong = await signInAttempt(admin.email, `${admin.password}-wrong`)
    expect(wrong.status).toBe(401)
    await signOut(await signIn(admin.email, admin.password))
    await refreshAdminToken()
    // A fork sends no mail (GOTRUE_MAILER_AUTOCONFIRM, blank SMTP host).
    expect((await requestPasswordReset({ email: admin.email })).status).toBe(202)

    for (const event of [SIGN_IN_FAILED, SIGNED_OUT, RESET_REQUESTED]) {
      await expect.poll(async () => rowsOf(await rows(adminToken, event), adminId).length, { ...POLL, message: event }).toBeGreaterThan(0)
    }
  })

  test('an admin sees every member, a preparer only themself', async () => {
    const session = await signIn(preparer.email, preparer.password)
    await expect.poll(async () => rowsOf(await rows(adminToken, SIGNED_IN), preparer.id).length, POLL).toBeGreaterThan(0)

    const own = await rows(session.access_token, SIGNED_IN)
    expect(own.length).toBeGreaterThan(0)
    expect(rowsOf(own, preparer.id).length).toBeGreaterThan(0)
    expect(rowsOf(own, adminId)).toEqual([])
    for (const e of own) expect(e.actor).toBe(preparer.id)
  })

  test('an unknown address leaves no row', async () => {
    const failedBefore = (await getAuditLog(adminToken, { event: [SIGN_IN_FAILED], limit: 100 })).total
    expect(failedBefore).toBeGreaterThan(0)
    const outBefore = (await getAuditLog(adminToken, { event: [SIGNED_OUT], limit: 100 })).total

    const res = await signInAttempt(`nobody-${crypto.randomUUID()}@example.com`, 'not-a-password')
    expect(res.status).toBe(401)
    // The marker shows the mover has passed the unknown attempt (it moves by occurred_at); jobs run concurrently, so wait one more tick before the read.
    await signOut(await signIn(admin.email, admin.password))
    await refreshAdminToken()
    await expect.poll(async () => (await getAuditLog(adminToken, { event: [SIGNED_OUT], limit: 100 })).total, POLL).toBeGreaterThan(outBefore)

    await new Promise((r) => setTimeout(r, 6_000)) // one HandoffInterval (5 s) plus job time
    expect((await getAuditLog(adminToken, { event: [SIGN_IN_FAILED], limit: 100 })).total).toBe(failedBefore)
  })

  test('each throttle refusal is recorded', async () => {
    // The throttle key is the address alone (signInKey), so K's attempts touch no other account.
    for (let i = 0; i < MAX_FAILURES; i++) {
      expect((await signInAttempt(blocked.email, `${blocked.password}-wrong`)).status, `wrong password ${i + 1}`).toBe(401)
    }
    for (let i = 0; i < 2; i++) {
      expect((await signInAttempt(blocked.email, `${blocked.password}-wrong`)).status, `refusal ${i + 1}`).toBe(429)
    }
    await expect.poll(async () => rowsOf(await rows(adminToken, SIGN_IN_BLOCKED), blocked.id).length, POLL).toBeGreaterThanOrEqual(2)

    const own = await rows(blocked.token, SIGN_IN_BLOCKED)
    expect(own).toHaveLength(2)
    for (const e of own) expect(e.actor).toBe(blocked.id)
    expect(rowsOf(await rows(adminToken, SIGN_IN_BLOCKED), blocked.id)).toHaveLength(2)
  })
})

