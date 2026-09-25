// Registration and workspace provisioning over the deployed gateway.
// The fork's GoTrue has signup on and SMTP blanked, so it mails nothing; the emailed-link
// path is proven in CI by TestIdP_EmailedLinkVerifiesThenSignInSucceeds instead.
// A pr-<N> fork is PosturePreview, so /auth/login mints any subject, an empty tenant included.
// Every run uses a fresh address and subject: the auth.users, tenants and memberships rows
// it creates survive the per-deploy reset.
import { test, expect } from '@playwright/test'
import { login, memberships, rawFetch, PERSONAS, type Me, type Persona } from './client'
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
// internal/gateway/register.go VerifyHandler: the failure redirect's query.
const VERIFY_FAILED = '?verify=failed'

const WORKSPACES = '/api/tenancy/v1/workspaces'
const ME = '/api/tenancy/v1/me'

function asSubject(subject: string, tenantId: string): Persona {
  return { ...PERSONAS.A, subject, tenantId }
}

test.describe('registration (API E2E, over the deployed gateway)', () => {
  test('a fresh address registers, and a repeat answers the same 202', async () => {
    const credentials = { email: `reg-${crypto.randomUUID()}@example.com`, password: crypto.randomUUID().slice(0, 12) }
    expect(credentials.password).toHaveLength(12)

    const first = await rawFetch('/auth/register', { method: 'POST', body: credentials })
    expect(first.status, 'a new address').toBe(202)
    expect(first.body).toEqual(VERIFICATION_PENDING)

    // GoTrue answers the repeat 429 over_email_send_rate_limit; the gateway maps it to 202.
    const repeat = await rawFetch('/auth/register', { method: 'POST', body: credentials })
    expect(repeat.status, 'a repeat must not reveal the address is taken').toBe(202)
    expect(repeat.body).toEqual(VERIFICATION_PENDING)
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

  test('a bogus verification link redirects 303 to the landing failure page', async () => {
    const res = await rawFetch('/auth/verify?token=bogus&type=signup', { redirect: 'manual' })
    expect(res.status).toBe(303)
    // Exact, so a lookalike host (landing.example.evil) cannot pass a prefix match.
    expect(res.location, 'the Location header').toBe(`${resolveTarget('LANDING_URL')}/${VERIFY_FAILED}`)
  })
})

test.describe('workspace provisioning (API E2E, over the deployed gateway)', () => {
  test('a tenant-less subject provisions one workspace and becomes its active admin', async () => {
    const subject = crypto.randomUUID()
    const workspace = { workspace_name: `Registration E2E ${subject.slice(0, 8)}`, display_name: 'Registration E2E' }
    const tenantless = await login(asSubject(subject, ''))
    const auth = { Authorization: `Bearer ${tenantless}` }

    let tenantId = ''
    await test.step('POST /workspaces -> 201 in the /me shape', async () => {
      const res = await rawFetch(WORKSPACES, { method: 'POST', headers: auth, body: workspace })
      expect(res.status, JSON.stringify(res.body)).toBe(201)
      const body = res.body as Me
      expect(typeof body.tenant.id).toBe('string')
      expect(body.tenant.id).not.toBe('')
      expect(body.tenant.name).toBe(workspace.workspace_name)
      // migrations/20260709153027_tenants_add_kind.sql: DEFAULT 'firm'.
      expect(body.tenant.kind).toBe('firm')
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
        tenant: { id: tenantId, name: workspace.workspace_name, kind: 'firm' },
        user: { id: subject, role: 'admin' },
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
