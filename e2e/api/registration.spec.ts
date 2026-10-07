// Registration and workspace provisioning over the deployed gateway.
// The fork's GoTrue has signup on, SMTP blanked and autoconfirm on, so it mails nothing; the
// emailed-link path is proven in CI by TestIdP_EmailedLinkVerifiesThenSignInSucceeds instead.
// /auth/login mints any subject in a mock build, an empty tenant included.
// Every run uses a fresh address and subject: the auth.users, tenants and memberships rows
// it creates survive the per-deploy reset.
import { test, expect } from '@playwright/test'
import { apiBase, getAuditLog, login, memberships, rawFetch, registerFresh, requestPasswordReset, resendVerification, PERSONAS, type Me, type Persona } from './client'
import { assertErrorEnvelope } from './contract-helpers'
import { resolveTarget } from '../targets'

// internal/gateway/register.go RegisterHandler: the 202 body.
const VERIFICATION_PENDING = { status: 'verification_pending' }
// internal/gateway/register.go RegisterHandler: the empty-field refusal.
const FIELDS_REQUIRED = 'email and password are required'
// internal/gateway/register.go RegisterHandler: the free-mail refusal.
const FREE_MAIL_REFUSED = 'a business email address is required; personal email providers are not accepted'
// internal/tenancy/tenancy.go statusForErr, ErrAlreadyProvisioned.
const ALREADY_PROVISIONED = 'this account already has a workspace'
// internal/gateway/gateway.go ServeHTTP: strings.ToLower(http.StatusText(403)).
const FORBIDDEN = 'forbidden'
// internal/gateway/register.go registrationAnswers: the workspace_name refusal.
const NAME_REFUSED = 'workspace_name must be 1 to 200 characters'
// internal/gateway/register.go maxAnswerNameChars.
const NAME_MAX_CHARS = 200
// internal/gateway/register.go VerifyHandler: the failure redirect's query.
const VERIFY_FAILED = '?verify=failed'
// internal/gateway/resend_verification.go ResendVerificationHandler: the 202 body.
const RESEND_ACCEPTED = { status: 'accepted' }
// internal/gateway/resend_verification.go ResendVerificationHandler: the 400 messages.
const RESEND_BODY_REFUSED = 'invalid request body'
const RESEND_EMAIL_REQUIRED = 'email is required'
const RESEND_EMAIL_REFUSED = 'invalid email address'
// internal/gateway/signin.go maxEmailBytes and maxExchangeBodyBytes (1 << 10).
const MAX_EMAIL_BYTES = 254
const MAX_BODY_BYTES = 1 << 10

// internal/gateway/register.go DefaultRegisterMinResponse; a pr-<N> fork inherits the default.
const REGISTER_MIN_MS = 2000
// db/seed.dev.sql: reviewer 'Halima Yusuf', status suspended, in tenant 1111...
const SUSPENDED_MEMBER = 'c0000000-0000-0000-0000-000000000007'

const WORKSPACES = '/api/tenancy/v1/workspaces'
const ME = '/api/tenancy/v1/me'

function asSubject(subject: string, tenantId: string): Persona {
  return { ...PERSONAS.A, subject, tenantId }
}

test.describe('registration (API E2E, over the deployed gateway)', () => {
  test('a fresh address registers, and a repeat answers the same 202', async () => {
    const credentials = { email: `reg-${crypto.randomUUID()}@example.com`, password: crypto.randomUUID().slice(0, 12) }
    expect(credentials.password).toHaveLength(12)

    // Lower bounds only: the deployed app's upper latency is not ours to assert.
    let t0 = performance.now()
    const first = await rawFetch('/auth/register', { method: 'POST', body: credentials })
    const firstMs = performance.now() - t0
    expect(first.status, 'a new address').toBe(202)
    expect(firstMs, 'a new address waits out the minimum').toBeGreaterThanOrEqual(REGISTER_MIN_MS)
    expect(first.body).toEqual(VERIFICATION_PENDING)

    // GoTrue answers the repeat 422 user_already_exists; the gateway maps it to 202.
    t0 = performance.now()
    const repeat = await rawFetch('/auth/register', { method: 'POST', body: credentials })
    const repeatMs = performance.now() - t0
    expect(repeatMs, 'a repeat waits out the minimum').toBeGreaterThanOrEqual(REGISTER_MIN_MS)
    expect(repeat.status, 'a repeat must not reveal the address is taken').toBe(202)
    expect(repeat.body).toEqual(VERIFICATION_PENDING)
  })

  test('a body carrying the registration answers answers 202, a name at the limit included', async () => {
    for (const workspace_name of [`Registration E2E ${crypto.randomUUID().slice(0, 8)}`, 'W'.repeat(NAME_MAX_CHARS)]) {
      const res = await rawFetch('/auth/register', {
        method: 'POST',
        body: {
          email: `reg-${crypto.randomUUID()}@example.com`,
          password: crypto.randomUUID().slice(0, 12),
          workspace_name,
          display_name: 'Registration E2E',
          kind: 'firm',
        },
      })
      expect(res.status, `${workspace_name.length} characters: ${JSON.stringify(res.body)}`).toBe(202)
      expect(res.body).toEqual(VERIFICATION_PENDING)
    }
  })

  test('a workspace name one character over the limit is refused with 400', async () => {
    const res = await rawFetch('/auth/register', {
      method: 'POST',
      body: {
        email: `reg-${crypto.randomUUID()}@example.com`,
        password: crypto.randomUUID().slice(0, 12),
        workspace_name: 'W'.repeat(NAME_MAX_CHARS + 1),
        display_name: 'Registration E2E',
        kind: 'firm',
      },
    })
    assertErrorEnvelope(res, 400, 'over-long workspace name')
    expect((res.body as { error: string }).error).toBe(NAME_REFUSED)
  })

  test('an empty password is refused with 400', async () => {
    const res = await rawFetch('/auth/register', {
      method: 'POST',
      body: { email: `reg-${crypto.randomUUID()}@example.com`, password: '' },
    })
    assertErrorEnvelope(res, 400, 'empty password')
    expect((res.body as { error: string }).error).toBe(FIELDS_REQUIRED)
  })

  test('a free-mail address is refused with the policy message', async () => {
    const res = await rawFetch('/auth/register', {
      method: 'POST',
      body: { email: `reg-${crypto.randomUUID()}@gmail.com`, password: crypto.randomUUID().slice(0, 12) },
    })
    assertErrorEnvelope(res, 400, 'free-mail address')
    expect((res.body as { error: string }).error).toBe(FREE_MAIL_REFUSED)
  })

  test('every resend answers the same 202 after the minimum', async () => {
    const registered = await registerFresh('resend')
    for (const [label, email] of [
      ['an unknown address', `resend-${crypto.randomUUID()}@example.com`],
      ['a registered address', registered.email],
    ]) {
      const t0 = performance.now()
      const res = await resendVerification({ email })
      const ms = performance.now() - t0
      expect(res.status, `${label}: ${JSON.stringify(res.body)}`).toBe(202)
      expect(res.body, label).toEqual(RESEND_ACCEPTED)
      expect(ms, `${label} waits out the minimum`).toBeGreaterThanOrEqual(REGISTER_MIN_MS)
    }
  })

  test("the landing origin's preflight for the resend route is granted", async () => {
    const landing = resolveTarget('LANDING_URL')
    const res = await fetch(`${apiBase()}/auth/resend-verification`, {
      method: 'OPTIONS',
      headers: { Origin: landing, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'content-type' },
    })
    expect(res.status).toBe(204)
    expect(res.headers.get('access-control-allow-origin')).toBe(landing)
  })

  test('a malformed, empty or over-long request is refused with 400 at once', async () => {
    const rows: [string, () => Promise<{ status: number; body: unknown }>, string][] = [
      ['an empty address', () => resendVerification({ email: '' }), RESEND_EMAIL_REQUIRED],
      ['an address over the cap', () => resendVerification({ email: `${'a'.repeat(MAX_EMAIL_BYTES)}@example.com` }), RESEND_EMAIL_REFUSED],
      ['a body over the cap', () => resendVerification({ email: `${'a'.repeat(MAX_BODY_BYTES)}@example.com` }), RESEND_BODY_REFUSED],
      [
        'a malformed body',
        async () => {
          const res = await fetch(`${apiBase()}/auth/resend-verification`, { method: 'POST', body: '{', headers: { 'Content-Type': 'application/json' } })
          return { status: res.status, body: await res.json() }
        },
        RESEND_BODY_REFUSED,
      ],
    ]
    for (const [label, send, message] of rows) {
      const t0 = performance.now()
      const res = await send()
      const ms = performance.now() - t0
      assertErrorEnvelope(res, 400, label)
      expect((res.body as { error: string }).error, label).toBe(message)
      expect(ms, `${label} does not wait out the minimum`).toBeLessThan(REGISTER_MIN_MS)
    }
  })

  test('opening a bogus verification link answers the confirm page', async () => {
    const res = await fetch(`${apiBase()}/auth/verify?token=bogus-${crypto.randomUUID()}&type=signup`, { redirect: 'manual' })
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type'), 'the content type').toMatch(/^text\/html/)
    expect(res.headers.get('location'), 'a Location header').toBeNull()
    expect(await res.text()).toContain('Confirm my email')
  })

  test('a bogus confirm click redirects 303 to the landing failure page', async () => {
    const res = await fetch(`${apiBase()}/auth/verify`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ token: `bogus-${crypto.randomUUID()}`, type: 'signup' }),
      redirect: 'manual',
    })
    expect(res.status).toBe(303)
    // Exact, so a lookalike host (landing.example.evil) cannot pass a prefix match.
    expect(res.headers.get('location'), 'the Location header').toBe(`${resolveTarget('LANDING_URL')}/${VERIFY_FAILED}`)
  })
})

test.describe('password reset (API E2E, over the deployed gateway)', () => {
  // internal/gateway/reset_password.go resetFailedURL: the failure redirect's query.
  const RESET_FAILED = '?reset=failed'
  // internal/gateway/reset_password.go resetPasswordHint: the short-password alert.
  const RESET_PASSWORD_HINT = 'Use a password of 6 to 72 characters.'
  const pageUrl = (token: string) => `${apiBase()}/auth/reset-password?token=${token}&type=recovery`
  const postForm = (fields: Record<string, string>) =>
    fetch(`${apiBase()}/auth/reset-password`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ type: 'recovery', ...fields }),
      redirect: 'manual',
    })

  test('every reset request answers the same 202 after the minimum', async () => {
    const registered = await registerFresh('reset')
    for (const [label, email] of [
      ['an unknown address', `reset-${crypto.randomUUID()}@example.com`],
      ['a registered address', registered.email],
    ]) {
      const t0 = performance.now()
      const res = await requestPasswordReset({ email })
      const ms = performance.now() - t0
      expect(res.status, `${label}: ${JSON.stringify(res.body)}`).toBe(202)
      expect(res.body, label).toEqual(RESEND_ACCEPTED)
      expect(ms, `${label} waits out the minimum`).toBeGreaterThanOrEqual(REGISTER_MIN_MS)
    }
  })

  test("the landing origin's preflight for the reset request route is granted", async () => {
    const landing = resolveTarget('LANDING_URL')
    const res = await fetch(`${apiBase()}/auth/request-password-reset`, {
      method: 'OPTIONS',
      headers: { Origin: landing, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'content-type' },
    })
    expect(res.status).toBe(204)
    expect(res.headers.get('access-control-allow-origin')).toBe(landing)
  })

  test('an empty address is refused with 400 at once', async () => {
    const t0 = performance.now()
    const res = await requestPasswordReset({ email: '' })
    const ms = performance.now() - t0
    assertErrorEnvelope(res, 400, 'an empty address')
    expect((res.body as { error: string }).error).toBe(RESEND_EMAIL_REQUIRED)
    expect(ms, 'an empty address does not wait out the minimum').toBeLessThan(REGISTER_MIN_MS)
  })

  test('opening a bogus reset link answers the page', async () => {
    const res = await fetch(pageUrl(`bogus-${crypto.randomUUID()}`), { redirect: 'manual' })
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type'), 'the content type').toMatch(/^text\/html/)
    expect(res.headers.get('location'), 'a Location header').toBeNull()
    expect(await res.text()).toContain('Set new password')
  })

  test('a reset link with an empty token redirects 303 to the landing failure notice', async () => {
    const res = await fetch(pageUrl(''), { redirect: 'manual' })
    expect(res.status).toBe(303)
    expect(res.headers.get('location'), 'the Location header').toBe(`${resolveTarget('LANDING_URL')}/${RESET_FAILED}`)
  })

  test('a bogus reset form post redirects 303 to the landing failure notice', async () => {
    const res = await postForm({ token: `bogus-${crypto.randomUUID()}`, password: crypto.randomUUID() })
    expect(res.status).toBe(303)
    expect(res.headers.get('location'), 'the Location header').toBe(`${resolveTarget('LANDING_URL')}/${RESET_FAILED}`)
  })

  test('a too-short password re-renders the page with 400', async () => {
    const res = await postForm({ token: `bogus-${crypto.randomUUID()}`, password: 'short' })
    expect(res.status).toBe(400)
    expect(res.headers.get('content-type'), 'the content type').toMatch(/^text\/html/)
    expect(await res.text()).toContain(RESET_PASSWORD_HINT)
  })
})

test.describe('workspace provisioning (API E2E, over the deployed gateway)', () => {
  test('a tenant-less subject provisions one workspace and becomes its active admin', async () => {
    const subject = crypto.randomUUID()
    const workspace = { workspace_name: `Registration E2E ${subject.slice(0, 8)}`, display_name: 'Registration E2E' }
    const tenantless = await login(asSubject(subject, ''))
    const auth = { Authorization: `Bearer ${tenantless}` }

    let tenantId = ''
    await test.step('POST /workspaces -> 201', async () => {
      const res = await rawFetch(WORKSPACES, { method: 'POST', headers: auth, body: workspace })
      expect(res.status, JSON.stringify(res.body)).toBe(201)
      const body = res.body as { tenant: Me['tenant']; user: Pick<Me['user'], 'id' | 'role'> }
      expect(typeof body.tenant.id).toBe('string')
      expect(body.tenant.id).not.toBe('')
      expect(body.tenant.name).toBe(workspace.workspace_name)
      // internal/tenancy/store.go ProvisionWorkspace: an absent kind is in_house.
      expect(body.tenant.kind).toBe('in_house')
      expect(body.user).toEqual({ id: subject, role: 'admin' })
      tenantId = body.tenant.id
    })

    await test.step('the same token again -> 409', async () => {
      const res = await rawFetch(WORKSPACES, { method: 'POST', headers: auth, body: workspace })
      assertErrorEnvelope(res, 409, 'second provision')
      expect((res.body as { error: string }).error).toBe(ALREADY_PROVISIONED)
    })

    await test.step('the tenant-less token on GET /me -> 403', async () => {
      const res = await rawFetch(ME, { headers: auth })
      assertErrorEnvelope(res, 403, 'tenant-less /me')
      expect((res.body as { error: string }).error).toBe(FORBIDDEN)
    })

    const tenantToken = await login(asSubject(subject, tenantId))

    await test.step('a token carrying the new tenant resolves it on GET /me', async () => {
      const res = await rawFetch(ME, { headers: { Authorization: `Bearer ${tenantToken}` } })
      expect(res.status, JSON.stringify(res.body)).toBe(200)
      expect(res.body).toEqual({
        tenant: { id: tenantId, name: workspace.workspace_name, kind: 'in_house' },
        user: { id: subject, role: 'admin', display_name: workspace.display_name, email: null },
      })
    })

    await test.step('the roster holds the subject as an active admin with no email', async () => {
      const { memberships: rows } = await memberships(tenantToken)
      const row = rows.find((m) => m.user_id === subject)
      expect(row, `subject ${subject} in the roster`).toBeDefined()
      expect(row!.role).toBe('admin')
      expect(row!.status).toBe('active')
      // The mock token carries no email claim.
      expect(row!.email).toBeNull()
    })

    await test.step('the new admin reads exactly one workspace.provisioned event', async () => {
      const { events } = await getAuditLog(tenantToken, { event: ['workspace.provisioned'] })
      expect(events).toHaveLength(1)
      const [event] = events
      expect(event.actor).toBe(subject)
      expect(event.company_scope).toBe('workspace')
      expect(event.entity_id).toBeNull()
      expect((event.payload as { tenant_id: string }).tenant_id).toBe(tenantId)
    })
  })

  test('a member of another workspace cannot provision a second one', async () => {
    const token = await login(asSubject(SUSPENDED_MEMBER, ''))
    const res = await rawFetch(WORKSPACES, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { workspace_name: `Registration E2E ${crypto.randomUUID().slice(0, 8)}`, display_name: 'Registration E2E' },
    })
    assertErrorEnvelope(res, 409, 'suspended member provision')
    expect((res.body as { error: string }).error).toBe(ALREADY_PROVISIONED)
  })

  test('a tenant-bearing caller is refused with 409', async () => {
    const token = await login(PERSONAS.A)
    const res = await rawFetch(WORKSPACES, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { workspace_name: `Registration E2E ${crypto.randomUUID().slice(0, 8)}`, display_name: 'Registration E2E' },
    })
    assertErrorEnvelope(res, 409, 'persona A provision')
    expect((res.body as { error: string }).error).toBe(ALREADY_PROVISIONED)
  })
})
