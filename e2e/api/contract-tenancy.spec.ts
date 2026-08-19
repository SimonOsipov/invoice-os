// The tenancy contract spec, over the wire — through the SAME typed seam
// (api/client.ts) every api/ spec shares. Assertions go through rawFetch
// (M3-15-01), so the exact HTTP status + envelope shape is directly
// observable — unlike apiFetch, which normalizes a non-2xx into a thrown
// ApiError. Mirrors contract-portfolio.spec.ts's assertErrorEnvelope
// convention (its own local copy, not imported — matches this suite's
// existing per-file duplication of the shape helper).
//
// NO LONGER READ-ONLY. The PATCH matrix below writes to a shared environment, and
// memberships is one of the tables the per-PR reset deliberately EXCLUDES (resetTables) --
// a dirty status survives the run. So every write here obeys two rules:
//   - the subject is never …0001/…0002, each tenant's sole admin — a tenant
//     stranded at zero active admins needs a superuser to recover;
//   - the row is forced back to `active` on the way in AND on the way out, in a
//     `finally`, so neither a mid-assertion failure nor a killed prior run can
//     leave it dirty. The api job runs BEFORE test:topology in dev-env.yml
//     (:816 / :821), and topology's roles.spec.ts asserts exact roster content.
//
// Four properties are proven against the DEPLOYED gateway:
//   - Happy-path status + shape (persona A, Core AC 1): /me -> 200 +
//     {tenant:{id,name,kind}, user:{id,role}}; /memberships -> 200 +
//     {memberships:[{user_id,role,status,display_name,email},...]}.
//   - The PATCH status matrix: 200 round-trip, 403 non-admin, 404 unknown
//     user_id, 400 out-of-vocabulary status.
//   - Error-path status + envelope on GET /me: both cases mint their token
//     via the EXISTING login() helper — no new fixtures. login() only reads
//     persona.subject + persona.tenantId (client.ts), so a plain object
//     spread of PERSONAS.A with one field overridden is enough to express a
//     custom (subject, tenant_id) pair; no change to login()/client.ts was
//     needed.
//     - 403 (no membership): tenant_id = seeded tenant A, subject = persona
//       B (a real seeded UUID, member of tenant B only). Gateway
//       authorize() passes (tenant_id non-empty) -> tenancy resolves tenant
//       A under RLS -> the membership lookup for subject-B-in-tenant-A finds
//       no row -> ErrNoMembership -> 403.
//     - 404 (unknown tenant): tenant_id = crypto.randomUUID() (syntactically
//       valid, nonexistent). The tenant lookup runs first under RLS and
//       returns 0 rows -> ErrTenantNotFound -> 404, before membership is
//       ever queried.
//   - List-shape boundary (Core AC 4): /memberships is a flat, non-paginated
//     list — the body has NO `pagination` key (the paginated list is
//     portfolio's, covered in M3-15-03).
//
// Deliberately NOT covered here:
//   - 500: legitimately unreachable without breaking the server (a real
//     DB/infra failure) — not something this suite can trigger on demand.
//   - 401 (missing/malformed/invalid-Bearer auth): the shared pre-routing
//     auth-failure envelope is already proven for the tenancy surface (and
//     cross-surface) by auth-contract.spec.ts (M3-15-02).
//   - 409 last-active-admin. UNREACHABLE from here without breaking the rule
//     above: each seeded tenant has exactly ONE admin and it is the sign-in
//     persona, and PATCH writes `status` only — it can never mint a second
//     admin, so no seed-only subject can trigger this branch. Covered instead
//     by a PAIR in internal/tenancy/tenancy_test.go, each half over an isolated
//     fixture: TestMembership_LastActiveAdminRefused proves the store REFUSES
//     (ErrLastActiveAdmin, both shapes), and TestMembership_StatusForErrTable
//     proves that error maps to HTTP 409 with the sentence the SPA renders.
//     Neither alone is the wire claim. A cited gap, not a silent one.
//   - 409 invited-not-transitionable: no seeded row has status `invited` and
//     nothing mints one.
import { test, expect } from '@playwright/test'
import { login, memberships, rawFetch, setMembershipStatus, PERSONAS, type Membership } from './client'
import { assertErrorEnvelope } from './contract-helpers'

// The five keys internal/tenancy's Membership serializes, sorted — the exact-key-set
// idiom the /me test below already uses, applied per row.
const MEMBERSHIP_KEYS = ['display_name', 'email', 'role', 'status', 'user_id']

// Seeded subjects (db/seed.dev.sql), never a tenant's sole admin. Every active
// seeded member can sign in now, so a leaked suspension would also block a demo
// login until the next deploy re-seeds.
// …0006 is the PATCH target deliberately: it holds the firm's `preparer` seat
// ALONGSIDE …0003, so even a failed restore leaves every role-derived string in
// topology/roles.spec.ts byte-identical (preparer keeps an active holder, and it
// is named in no policy step, so no row grows a blocking warning). Only its
// status pill would differ.
const PATCH_SUBJECT = 'c0000000-0000-0000-0000-000000000006'
const PREPARER_SUBJECT = 'c0000000-0000-0000-0000-000000000003'

test.describe('tenancy contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test.describe('happy-path status + shape (persona A)', () => {
    test('/me -> 200 + {tenant:{id,name,kind}, user:{id,role}}', async () => {
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, '/me should return 200').toBe(200)

      const body = res.body as Record<string, unknown>
      expect(Object.keys(body).sort(), 'expected exactly the tenant/user top-level keys').toEqual(['tenant', 'user'])

      const tenant = body.tenant as Record<string, unknown>
      expect(Object.keys(tenant).sort(), 'expected exactly the tenant.{id,name,kind} keys').toEqual([
        'id',
        'kind',
        'name',
      ])
      // Known seeded values (db/seed.dev.sql) — same convention as
      // isolation.spec.ts's AC1: a real, resolvable identity, not just a
      // well-typed one.
      expect(tenant.id).toBe(PERSONAS.A.tenantId)
      expect(tenant.name).toBe('Okafor & Partners')
      expect(tenant.kind).toBe('firm')

      const user = body.user as Record<string, unknown>
      expect(Object.keys(user).sort(), 'expected exactly the user.{id,role} keys').toEqual(['id', 'role'])
      expect(user.id).toBe(PERSONAS.A.subject)
      expect(user.role).toBe('admin')
    })

    test('/memberships -> 200 + {memberships:[{user_id,role,status,display_name,email},...]}, no pagination key', async () => {
      const res = await rawFetch('/api/tenancy/v1/memberships', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, '/memberships should return 200').toBe(200)

      const body = res.body as Record<string, unknown>
      expect(Array.isArray(body.memberships), 'body.memberships should be an array').toBe(true)
      const members = body.memberships as Array<Record<string, unknown>>
      expect(members.length, 'tenant A has seeded members').toBeGreaterThan(0)
      for (const m of members) {
        // EXACT key set per row, not a typeof sweep: display_name/email carry no
        // omitempty on the Go struct, so a row that stored no address must still
        // ship the key as an explicit null. A presence-only check passes on the
        // omission this assertion exists to catch.
        expect(Object.keys(m).sort(), `membership ${String(m.user_id)}: expected exactly the five wire keys`).toEqual(MEMBERSHIP_KEYS)
        expect(typeof m.user_id, 'each membership.user_id should be a string').toBe('string')
        expect(typeof m.role, 'each membership.role should be a string').toBe('string')
        expect(typeof m.status, 'each membership.status should be a string').toBe('string')
        expect(m.display_name === null || typeof m.display_name === 'string', 'display_name should be a string or null').toBe(true)
        expect(m.email === null || typeof m.email === 'string', 'email should be a string or null').toBe(true)
      }

      // List-shape boundary (Core AC 4): memberships is a flat,
      // non-paginated list — unlike portfolio's list (M3-15-03), it must
      // NOT carry a pagination envelope.
      expect(body, 'memberships should have no pagination key').not.toHaveProperty('pagination')
    })
  })

  // The persona switcher renders this list. A null display_name, an unseeded role, or a
  // suspended row filtered out server-side would each leave it nothing usable to draw.
  test.describe('roster identity, both tenants', () => {
    const SEEDED_ROLES = ['admin', 'preparer', 'reviewer']
    const SEEDED_STATUSES = ['active', 'suspended']
    const INHOUSE_REVIEWER = 'c0000000-0000-0000-0000-000000000008'

    let tokenB: string

    test.beforeAll(async () => {
      tokenB = await login(PERSONAS.B)
    })

    function assertRenderable(rows: Membership[], label: string) {
      expect(rows.length, `${label}: the roster should not be empty`).toBeGreaterThan(0)
      for (const m of rows) {
        expect(
          typeof m.display_name === 'string' && m.display_name.length > 0,
          `${label}: ${m.user_id} has no display_name — the switcher would render a bare uuid`,
        ).toBe(true)
        expect(SEEDED_ROLES, `${label}: ${m.user_id} role`).toContain(m.role)
        expect(SEEDED_STATUSES, `${label}: ${m.user_id} status`).toContain(m.status)
      }
      expect(
        rows.some((m) => m.status === 'suspended'),
        `${label}: no row reports status "suspended" — the switcher has nothing to render disabled`,
      ).toBe(true)
    }

    test('every row carries a renderable identity, the suspended one included (persona A)', async () => {
      const { memberships: rows } = await memberships(token)
      assertRenderable(rows, 'firm')
    })

    test('every row carries a renderable identity, the suspended one included (persona B)', async () => {
      const { memberships: rows } = await memberships(tokenB)
      assertRenderable(rows, 'in-house')
    })

    test('no row carries tenant_id (persona B)', async () => {
      const { memberships: rows } = await memberships(tokenB)
      for (const m of rows) {
        expect(
          Object.keys(m).sort(),
          `${m.user_id}: a switcher takes the tenant from GET /v1/me, never from a roster row`,
        ).toEqual(MEMBERSHIP_KEYS)
      }
    })

    test('a newly admitted in-house reviewer resolves its seeded access role', async () => {
      // Proves ROLE RESOLUTION only, not the sign-in allowlist: the gate deploys a pr-<N>
      // environment (PosturePreview), where the hosted allowlist is not consulted, so
      // /auth/login is permissive here for any subject. The refusal proof is a Go test.
      const reviewerToken = await login({ ...PERSONAS.B, subject: INHOUSE_REVIEWER })
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${reviewerToken}` },
      })
      expect(res.status, '/me should return 200 for the in-house reviewer').toBe(200)

      const body = res.body as { tenant: Record<string, unknown>; user: Record<string, unknown> }
      expect(body.tenant.id, 'the in-house tenant').toBe(PERSONAS.B.tenantId)
      expect(body.user.role, 'the seeded access role').toBe('reviewer')
    })
  })

  test.describe('error paths on GET /me', () => {
    test('403 (no membership): tenant A token, persona B subject -> {error: string}', async () => {
      const noMembershipToken = await login({ ...PERSONAS.A, subject: PERSONAS.B.subject })
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${noMembershipToken}` },
      })
      assertErrorEnvelope(res, 403, 'tenant A token + persona B subject (non-member of A)')
    })

    test('404 (unknown tenant): random-UUID tenant_id -> {error: string}', async () => {
      const unknownTenantToken = await login({ ...PERSONAS.A, tenantId: crypto.randomUUID() })
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${unknownTenantToken}` },
      })
      assertErrorEnvelope(res, 404, 'unknown tenant (random UUID tenant_id)')
    })
  })

  test.describe('PATCH /memberships/{user_id} status matrix (persona A, admin)', () => {
    // Forced back to `active` before the matrix runs, not only after it: a `finally`
    // cannot survive a killed process, so a crashed earlier run would otherwise hand
    // this shared environment a suspended row that every later spec inherits. The
    // no-op arm of the store returns 200 without writing or auditing, so this is
    // free when the row is already clean.
    test.beforeAll(async () => {
      await setMembershipStatus(token, PATCH_SUBJECT, 'active')
    })

    // Belt to the round-trip's braces. A killed worker never reaches a `finally`,
    // and Playwright replays a describe's hooks on retry — so the seeded value is
    // restated here as well as there, and `active` IS this row's seeded value
    // (db/seed.dev.sql), not merely the state it happened to be in.
    test.afterAll(async () => {
      await setMembershipStatus(token, PATCH_SUBJECT, 'active')
    })

    test('200 round-trip: suspend then reactivate, five-key body both ways', async () => {
      try {
        const suspended = await setMembershipStatus(token, PATCH_SUBJECT, 'suspended')
        expect(Object.keys(suspended).sort(), 'PATCH answers a list element').toEqual(MEMBERSHIP_KEYS)
        expect(suspended.user_id, 'PATCH answers the row it was asked for').toBe(PATCH_SUBJECT)
        expect(suspended.status).toBe('suspended')

        // The write LANDED — read it back through the list rather than trusting the
        // response body, which a handler could synthesize without touching the row.
        const listed = await rawFetch('/api/tenancy/v1/memberships', { headers: { Authorization: `Bearer ${token}` } })
        const row = ((listed.body as { memberships: Membership[] }).memberships ?? []).find((m) => m.user_id === PATCH_SUBJECT)
        expect(row?.status, 'the suspension is visible on the list').toBe('suspended')
      } finally {
        const restored = await setMembershipStatus(token, PATCH_SUBJECT, 'active')
        expect(restored.status, 'the shared environment must be left as it was found').toBe('active')
      }
    })

    test('403: a non-admin member cannot change a status', async () => {
      // A seeded PREPARER's token. login() sends {subject, role:'authenticated',
      // tenant_id} and the domain role resolves server-side from `memberships`, so a
      // spread of PERSONAS.A with the subject overridden is the whole fixture — the
      // same shape the /me 403 case above already uses.
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const res = await rawFetch(`/api/tenancy/v1/memberships/${PATCH_SUBJECT}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { status: 'suspended' },
      })
      assertErrorEnvelope(res, 403, 'seeded preparer PATCHing a membership status')
    })

    test('404: a syntactically valid but unknown user_id', async () => {
      const res = await rawFetch(`/api/tenancy/v1/memberships/${crypto.randomUUID()}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { status: 'suspended' },
      })
      assertErrorEnvelope(res, 404, 'PATCH an unknown user_id')
    })

    test('400: an out-of-vocabulary status, checked BEFORE the path id', async () => {
      // Deliberately paired with an unknown id: the handler validates the body first,
      // so this must read 400 rather than 404 — that ordering is the assertion.
      const res = await rawFetch(`/api/tenancy/v1/memberships/${crypto.randomUUID()}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { status: 'invited' },
      })
      assertErrorEnvelope(res, 400, 'PATCH with status "invited" and an unknown id')
    })
  })
})
