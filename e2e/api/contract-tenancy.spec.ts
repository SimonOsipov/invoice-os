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
//   - the subject is never …0001/…0002, each tenant's seeded admin — a tenant
//     stranded at zero active admins needs a superuser to recover;
//   - the row is forced back to `active` on the way in AND on the way out, in a
//     `finally`, so neither a mid-assertion failure nor a killed prior run can
//     leave it dirty. The api suite finishes before the topology job starts
//     (`needs: e2e` in dev-env.yml), and topology's roles.spec.ts asserts exact roster content.
//
// Four properties are proven against the DEPLOYED gateway:
//   - Happy-path status + shape (persona A, Core AC 1): /me -> 200 +
//     {tenant:{id,name,kind}, user:{id,role,display_name,email}}; /memberships -> 200 +
//     {memberships:[{user_id,role,status,display_name,email,client_access},...]}.
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
//     above: each seeded tenant has exactly ONE seeded admin and it is the sign-in
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
import {
  createEntity,
  grantMembership,
  listEntities,
  login,
  memberships,
  rawFetch,
  registerFormAccount,
  registerFresh,
  me,
  setMemberClients,
  setMembershipStatus,
  signInSession,
  subjectOf,
  PERSONAS,
  type Membership,
} from './client'
import { assertErrorEnvelope } from './contract-helpers'
import { freshTin } from './fixtures'

// The six keys internal/tenancy's MemberAccess serializes, sorted — the exact-key-set
// idiom the /me test below already uses, applied per row.
const MEMBERSHIP_KEYS = ['client_access', 'display_name', 'email', 'role', 'status', 'user_id']

const ADMIN_SUBJECT = 'c0000000-0000-0000-0000-000000000001'
// Seeded limited reviewer (db/seed.dev.sql): works on two clients, not all_clients.
const LIMITED_REVIEWER = 'c0000000-0000-0000-0000-000000000014'
const INHOUSE_REVIEWER_SUBJECT = 'c0000000-0000-0000-0000-000000000008'
// internal/tenancy/tenancy.go statusForErr: ErrClientAccessNotPermitted, ErrClientAccessAdmin,
// ErrUnknownClient, ErrClientAccessNotFirm.
const NOT_ADMIN_CLIENTS = "only an admin can choose a member's clients"
const ADMIN_SEES_ALL = 'an admin sees every client'
const UNKNOWN_CLIENT = 'a client in the list is not in this workspace'
const NOT_FIRM = 'client access applies to accounting firm workspaces only'

// Seeded subjects (db/seed.dev.sql), never a tenant's seeded admin. Every active
// seeded member can sign in, and the mint takes any subject, so a leaked
// suspension does not block the login — but since AUDIT-10 it hands that persona a
// session whose every tenant-scoped call refuses with 403, GET /v1/me excepted, until
// the next deploy re-seeds. A leak is that much more expensive than it used to be.
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
    test('/me -> 200 + {tenant:{id,name,kind}, user:{id,role,display_name,email}}', async () => {
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, '/me should return 200').toBe(200)

      const body = res.body as Record<string, unknown>
      expect(Object.keys(body).sort(), 'expected exactly the tenant/user top-level keys').toEqual(['tenant', 'user'])

      const tenant = body.tenant as Record<string, unknown>
      expect(Object.keys(tenant).sort(), 'expected exactly the tenant.{deletion_due_at,id,kind,name} keys').toEqual([
        'deletion_due_at',
        'id',
        'kind',
        'name',
      ])
      expect(tenant.deletion_due_at, 'a seeded tenant is not scheduled for deletion').toBeNull()
      // Known seeded values (db/seed.dev.sql) — same convention as
      // isolation.spec.ts's AC1: a real, resolvable identity, not just a
      // well-typed one.
      expect(tenant.id).toBe(PERSONAS.A.tenantId)
      expect(tenant.name).toBe('Okafor & Partners')
      expect(tenant.kind).toBe('firm')

      const user = body.user as Record<string, unknown>
      expect(Object.keys(user).sort(), 'expected exactly the user.{display_name,email,id,role} keys').toEqual([
        'display_name',
        'email',
        'id',
        'role',
      ])
      expect(user.id).toBe(PERSONAS.A.subject)
      expect(user.role).toBe('admin')
      // db/seed.dev.sql: persona A's membership row.
      expect(user.display_name).toBe('Chinedu Okafor')
      expect(user.email).toBe('c.okafor@okafor.ng')
    })

    test('/memberships -> 200 + {memberships:[{user_id,role,status,display_name,email,client_access},...]}, no pagination key', async () => {
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
        expect(Object.keys(m).sort(), `membership ${String(m.user_id)}: expected exactly the six wire keys`).toEqual(MEMBERSHIP_KEYS)
        expect(typeof m.user_id, 'each membership.user_id should be a string').toBe('string')
        expect(typeof m.role, 'each membership.role should be a string').toBe('string')
        expect(typeof m.status, 'each membership.status should be a string').toBe('string')
        expect(m.display_name === null || typeof m.display_name === 'string', 'display_name should be a string or null').toBe(true)
        expect(m.email === null || typeof m.email === 'string', 'email should be a string or null').toBe(true)
        expect(
          m.client_access === null || m.client_access === 'all' || Array.isArray(m.client_access),
          'client_access should be "all", an id list or null',
        ).toBe(true)
      }

      // List-shape boundary (Core AC 4): memberships is a flat,
      // non-paginated list — unlike portfolio's list (M3-15-03), it must
      // NOT carry a pagination envelope.
      expect(body, 'memberships should have no pagination key').not.toHaveProperty('pagination')
    })
  })

  test.describe('client_access values (D12)', () => {
    async function clientIds(names: string[]): Promise<string[]> {
      const ids: string[] = []
      for (const name of names) {
        const { entities } = await listEntities(token, { q: name })
        const hit = entities.find((e) => e.name === name)
        expect(hit, `seeded client ${name}`).toBeTruthy()
        ids.push(hit!.id)
      }
      return ids.sort()
    }
    const byId = (rows: Membership[]) => new Map(rows.map((m) => [m.user_id, m.client_access]))

    test('an admin reads every row: admin and all-clients preparer "all", limited reviewer its two clients, in-house null', async () => {
      const { memberships: rows } = await memberships(token)
      const got = byId(rows)
      expect(got.get(ADMIN_SUBJECT)).toBe('all')
      expect(got.get(PREPARER_SUBJECT)).toBe('all')
      expect(got.get(LIMITED_REVIEWER)).toEqual(await clientIds(['Adeyemi & Sons Trading Ltd', 'Chukwu Global Ventures Ltd']))
      const tokenB = await login(PERSONAS.B)
      for (const m of (await memberships(tokenB)).memberships) expect(m.client_access, `in-house ${m.user_id}`).toBeNull()
    })

    test('a firm non-admin reads client_access on its own row only and null on every other (pm BQ1)', async () => {
      for (const subject of [PREPARER_SUBJECT, LIMITED_REVIEWER]) {
        const own = await login({ ...PERSONAS.A, subject })
        const { memberships: rows } = await memberships(own)
        const mine = rows.find((m) => m.user_id === subject)!
        const adminView = byId((await memberships(token)).memberships).get(subject)
        expect(mine.client_access, `${subject} reads its own row`).toEqual(adminView)
        const others = rows.filter((m) => m.user_id !== subject)
        expect(others.length, 'the firm has other members').toBeGreaterThan(0)
        for (const m of others) expect(m.client_access, `${subject} reading ${m.user_id}`).toBeNull()
      }
    })
  })

  // The Settings Members tab renders this list. A null display_name, an unseeded role, or a
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
          `${label}: ${m.user_id} has no display_name — the Members tab would render a bare uuid`,
        ).toBe(true)
        expect(SEEDED_ROLES, `${label}: ${m.user_id} role`).toContain(m.role)
        expect(SEEDED_STATUSES, `${label}: ${m.user_id} status`).toContain(m.status)
      }
      expect(
        rows.some((m) => m.status === 'suspended'),
        `${label}: no row reports status "suspended" — the Members tab has no suspended row to render`,
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
      // Floor first: an empty list satisfies every per-row assertion below.
      expect(rows.length, 'in-house: the roster should not be empty').toBeGreaterThan(0)
      for (const m of rows) {
        expect(
          Object.keys(m).sort(),
          `${m.user_id}: the app takes the tenant from GET /v1/me, never from a roster row`,
        ).toEqual(MEMBERSHIP_KEYS)
      }
    })

    test('a newly admitted in-house reviewer resolves its seeded access role', async () => {
      // Proves ROLE RESOLUTION only: /auth/login mints for any subject, so signing in
      // proves nothing about membership.
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

    test('200 round-trip: suspend then reactivate, six-key body both ways', async () => {
      try {
        const suspended = await setMembershipStatus(token, PATCH_SUBJECT, 'suspended')
        expect(Object.keys(suspended).sort(), 'PATCH answers a list element').toEqual(MEMBERSHIP_KEYS)
        expect(suspended.user_id, 'PATCH answers the row it was asked for').toBe(PATCH_SUBJECT)
        expect(suspended.status).toBe('suspended')
        expect(suspended.client_access, 'a suspended member keeps its access').toBe('all')

        // The write LANDED — read it back through the list rather than trusting the
        // response body, which a handler could synthesize without touching the row.
        const listed = await rawFetch('/api/tenancy/v1/memberships', { headers: { Authorization: `Bearer ${token}` } })
        const row = ((listed.body as { memberships: Membership[] }).memberships ?? []).find((m) => m.user_id === PATCH_SUBJECT)
        expect(row?.status, 'the suspension is visible on the list').toBe('suspended')
      } finally {
        const restored = await setMembershipStatus(token, PATCH_SUBJECT, 'active')
        expect(restored.status, 'the shared environment must be left as it was found').toBe('active')
        expect(restored.client_access).toBe('all')
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

  test.describe('PUT /memberships/{user_id}/clients (D24)', () => {
    const err = (res: { body: unknown }) => (res.body as { error: string }).error

    test('PUT /memberships/{user_id}/clients: an admin sets a set, all and none', async () => {
      const { token: admin } = await registerFormAccount('clients-set')
      const e1 = await createEntity(admin, { name: `FIRMS-02 set ${freshTin()}`, tin: freshTin() })
      const e2 = await createEntity(admin, { name: `FIRMS-02 set ${freshTin()}`, tin: freshTin() })
      const tenantId = (await me(admin)).tenant.id
      const preparer = await registerFresh('clients-set-preparer')
      const userId = subjectOf((await signInSession(preparer.email, preparer.password)).access_token)
      await grantMembership({ user_id: userId, tenant_id: tenantId, role: 'preparer', display_name: 'Clients E2E Preparer', email: preparer.email })

      const put = (access: unknown) => setMemberClients(admin, userId, access)
      const read = async () => (await memberships(admin)).memberships.find((m) => m.user_id === userId)!.client_access

      const set = await put([e2.id, e1.id])
      expect(set.status, JSON.stringify(set.body)).toBe(200)
      expect(Object.keys(set.body as object).sort()).toEqual(MEMBERSHIP_KEYS)
      const expected = [e1.id, e2.id].sort()
      expect((set.body as Membership).client_access, 'the answer is sorted').toEqual(expected)
      expect(await read(), 'GET agrees').toEqual(expected)

      const all = await put('all')
      expect(all.status, JSON.stringify(all.body)).toBe(200)
      expect((all.body as Membership).client_access).toBe('all')
      expect(await read()).toBe('all')

      const none = await put([])
      expect(none.status, JSON.stringify(none.body)).toBe(200)
      expect((none.body as Membership).client_access).toEqual([])
      expect(await read()).toEqual([])
    })

    test('PUT /memberships/{user_id}/clients: refusals', async () => {
      const { token: admin } = await registerFormAccount('clients-refuse')
      const tenantId = (await me(admin)).tenant.id
      const adminId = subjectOf(admin)
      const preparer = await registerFresh('clients-refuse-preparer')
      const userId = subjectOf((await signInSession(preparer.email, preparer.password)).access_token)
      await grantMembership({ user_id: userId, tenant_id: tenantId, role: 'preparer', display_name: 'Clients E2E Preparer', email: preparer.email })
      const read = async () => (await memberships(admin)).memberships.find((m) => m.user_id === userId)!.client_access
      const before = await read()

      // The preparer's own token: mock login with its subject and the workspace.
      const preparerToken = await login({ ...PERSONAS.A, subject: userId, tenantId })
      const forbidden = await setMemberClients(preparerToken, userId, 'all')
      assertErrorEnvelope(forbidden, 403, 'preparer PUT')
      expect(err(forbidden)).toBe(NOT_ADMIN_CLIENTS) // ErrClientAccessNotPermitted

      const onSelf = await setMemberClients(admin, adminId, 'all')
      assertErrorEnvelope(onSelf, 409, 'admin PUT on itself')
      expect(err(onSelf)).toBe(ADMIN_SEES_ALL) // ErrClientAccessAdmin

      assertErrorEnvelope(await setMemberClients(admin, crypto.randomUUID(), 'all'), 404, 'unknown member')

      // An entity of 1111 is not in this workspace.
      const { entities } = await listEntities(await login(PERSONAS.A), { limit: 1 })
      const foreign = await setMemberClients(admin, userId, [entities[0].id])
      assertErrorEnvelope(foreign, 400, 'a client of another workspace')
      expect(err(foreign)).toBe(UNKNOWN_CLIENT) // ErrUnknownClient
      expect(await read(), 'the stored set is unchanged').toEqual(before)

      for (const bad of ['some', {}, ['x']]) {
        assertErrorEnvelope(await setMemberClients(admin, userId, bad), 400, `body ${JSON.stringify(bad)}`)
      }
      expect(await read(), 'a malformed body stores nothing').toEqual(before)

      const tokenB = await login(PERSONAS.B)
      const inHouse = await setMemberClients(tokenB, INHOUSE_REVIEWER_SUBJECT, [crypto.randomUUID()])
      assertErrorEnvelope(inHouse, 409, 'in-house workspace')
      expect(err(inHouse)).toBe(NOT_FIRM) // ErrClientAccessNotFirm
    })
  })
})
