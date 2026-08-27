// AUDIT-10-06: read-path suspension over the deployed gateway. The Go tests prove the seam
// (internal/platform/db/request_gate_db_test.go); only this file proves the WIRE — which
// status and which body a real suspended member gets from a real route through a real JWT.
//
// The sign-in question, settled. `login()` mints for any (subject, tenant_id, role) triple
// here because the gate deploys a `pr-<N>` environment: platform.Posture() maps that name to
// PosturePreview, and MockLoginHandler consults its persona allowlist only under
// PostureHosted (internal/gateway/gateway.go). Two specs already depend on that and are green
// in this job — contract-tenancy.spec.ts mints a random-UUID tenant, and a tenant-A token for
// persona B's subject; neither triple can be in `loginPersonas`. So minting for …0007, tenant
// A's seeded SUSPENDED reviewer, is the same mechanism, not a new assumption.
//
// NOT READ-ONLY, and `memberships` is excluded from both the per-deploy reset and the demo
// purge (docs/e2e-convention.md), so a leaked status survives into the topology suite that
// runs after this one — and topology/roles.spec.ts asserts …0007's status pill verbatim. So:
//   - every mutating test restores `suspended` in its own `finally`, and the reactivated
//     window never spans a test boundary;
//   - beforeAll converges too, self-healing a crashed prior run, exactly as
//     contract-tenancy.spec.ts does for …0006;
//   - the last test is an independent read asserting the roster was left as the seed had it.
// db/seed.dev.sql re-asserts `status = EXCLUDED.status` on every deploy, so that is
// belt-and-braces, not the only guard.
//
// No browser spec accompanies this one, deliberately: the only browser-observable change is
// one error state, and docs/e2e-convention.md forbids growing the browser layer.
import { test, expect } from '@playwright/test'
import {
  getAuditLog,
  listEntities,
  listInvoices,
  login,
  memberships as listMemberships,
  rawFetch,
  setMembershipStatus,
  PERSONAS,
  type AuditEvent,
} from './client'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'

// The wire message. Declared once in Go (db.NotActiveMemberMessage,
// internal/platform/db/tenant.go) and TestHandlerMappingMessageIsNeverRetyped forbids a
// second Go copy — but that walk covers internal/, cmd/ and tools/ only, so this is the
// deployed-wire assertion, not a duplicate of a guarded literal.
const NOT_ACTIVE_MESSAGE = 'your membership in this workspace is not active'

// Tenant A's seeded suspended reviewer (db/seed.dev.sql), and never a tenant's sole admin —
// a tenant stranded at zero active admins needs a superuser to recover.
const SUSPENDED_SUBJECT = 'c0000000-0000-0000-0000-000000000007'

const AUDIT_LOG = '/api/invoice/v1/audit-log'

interface ReadRoute {
  label: string
  path: string
}

let adminToken = ''
let suspendedToken = ''
// The token as first minted. Compared before every post-reactivation read: Core AC 4 is that
// the gate reads status per REQUEST, so a spec that quietly re-minted would prove nothing.
let mintedOnce = ''
let readRoutes: ReadRoute[] = []

// Every loop below is an absence assertion over readRoutes, and an empty array passes one
// vacuously — so each asserts the floor first.
const READ_FAMILIES = 7

function bearer(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

function assertSuspensionRefusal(res: RawResult, label: string): void {
  // assertErrorEnvelope pins the body to EXACTLY {error: <string>} — which is also the
  // "403, never a body" half of Core AC 3: no rows ride along with the refusal.
  assertErrorEnvelope(res, 403, label)
  expect((res.body as { error: string }).error, `${label}: the shared refusal message`).toBe(NOT_ACTIVE_MESSAGE)
}

function payloadUserID(e: AuditEvent): unknown {
  return typeof e.payload === 'object' && e.payload !== null
    ? (e.payload as Record<string, unknown>).user_id
    : undefined
}

// convergeSuspended(): forces …0007 back to `suspended` and proves it with an INDEPENDENT
// read, never the PATCH's own response. Idempotent — SetMembershipStatus answers 200 with no
// UPDATE and no audit row when the target already holds the requested status — so it is safe
// from a beforeAll, a finally and a crashed prior run alike.
async function convergeSuspended(): Promise<void> {
  // A failed beforeAll leaves no admin token and nothing to restore; without this the
  // afterAll would replace the real failure with a bare 401.
  if (adminToken === '') return
  await setMembershipStatus(adminToken, SUSPENDED_SUBJECT, 'suspended')
  const { memberships } = await listMemberships(adminToken)
  const row = memberships.find((m) => m.user_id === SUSPENDED_SUBJECT)
  expect(row, `${SUSPENDED_SUBJECT} is missing from tenant A's roster`).toBeDefined()
  expect(row!.status, `${SUSPENDED_SUBJECT} must be left suspended — topology/roles.spec.ts asserts its status pill`).toBe(
    'suspended',
  )
}

test.describe('read-path suspension (API E2E, over the deployed gateway)', () => {
  test.beforeAll(async () => {
    adminToken = await login(PERSONAS.A)
    suspendedToken = await login({ ...PERSONAS.A, subject: SUSPENDED_SUBJECT })
    mintedOnce = suspendedToken

    // Ids are read live, never hard-coded: a route needing a real id must 403 for the
    // suspended member for the RIGHT reason, and must 200 for the admin.
    const { entities } = await listEntities(adminToken, { limit: 1 })
    expect(entities.length, 'tenant A must hold at least one entity (db/seed.dev.sql)').toBeGreaterThan(0)
    const { invoices } = await listInvoices(adminToken, { limit: 1 })
    expect(invoices.length, 'tenant A must hold at least one invoice (db/seed.dev.sql)').toBeGreaterThan(0)

    // A deliberately empty window: archive.PreviewHandler parses parameters BEFORE it reaches
    // the store, so a missing or malformed one would answer 400 and never reach the gate.
    // Zero invoices is a legitimate 200 (preview.go counts, it does not require a row).
    const bundle = new URLSearchParams({
      entity_id: entities[0].id,
      from: '2020-01-01T00:00:00Z',
      to: '2020-01-02T00:00:00Z',
    })

    // One route per read family, per the story — repo-wide without becoming per-endpoint.
    readRoutes = [
      { label: 'audit log', path: AUDIT_LOG },
      { label: 'invoice list', path: '/api/invoice/v1/invoices' },
      { label: 'invoice history', path: `/api/invoice/v1/invoices/${invoices[0].id}/history` },
      { label: 'entity list', path: '/api/portfolio/v1/entities' },
      { label: 'dashboard rollup', path: '/api/dashboard/v1/rollup' },
      { label: 'membership list', path: '/api/tenancy/v1/memberships' },
      { label: 'evidence-bundle preview', path: `/api/invoice/v1/evidence-bundle/preview?${bundle.toString()}` },
    ]

    await convergeSuspended()
  })

  test.afterAll(async () => {
    await convergeSuspended()
  })

  test('a suspended member is refused by every read family', async () => {
    expect(readRoutes, 'one route per read family').toHaveLength(READ_FAMILIES)
    for (const route of readRoutes) {
      const res = await rawFetch(route.path, { headers: bearer(suspendedToken) })
      assertSuspensionRefusal(res, `${route.label}, read by a suspended member`)
    }
  })

  test('a suspended member can still read /v1/me', async () => {
    // The one deliberate exemption (D-5): /v1/me is the SPA's only boot round trip, and
    // gating it would turn every suspended session into an unexplained sign-in failure with
    // nothing left able to say why. Its shape does not change, so the key sets are asserted
    // exactly — the same idiom contract-tenancy.spec.ts uses.
    const res = await rawFetch('/api/tenancy/v1/me', { headers: bearer(suspendedToken) })
    expect(res.status, '/v1/me must answer a suspended member').toBe(200)

    const body = res.body as { tenant: Record<string, unknown>; user: Record<string, unknown> }
    expect(Object.keys(body).sort(), '/v1/me top-level keys').toEqual(['tenant', 'user'])
    expect(Object.keys(body.tenant).sort(), '/v1/me tenant keys').toEqual(['id', 'kind', 'name'])
    expect(Object.keys(body.user).sort(), '/v1/me user keys').toEqual(['id', 'role'])
    expect(body.tenant.id, 'the suspended member still resolves their own tenant').toBe(PERSONAS.A.tenantId)
    expect(body.user.id, 'the subject the token carries').toBe(SUSPENDED_SUBJECT)
    expect(body.user.role, 'the seeded access role, unchanged by suspension').toBe('reviewer')
  })

  test('an active member is unaffected', async () => {
    // Without this the whole file would pass on a deployment that answered 403 to everyone.
    expect(readRoutes, 'one route per read family').toHaveLength(READ_FAMILIES)
    for (const route of readRoutes) {
      const res = await rawFetch(route.path, { headers: bearer(adminToken) })
      expect(res.status, `${route.label}, read by tenant A's active admin`).toBe(200)
    }
  })

  test('the three refusals are distinguishable', async () => {
    // Core AC 2. 401 signs the SPA out (authedFetch's isUnauthorized is true for 401 and
    // nothing else); 403 keeps the session and lets the screen say why; 404 is a resource
    // this caller cannot see. Collapsing any two would defeat the story.
    const unauthenticated = await rawFetch(AUDIT_LOG)
    assertErrorEnvelope(unauthenticated, 401, 'the audit log with no bearer')
    expect((unauthenticated.body as { error: string }).error, 'the unauthenticated message').toBe('unauthorized')

    const suspended = await rawFetch(AUDIT_LOG, { headers: bearer(suspendedToken) })
    assertSuspensionRefusal(suspended, 'the audit log, suspended member')

    const unknownTenantToken = await login({ ...PERSONAS.A, tenantId: crypto.randomUUID() })
    const unknownTenant = await rawFetch('/api/tenancy/v1/me', { headers: bearer(unknownTenantToken) })
    assertErrorEnvelope(unknownTenant, 404, '/v1/me for a tenant that does not exist')
    expect((unknownTenant.body as { error: string }).error, 'the unknown-tenant message').toBe('tenant not found')
  })

  test('cross-tenant: the refusal is scoped to the tenant that suspended them', async () => {
    // …0007 is suspended in tenant A and holds NO row at all in tenant B. AUDIT-12 flips the
    // gate to refuse a no-row caller exactly like a suspended one, so both now land on the
    // SAME shared refusal.
    const inTenantB = await login({ ...PERSONAS.B, subject: SUSPENDED_SUBJECT })

    const read = await rawFetch(AUDIT_LOG, { headers: bearer(inTenantB) })
    assertSuspensionRefusal(read, 'a non-member of tenant B')

    // /v1/me is stricter than the gate: it does its own membership lookup, so the same token
    // is refused there — a DIFFERENT 403, with a different message.
    const me = await rawFetch('/api/tenancy/v1/me', { headers: bearer(inTenantB) })
    assertErrorEnvelope(me, 403, '/v1/me in a tenant the caller holds no row in')
    expect((me.body as { error: string }).error, 'the no-membership refusal is not the suspension refusal').toBe(
      'no membership',
    )
  })

  test('a reactivated member regains read access', async () => {
    const before = await rawFetch(AUDIT_LOG, { headers: bearer(suspendedToken) })
    assertSuspensionRefusal(before, 'the audit log, before reactivation')

    try {
      const patched = await setMembershipStatus(adminToken, SUSPENDED_SUBJECT, 'active')
      expect(patched.status, 'the PATCH must land').toBe('active')

      expect(suspendedToken, 'the token must not be re-minted — the gate reads status per request').toBe(mintedOnce)

      expect(readRoutes, 'one route per read family').toHaveLength(READ_FAMILIES)
      for (const route of readRoutes) {
        const res = await rawFetch(route.path, { headers: bearer(suspendedToken) })
        expect(res.status, `${route.label}, read on the SAME token after reactivation`).toBe(200)
      }
    } finally {
      await convergeSuspended()
    }

    // And the same token loses access again the moment the row changes back — Core AC 4 in
    // the other direction. No JWT round trip either way.
    const after = await rawFetch(AUDIT_LOG, { headers: bearer(suspendedToken) })
    assertSuspensionRefusal(after, 'the audit log, after the live re-suspension')
    expect(suspendedToken, 'still the token minted in beforeAll').toBe(mintedOnce)
  })

  test('a suspended member cannot read the row recording their own suspension', async () => {
    // Core AC 3, the case that motivated the story. The re-suspension must be a real
    // transition: SetMembershipStatus no-ops (no UPDATE, no audit row) when the target
    // already holds the requested status, so a row naming …0007 needs active -> suspended.
    try {
      await setMembershipStatus(adminToken, SUSPENDED_SUBJECT, 'active')
      const resuspended = await setMembershipStatus(adminToken, SUSPENDED_SUBJECT, 'suspended')
      expect(resuspended.status, 'the re-suspension must land').toBe('suspended')

      // The row exists — proven by the admin, so the refusal below cannot pass vacuously.
      const asAdmin = await getAuditLog(adminToken, { event: ['membership.suspended'], limit: 100 })
      const naming = asAdmin.events.filter((e) => payloadUserID(e) === SUSPENDED_SUBJECT)
      expect(naming.length, 'the PATCH above must have written a membership.suspended row naming the target').toBeGreaterThan(
        0,
      )

      const denied = await rawFetch(`${AUDIT_LOG}?event=membership.suspended`, { headers: bearer(suspendedToken) })
      assertSuspensionRefusal(denied, 'the audit log, read by the member it just recorded suspending')
    } finally {
      await convergeSuspended()
    }
  })

  test('the roster is left as the seed had it', async () => {
    // An independent read, last: every mutating test above restores in a `finally`, and this
    // is the assertion that the restore actually landed.
    const { memberships } = await listMemberships(adminToken)
    const row = memberships.find((m) => m.user_id === SUSPENDED_SUBJECT)
    expect(row?.status, `${SUSPENDED_SUBJECT} must end the run suspended`).toBe('suspended')
  })
})
