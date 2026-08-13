// The workflow-role contract, over the wire — through the SAME typed seam
// (api/client.ts) every api/ spec shares. Error paths go through rawFetch, so the
// exact status and {error:…} envelope are observable; apiFetch resolves on ANY 2xx,
// so the create test uses rawFetch too, to see the 201 rather than merely a success.
//
// This is the only guard against a desc/description slip, a null `members`, or a lost
// staffing order reaching the SPA: roles.test.ts covers the pure functions, not the wire.
//
// WRITES to a shared environment, and workflow_roles is one of the tables the per-PR reset
// deliberately EXCLUDES (resetTables), so these rows really do outlive the run that made
// them. Following contract-tenancy.spec.ts:
//   - titles come from freshRoleTitle(), unique per run — here for identifiability in the
//     cleanup sweep, not constraint safety: duplicate titles are legal;
//   - staffing targets are SEED-ONLY subjects, never …0001/…0002 (the sign-in personas);
//   - no assertion depends on a key VALUE. The server suffixes a colliding key to -2, so a
//     re-run cannot predict what it minted;
//   - no assertion depends on the list's LENGTH or on which other roles exist. Nothing
//     seeds workflow_roles, it is not in resetTables, and DELETE is soft — rows accumulate
//     and keys are never reclaimed.
//
// Deliberately NOT covered here:
//   - 401 missing/malformed Bearer: already proven cross-surface by auth-contract.spec.ts.
//   - malformed/duplicate member uuids, body caps, 409 concurrent create: pure store logic
//     the Go suite owns, and 409 is unreachable at workers: 1.
//   - the PUT's CORS grant: rawFetch is Node fetch with no Origin, so no preflight is ever
//     issued and rawFetch exposes no response headers. Proven instead by
//     internal/gateway/cors_test.go, against the literal PUT and through the /api/ mount.
import { test, expect } from '@playwright/test'
import {
  createApprovalPolicy,
  createWorkflowRole,
  deleteApprovalPolicy,
  deleteWorkflowRole,
  getApprovalPolicy,
  listApprovalPolicies,
  listWorkflowRoles,
  login,
  publishApprovalPolicy,
  putApprovalPolicyDraft,
  rawFetch,
  staffWorkflowRole,
  updateWorkflowRole,
  PERSONAS,
  type ApprovalPolicy,
} from './client'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'
import { freshPolicyName, freshRoleTitle } from './fixtures'

// The four keys internal/approval's Role serializes, sorted. No omitempty on any field, so
// the set stays four even when desc is "" and members is empty.
const ROLE_KEYS = ['desc', 'key', 'members', 'title']

// Seed-only staffing targets (db/seed.dev.sql). …0006 is a PREPARER — a legal target by
// design, asserted here on the deployed build rather than only in Go.
const REVIEWER_TARGET = 'c0000000-0000-0000-0000-000000000004'
const PREPARER_TARGET = 'c0000000-0000-0000-0000-000000000006'
// The seeded preparer that can sign in — this suite's canonical 403 caller.
const PREPARER_CALLER = 'c0000000-0000-0000-0000-000000000003'

// The shape all five routes answer. `members` is checked on the PARSED body, which is
// enough at this boundary: JS distinguishes [] from null, unlike Go's serializer test.
function expectRoleShape(role: unknown, label: string): Record<string, unknown> {
  const r = role as Record<string, unknown>
  expect(Object.keys(r).sort(), `${label}: expected exactly the four Role keys`).toEqual(ROLE_KEYS)
  expect(Array.isArray(r.members), `${label}: members should be an array`).toBe(true)
  expect(r.members, `${label}: members should never be null`).not.toBeNull()
  // Restated by name, though the key-set equality already covers them: `description` is the
  // slip this file exists to catch, and `id` must never leave the store.
  expect(r, `${label}: no description key`).not.toHaveProperty('description')
  expect(r, `${label}: no id key`).not.toHaveProperty('id')
  return r
}

// assertErrorEnvelope plus the one claim it does not make: statusForErr hand-writes its
// messages so the "approval: " sentinel prefix never reaches the SPA as the reason.
function assertWireError(res: RawResult, status: number, label: string): void {
  assertErrorEnvelope(res, status, label)
  const body = res.body as Record<string, unknown>
  expect(String(body.error), `${label}: the message should carry no sentinel prefix`).not.toContain('approval: ')
}

test.describe('workflow-role contract (API E2E, over the deployed gateway)', () => {
  let token: string
  const createdKeys: string[] = []

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  // Best-effort sweep, idempotent on purpose: hooks replay on retry (retries: 1 in CI) and
  // the delete test already removed its own role, so a 404 here is expected and must never
  // mask a real assertion failure. This SOFT-deletes — every swept row survives with
  // deleted_at set, invisible to every list and every by-key path. The environment is left
  // free of live probes, not free of probe rows.
  test.afterAll(async () => {
    for (const key of createdKeys) {
      try {
        await deleteWorkflowRole(token, key)
      } catch {
        // already deleted, or never created
      }
    }
  })

  // No desc sent, so `desc` is genuinely absent on the wire. Registers the runtime key for
  // the sweep — the key is only ever learned, never predicted.
  async function createProbe() {
    const role = await createWorkflowRole(token, { title: freshRoleTitle() })
    createdKeys.push(role.key)
    return role
  }

  test('GET -> 200 {workflow_roles:[{desc,key,members,title}…]}, no pagination key', async () => {
    const probe = await createProbe()

    const res = await rawFetch('/api/invoice/v1/workflow-roles', {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(res.status, 'the list should return 200').toBe(200)

    const body = res.body as Record<string, unknown>
    expect(Object.keys(body).sort(), 'expected exactly the workflow_roles key').toEqual(['workflow_roles'])
    // A flat list — unlike portfolio's, it carries no pagination envelope.
    expect(body, 'the list should have no pagination key').not.toHaveProperty('pagination')
    expect(Array.isArray(body.workflow_roles), 'workflow_roles should be an array').toBe(true)

    const roles = body.workflow_roles as Array<Record<string, unknown>>
    // Justified by the probe just created, not by seed content — nothing seeds this table.
    // Also what stops the per-element loop below passing vacuously.
    expect(roles.length, 'the probe this test created is in the list').toBeGreaterThan(0)
    for (const role of roles) {
      expectRoleShape(role, `listed role ${String(role.key)}`)
    }
    expect(
      roles.find((r) => r.key === probe.key),
      'the probe is addressable by its runtime key',
    ).toBeDefined()
  })

  test('POST -> 201 + the four-key role, desc present as "" when unsent', async () => {
    const res = await rawFetch('/api/invoice/v1/workflow-roles', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { title: freshRoleTitle() },
    })
    expect(res.status, 'create should return 201').toBe(201)

    const role = expectRoleShape(res.body, 'created role')
    createdKeys.push(String(role.key))

    expect(role.desc, 'desc should be an explicit empty string, never absent').toBe('')
    expect(role.members, 'a new role is unstaffed').toEqual([])
    // Shape only: a colliding title suffixes the key to -2, so no literal is assertable.
    expect(String(role.key), 'key should be a slug').toMatch(/^[a-z0-9-]+$/)
  })

  test('staffing order round-trips, and a preparer is a legal target', async () => {
    const probe = await createProbe()

    const staffed = await staffWorkflowRole(token, probe.key, [REVIEWER_TARGET, PREPARER_TARGET])
    expectRoleShape(staffed, 'staffed role')
    expect(staffed.members, 'members come back in the submitted order').toEqual([REVIEWER_TARGET, PREPARER_TARGET])

    const reordered = await staffWorkflowRole(token, probe.key, [PREPARER_TARGET, REVIEWER_TARGET])
    expectRoleShape(reordered, 'restaffed role')
    expect(reordered.members, 'a reorder is a whole-set replace').toEqual([PREPARER_TARGET, REVIEWER_TARGET])

    // Read back through the list rather than trusting the response body, which a handler
    // could echo without persisting `ord`.
    const listed = await listWorkflowRoles(token)
    expect(
      listed.workflow_roles.find((r) => r.key === probe.key)?.members,
      'the submitted order persisted',
    ).toEqual([PREPARER_TARGET, REVIEWER_TARGET])
  })

  test('rename keeps the key and answers a full Role', async () => {
    const probe = await createProbe()
    await staffWorkflowRole(token, probe.key, [PREPARER_TARGET])

    const nextTitle = freshRoleTitle()
    const renamed = await updateWorkflowRole(token, probe.key, { title: nextTitle })
    expectRoleShape(renamed, 'renamed role')
    // A runtime comparison, never a literal: the key is minted once, at create, and a
    // sealed policy step may already name it.
    expect(renamed.key, 'a rename never re-mints the key').toBe(probe.key)
    expect(renamed.title, 'the new title is answered').toBe(nextTitle)
    // The PATCH answers a FULL Role — which is what lets the SPA swap the card wholesale.
    expect(renamed.members, 'a rename does not blank staffing').toEqual([PREPARER_TARGET])
  })

  test('delete removes the role from the list', async () => {
    const probe = await createProbe()

    const deleted = await deleteWorkflowRole(token, probe.key)
    expectRoleShape(deleted, 'deleted role')
    expect(deleted.key, 'delete answers the row it was asked for').toBe(probe.key)
    // No members assertion: DeleteRole answers [] even for a staffed role, by design.

    const listed = await listWorkflowRoles(token)
    expect(
      listed.workflow_roles.find((r) => r.key === probe.key),
      'a soft-deleted role is invisible to the list',
    ).toBeUndefined()
  })

  test.describe('error envelopes', () => {
    let probeKey: string

    // A LIVE key, deliberately: staffing reads the role row before it ever reaches the
    // members FK, so the unknown-user_id case below would 404 on a random slug and prove
    // nothing while still passing.
    test.beforeAll(async () => {
      probeKey = (await createProbe()).key
    })

    test('400: a whitespace-only title', async () => {
      // Proves the server TRIMS what the client sent — invisible above the wire.
      const res = await rawFetch('/api/invoice/v1/workflow-roles', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { title: '   ' },
      })
      assertWireError(res, 400, 'POST a whitespace-only title')
    })

    test('400: no field sent, checked BEFORE the path key', async () => {
      // Paired with an unknown key deliberately: body validation runs first, so this must
      // read 400 rather than 404 — that ordering IS the assertion.
      const res = await rawFetch(`/api/invoice/v1/workflow-roles/${crypto.randomUUID()}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: {},
      })
      assertWireError(res, 400, 'PATCH {} at an unknown key')
    })

    test('404: a syntactically valid but unknown key', async () => {
      const res = await rawFetch(`/api/invoice/v1/workflow-roles/${crypto.randomUUID()}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { title: freshRoleTitle() },
      })
      assertWireError(res, 404, 'PATCH an unknown key')
    })

    test('400: members null, checked BEFORE the path key', async () => {
      // Unexpressible through the typed wrapper, and unobservable anywhere but the JSON
      // boundary: a nil slice means unstaff at the store, so without this check {} would
      // silently wipe a role's staffing.
      const res = await rawFetch(`/api/invoice/v1/workflow-roles/${crypto.randomUUID()}/members`, {
        method: 'PUT',
        headers: { Authorization: `Bearer ${token}` },
        body: { members: null },
      })
      assertWireError(res, 400, 'PUT {"members":null} at an unknown key')
    })

    test('400: a user_id with no membership, on a LIVE key', async () => {
      const res = await rawFetch(`/api/invoice/v1/workflow-roles/${probeKey}/members`, {
        method: 'PUT',
        headers: { Authorization: `Bearer ${token}` },
        body: { members: [crypto.randomUUID()] },
      })
      assertWireError(res, 400, 'PUT a user_id with no membership')
    })

    test('403: a non-admin cannot write, but the same token may still LIST', async () => {
      // login() reads only subject + tenantId, so a spread of PERSONAS.A with the subject
      // overridden is the whole fixture — the domain role resolves server-side.
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_CALLER })
      const res = await rawFetch(`/api/invoice/v1/workflow-roles/${probeKey}/members`, {
        method: 'PUT',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { members: [] },
      })
      assertWireError(res, 403, 'a seeded preparer PUTting staffing')
      // Pinned literally: this is the sentence the SPA renders as the reason.
      expect((res.body as Record<string, unknown>).error).toBe('only an admin can change workflow roles')

      // The read is deliberately ungated — any caller with a tenant claim may list.
      const listed = await rawFetch('/api/invoice/v1/workflow-roles', {
        headers: { Authorization: `Bearer ${preparerToken}` },
      })
      expect(listed.status, 'a preparer may LIST workflow roles').toBe(200)
      expect(
        Array.isArray((listed.body as Record<string, unknown>).workflow_roles),
        'the ungated list still answers an array',
      ).toBe(true)
    })
  })
})

// The approval-policy half of the same seam (APPR-05), as a SIBLING describe — the
// workflow-role block above is untouched. Same shared-environment discipline as that block
// (approval_policies is excluded from db.Reset too, so nothing here may depend on a list
// LENGTH or on a predicted id), plus three rules of its own:
//
//   - approval_policy_versions_one_active is ON (tenant_id), so EVERY publish in this file
//     deactivates whatever the previous test published. No test may assert that a policy
//     published by an EARLIER test is still active — only its own, off its own response
//     body, never a follow-up GET.
//   - `steps` is the policy's HIGHEST version's tree, which is not necessarily the sealed
//     one: the moment a draft is opened it is an unpublished proposal, and there is no
//     endpoint for the in-force tree at all (docs/approvals.md §10). Never described here
//     as "the active tree".
//   - every test mints its own policy inline, because a CI retry replays the whole test:
//     a shared published policy would answer 409 the second time round.
//
// published_at is asserted as a SHAPE (Z-suffixed RFC3339, 0-6 fractional digits) plus an
// epoch-millis window taken around the publish call. That is deliberately all it can prove:
// the store's at.UTC() normalisation is NOT observable over the wire from a UTC-TZ
// deployment — a plain time.Time marshals to Z there too — so proving .UTC() is the Go
// suite's job, not this file's. Nothing here derives from the runner's local clock or
// locale (Date.parse of a Z-suffixed string is TZ-free; toISOString / toLocale* / getDate()
// would be green on CI and red at home), and published_at is never string-compared against
// another API timestamp — every other one marshals in pgx's time.Local.

// The eight keys internal/approval's Policy serializes, sorted, and the five and ten of its
// two nested shapes. No omitempty on any field, and Policy/Step both carry a value-receiver
// MarshalJSON substituting [] for a nil lane, so every set is fixed whatever the content.
const POLICY_KEYS = ['id', 'name', 'scope', 'sealed', 'status', 'steps', 'version', 'versions']
const POLICY_VERSION_KEYS = ['is_active', 'published_at', 'published_by', 'sealed', 'version']
const STEP_KEYS = [
  'cond_amount',
  'cond_op',
  'else',
  'id',
  'kind',
  'notify_channel',
  'notify_target',
  'sla_hours',
  'then',
  'workflow_role_key',
]

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
// Z-suffixed RFC3339 with 0-6 fractional digits: Postgres now() is microsecond precision
// and RFC3339Nano trims trailing zeros, so the width varies and must not be pinned.
const PUBLISHED_AT_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/

// A scope approval_policies_scope_check has no value for. normalizeScope refuses it above
// the transaction, which is what makes it a 400 rather than a 500.
const FOREIGN_SCOPE = 'Capex & fixed assets'

// Walks the tree, not just the roots: the ten-key claim has to hold at depth, which is
// exactly what Step's MarshalJSON re-entering itself is for.
function expectStepShape(step: unknown, label: string): Record<string, unknown> {
  const s = step as Record<string, unknown>
  expect(Object.keys(s).sort(), `${label}: expected exactly the ten Step keys`).toEqual(STEP_KEYS)
  expect(Array.isArray(s.then), `${label}: then should be an array, never null`).toBe(true)
  expect(Array.isArray(s.else), `${label}: else should be an array, never null`).toBe(true)
  const then = s.then as unknown[]
  const otherwise = s.else as unknown[]
  for (let i = 0; i < then.length; i++) expectStepShape(then[i], `${label}.then[${i}]`)
  for (let i = 0; i < otherwise.length; i++) expectStepShape(otherwise[i], `${label}.else[${i}]`)
  return s
}

// The shape all six routes answer. Checked on the PARSED body, which is enough at this
// boundary: JS distinguishes [] from null, unlike Go's serializer test.
function expectPolicyShape(policy: unknown, label: string): Record<string, unknown> {
  const p = policy as Record<string, unknown>
  expect(Object.keys(p).sort(), `${label}: expected exactly the eight Policy keys`).toEqual(POLICY_KEYS)
  expect(Array.isArray(p.steps), `${label}: steps should be an array, never null`).toBe(true)
  expect(Array.isArray(p.versions), `${label}: versions should be an array, never null`).toBe(true)
  const steps = p.steps as unknown[]
  for (let i = 0; i < steps.length; i++) expectStepShape(steps[i], `${label}.steps[${i}]`)
  const versions = p.versions as unknown[]
  for (let i = 0; i < versions.length; i++) {
    const v = versions[i] as Record<string, unknown>
    expect(
      Object.keys(v).sort(),
      `${label}.versions[${i}]: expected exactly the five PolicyVersion keys`,
    ).toEqual(POLICY_VERSION_KEYS)
  }
  return p
}

function collectStepIds(steps: unknown[], out: string[] = []): string[] {
  for (const raw of steps) {
    const s = raw as Record<string, unknown>
    out.push(String(s.id))
    collectStepIds(s.then as unknown[], out)
    collectStepIds(s.else as unknown[], out)
  }
  return out
}

test.describe('approval-policy contract (API E2E, over the deployed gateway)', () => {
  let token: string
  // Created once and swept, so the publish tests have a role to name. A step naming a role
  // is the only shape publish's gate lets through, so this cannot be skipped.
  let liveRoleKey: string
  const createdPolicyIds: string[] = []
  const createdRoleKeys: string[] = []
  // Tenant B's own rows, swept separately: the loop below deletes with A's token, which 404s
  // on a B row and would leave it live in an environment nothing resets.
  const createdPolicyIdsB: string[] = []

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    liveRoleKey = await createRoleProbe()
  })

  // Two sweeps, because this describe creates workflow ROLES as well as policies: a role
  // left behind stays live in an environment nothing resets. Best-effort and idempotent on
  // purpose — hooks replay on retry (retries: 1 in CI), the dangling-role test already
  // deleted its own role, and a second policy delete is 404 (deleted_at IS NULL is the
  // existence predicate), so a throw here is expected and must never mask a real assertion
  // failure. Deleting a policy also releases the tenant's active slot, so the environment
  // is left with no live probe governing anything.
  test.afterAll(async () => {
    for (const id of createdPolicyIds) {
      try {
        await deleteApprovalPolicy(token, id)
      } catch {
        // already deleted, or never created
      }
    }
    for (const key of createdRoleKeys) {
      try {
        await deleteWorkflowRole(token, key)
      } catch {
        // already deleted, or never created
      }
    }
    // B's rows need B's own token — see the array's declaration. Gated on length so the
    // common case never pays for a second sign-in.
    if (createdPolicyIdsB.length > 0) {
      const tokenB = await login(PERSONAS.B)
      for (const id of createdPolicyIdsB) {
        try {
          await deleteApprovalPolicy(tokenB, id)
        } catch {
          // already deleted, or never created
        }
      }
    }
  })

  // Registers the runtime id for the sweep — an id is only ever learned, never predicted.
  async function createPolicyProbe(): Promise<ApprovalPolicy> {
    const policy = await createApprovalPolicy(token, { name: freshPolicyName() })
    createdPolicyIds.push(policy.id)
    return policy
  }

  async function createRoleProbe(): Promise<string> {
    const role = await createWorkflowRole(token, { title: freshRoleTitle() })
    createdRoleKeys.push(role.key)
    return role.key
  }

  function publishRaw(id: string): Promise<RawResult> {
    return rawFetch(`/api/invoice/v1/approval-policies/${id}/publish`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
    })
  }

  test('GET -> 200 {approval_policies:[…]}, no pagination key', async () => {
    const probe = await createPolicyProbe()

    const res = await rawFetch('/api/invoice/v1/approval-policies', {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(res.status, 'the list should return 200').toBe(200)

    const body = res.body as Record<string, unknown>
    expect(Object.keys(body).sort(), 'expected exactly the approval_policies key').toEqual(['approval_policies'])
    // A flat list — like workflow roles and unlike portfolio, it carries no pagination envelope.
    expect(body, 'the list should have no pagination key').not.toHaveProperty('pagination')
    expect(Array.isArray(body.approval_policies), 'approval_policies should be an array').toBe(true)

    const policies = body.approval_policies as Array<Record<string, unknown>>
    // Justified by the probe just created, not by seed content — nothing seeds this table.
    // Also what stops the per-element loop below passing vacuously.
    expect(policies.length, 'the probe this test created is in the list').toBeGreaterThan(0)
    for (const policy of policies) {
      expectPolicyShape(policy, `listed policy ${String(policy.id)}`)
    }
    expect(
      policies.find((p) => p.id === probe.id),
      'the probe is addressable by its runtime id',
    ).toBeDefined()

    // The typed seam reaches the same envelope key the raw read just pinned.
    const listed = await listApprovalPolicies(token)
    expect(
      listed.approval_policies.find((p) => p.id === probe.id)?.name,
      'the typed list agrees with the raw one',
    ).toBe(probe.name)
  })

  test('POST -> 201 + a draft policy scoped to All invoices, carrying an open version 1', async () => {
    const res = await rawFetch('/api/invoice/v1/approval-policies', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { name: freshPolicyName() },
    })
    // Registered BEFORE any assertion: a failed one below would otherwise leak a live
    // policy into an environment nothing resets.
    const created = res.body as Record<string, unknown> | undefined
    const createdID = created?.id
    if (typeof createdID === 'string') createdPolicyIds.push(createdID)

    expect(res.status, 'create should return 201').toBe(201)
    const policy = expectPolicyShape(created, 'created policy')

    // No scope sent: normalizeScope reads "" as "the default", not as a value.
    expect(policy.scope, 'an unsent scope lands as the default').toBe('All invoices')
    expect(policy.status, 'a new policy is a draft').toBe('draft')
    expect(policy.sealed, 'a new policy is unsealed').toBe(false)
    expect(policy.steps, 'a new policy has no steps').toEqual([])

    // Create mints the policy AND its open draft version 1, so `versions` is never empty
    // and this policy's own row is the only one in it — a per-policy count, not a list length.
    expect(policy.version, 'the open draft is version 1').toBe(1)
    const versions = policy.versions as Array<Record<string, unknown>>
    expect(versions.length, 'exactly the one version create minted').toBe(1)
    expect(versions[0].version, 'create mints version one').toBe(1)
    expect(versions[0].sealed, 'the minted version is open').toBe(false)
    expect(versions[0].is_active, 'creating does not take the tenant active slot').toBe(false)
    expect(versions[0].published_at, 'published_at is an explicit null before publish').toBeNull()
    expect(versions[0].published_by, 'published_by is an explicit null before publish').toBeNull()
  })

  test('policy shape: exactly the eight keys, and a populated tree keeps [] never null', async () => {
    const probe = await createPolicyProbe()
    // A PUT before the GET is REQUIRED: a freshly created policy's draft is empty, so the
    // ten-key Step claim below would otherwise pass vacuously over nothing.
    await putApprovalPolicyDraft(token, probe.id, {
      steps: [
        {
          kind: 'condition',
          cond_op: '>',
          cond_amount: '1000.00',
          then: [{ kind: 'approval', workflow_role_key: liveRoleKey, sla_hours: 24 }],
          else: [],
        },
      ],
    })

    const res = await rawFetch(`/api/invoice/v1/approval-policies/${probe.id}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(res.status, 'the get should return 200').toBe(200)

    // expectPolicyShape walks every step at every depth, so the ten-key claim is made on
    // the nested approval too, not only the root.
    const policy = expectPolicyShape(res.body, 'fetched policy')
    const steps = policy.steps as Array<Record<string, unknown>>
    expect(steps.length, 'the tree just written is the top version tree').toBe(1)

    const root = steps[0]
    expect(root.kind).toBe('condition')
    expect(root.cond_op).toBe('>')
    // numeric(14,2) read back via ::text, so the scale survives the round trip.
    expect(root.cond_amount, 'cond_amount keeps its scale').toBe('1000.00')
    // The unused lane is where the [] substitution shows: absent would be a null here.
    expect(root.else, 'an empty lane is [] never null').toEqual([])

    const nested = (root.then as Array<Record<string, unknown>>)[0]
    expect(nested.kind).toBe('approval')
    expect(nested.workflow_role_key, 'the role key round-trips').toBe(liveRoleKey)
    expect(nested.sla_hours, 'sla_hours round-trips as a number').toBe(24)
    // The fields this kind does not use are explicit nulls, not absent keys.
    expect(nested.cond_op, 'an unused field is an explicit null').toBeNull()
    expect(nested.notify_target, 'an unused field is an explicit null').toBeNull()
  })

  test('publish -> 200 with sealed and is_active true on the top version', async () => {
    const probe = await createPolicyProbe()
    await putApprovalPolicyDraft(token, probe.id, {
      steps: [{ kind: 'approval', workflow_role_key: liveRoleKey, sla_hours: 48 }],
    })

    // The window is taken around the call and read off THIS response. Never off a later
    // GET: one_active is tenant-wide, so a subsequent publish anywhere in the tenant would
    // have cleared is_active by then.
    const before = Date.now()
    const res = await publishRaw(probe.id)
    const after = Date.now()
    expect(res.status, 'publish should return 200').toBe(200)

    const policy = expectPolicyShape(res.body, 'published policy')
    expect(policy.status, 'a sealed top version reads published').toBe('published')
    expect(policy.sealed).toBe(true)

    const top = (policy.versions as Array<Record<string, unknown>>)[0]
    expect(top.version, 'versions come back newest first').toBe(policy.version)
    expect(top.sealed, 'publishing seals the version').toBe(true)
    expect(top.is_active, 'publishing takes the tenant active slot').toBe(true)
    expect(typeof top.published_at, 'published_at is a string once published').toBe('string')
    // Shape only — see this block's header: a UTC-TZ deployment cannot distinguish the
    // store's .UTC() from a plain marshal, so the Z suffix is the whole claim.
    expect(top.published_at, 'published_at is Z-suffixed RFC3339').toMatch(PUBLISHED_AT_RE)
    // Epoch millis, which is TZ-free. The window is wide on purpose: the server's clock is
    // not this process's, and only gross nonsense is being excluded.
    const stampedAt = Date.parse(top.published_at as string)
    expect(stampedAt, 'published_at is not stamped before the call').toBeGreaterThanOrEqual(before - 60_000)
    expect(stampedAt, 'published_at is not stamped after the call').toBeLessThanOrEqual(after + 60_000)
    expect(top.published_by, 'published_by is the calling admin, taken server-side').toBe(PERSONAS.A.subject)
  })

  test('409: re-publishing an already-sealed version', async () => {
    const probe = await createPolicyProbe()
    await putApprovalPolicyDraft(token, probe.id, {
      steps: [{ kind: 'approval', workflow_role_key: liveRoleKey }],
    })
    const published = await publishApprovalPolicy(token, probe.id)
    expect(published.sealed, 'the first publish sealed the draft').toBe(true)

    // Sealing mints no successor, so there is now no unsealed version to publish. The code
    // is the claim: a 200 here would mean a second seal, and a 404 would mean the policy
    // was lost.
    assertWireError(await publishRaw(probe.id), 409, 're-publishing a sealed policy')
  })

  test('a draft edit after publish yields a NEW version number', async () => {
    const probe = await createPolicyProbe()
    await putApprovalPolicyDraft(token, probe.id, {
      steps: [{ kind: 'approval', workflow_role_key: liveRoleKey }],
    })
    const published = await publishApprovalPolicy(token, probe.id)
    const sealedVersion = published.version
    expect(published.sealed, 'the publish sealed it').toBe(true)

    // No open draft survives a publish, so this PUT forks max+1 rather than rewriting.
    const forked = await putApprovalPolicyDraft(token, probe.id, { steps: [] })
    expect(forked.version, 'the edit forked a new version').toBe(sealedVersion + 1)
    expect(forked.sealed, 'the fork is open').toBe(false)
    expect(forked.status, 'and the policy reads draft again').toBe('draft')

    const fetched = await getApprovalPolicy(token, probe.id)
    expect(fetched.version, 'the fork is now the top version').toBe(sealedVersion + 1)
    expect(fetched.sealed).toBe(false)
    expect(fetched.versions[0].version, 'versions come back newest first').toBe(sealedVersion + 1)
    expect(fetched.versions[0].published_at, 'an open version carries no publish stamp').toBeNull()
    // Both versions survive the fork. Deliberately no is_active claim on the sealed one:
    // the active slot is tenant-wide, so any later publish — here or in a test inserted
    // between these two calls — clears it.
    const previous = fetched.versions.find((v) => v.version === sealedVersion)
    expect(previous, 'the sealed version survives the fork').toBeDefined()
    expect(previous?.sealed, 'sealing is permanent').toBe(true)
  })

  test('409: publishing a step that names no live role', async () => {
    const doomedRoleKey = await createRoleProbe()
    const probe = await createPolicyProbe()
    await putApprovalPolicyDraft(token, probe.id, {
      steps: [{ kind: 'approval', workflow_role_key: doomedRoleKey }],
    })
    // The dangling-role gate is at publish's door, not the draft's, so naming a LIVE role
    // and killing it afterwards is the only route to it.
    await deleteWorkflowRole(token, doomedRoleKey)

    assertWireError(await publishRaw(probe.id), 409, 'publishing a step naming a deleted role')
  })

  test('409: publishing a condition with two empty lanes', async () => {
    const probe = await createPolicyProbe()
    // cond_op and cond_amount are mandatory on a condition whatever its lanes hold: without
    // them the PUT 400s and this test never reaches the gate it exists to prove.
    const draft = await putApprovalPolicyDraft(token, probe.id, {
      steps: [{ kind: 'condition', cond_op: '>', cond_amount: '1000.00', then: [], else: [] }],
    })
    expect(draft.steps.length, 'an empty-lane condition is a legal DRAFT').toBe(1)

    assertWireError(await publishRaw(probe.id), 409, 'publishing a condition with two empty lanes')
  })

  test('200: an empty policy publishes', async () => {
    const probe = await createPolicyProbe()

    // Create already minted the open draft, so there IS something to seal — a policy with
    // zero steps is publishable by design, and this is what separates "nothing to publish"
    // from "nothing in it".
    const res = await publishRaw(probe.id)
    expect(res.status, 'an empty policy publishes').toBe(200)

    const policy = expectPolicyShape(res.body, 'published empty policy')
    expect(policy.steps, 'an empty tree stays []').toEqual([])
    const top = (policy.versions as Array<Record<string, unknown>>)[0]
    expect(top.sealed, 'the empty version is sealed').toBe(true)
    expect(top.is_active, 'and takes the tenant active slot').toBe(true)
  })

  test('400: a foreign scope on PUT draft', async () => {
    const probe = await createPolicyProbe()
    const res = await rawFetch(`/api/invoice/v1/approval-policies/${probe.id}/draft`, {
      method: 'PUT',
      headers: { Authorization: `Bearer ${token}` },
      body: { scope: FOREIGN_SCOPE, steps: [] },
    })
    assertWireError(res, 400, 'PUT a scope the column has no value for')
  })

  test('400: a foreign scope on POST', async () => {
    // Pinned over the wire rather than through the typed seam, which rejects on 400 and 500
    // alike: normalizeScope running BELOW the transaction would let
    // approval_policies_scope_check fire instead, and a 23514 carries no sentinel, so it
    // would answer 500 — invisible to apiFetch, and the reason this case exists.
    const res = await rawFetch('/api/invoice/v1/approval-policies', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { name: freshPolicyName(), scope: FOREIGN_SCOPE },
    })
    assertWireError(res, 400, 'POST a scope the column has no value for')
  })

  test('400: steps null, checked BEFORE the path id', async () => {
    // Paired with an unknown id deliberately: the presence check runs first, so this must
    // read 400 rather than 404 — that ordering IS the assertion. Unexpressible through the
    // typed wrapper, and it matters because a nil slice means clear-the-tree at the store,
    // so without the check a {} body would silently wipe a policy's steps.
    const res = await rawFetch(`/api/invoice/v1/approval-policies/${crypto.randomUUID()}/draft`, {
      method: 'PUT',
      headers: { Authorization: `Bearer ${token}` },
      body: { steps: null },
    })
    assertWireError(res, 400, 'PUT {"steps":null} at an unknown id')
  })

  test('403: a preparer cannot write, but the same token may still LIST', async () => {
    const probe = await createPolicyProbe()
    // A NON-empty draft, so the untouched check at the end is not vacuous: the preparer
    // sends steps: [], which is the clear-the-tree body, so a permission check running
    // after the write would leave zero steps behind rather than this one.
    await putApprovalPolicyDraft(token, probe.id, {
      steps: [{ kind: 'approval', workflow_role_key: liveRoleKey }],
    })
    // login() reads only subject + tenantId, so a spread of PERSONAS.A with the subject
    // overridden is the whole fixture — the domain role resolves server-side.
    const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_CALLER })

    const res = await rawFetch(`/api/invoice/v1/approval-policies/${probe.id}/draft`, {
      method: 'PUT',
      headers: { Authorization: `Bearer ${preparerToken}` },
      body: { steps: [] },
    })
    assertWireError(res, 403, 'a seeded preparer PUTting a policy draft')
    // Pinned literally, and it is NOT the workflow-role sentence above: the policy seam has
    // its own mapper, and this is the sentence the SPA renders as the reason.
    expect((res.body as Record<string, unknown>).error).toBe('only an admin can change approval policies')

    // The read is deliberately ungated — any caller with a tenant claim may list.
    const listed = await rawFetch('/api/invoice/v1/approval-policies', {
      headers: { Authorization: `Bearer ${preparerToken}` },
    })
    expect(listed.status, 'a preparer may LIST approval policies').toBe(200)
    expect(
      Array.isArray((listed.body as Record<string, unknown>).approval_policies),
      'the ungated list still answers an array',
    ).toBe(true)

    // The refusal wrote nothing: requireActiveAdmin is the store's first statement, above
    // any read of the policy row.
    const untouched = await getApprovalPolicy(token, probe.id)
    expect(untouched.steps.length, 'a refused write leaves the draft alone').toBe(1)
  })

  test('client-supplied step ids are ignored — every returned id is a server uuid', async () => {
    const probe = await createPolicyProbe()
    // Sent through rawFetch because the typed input cannot express an id at all — stepInput
    // declares no id field, so these are dropped at decode rather than rejected.
    const res = await rawFetch(`/api/invoice/v1/approval-policies/${probe.id}/draft`, {
      method: 'PUT',
      headers: { Authorization: `Bearer ${token}` },
      body: {
        steps: [
          {
            id: 'wn1000',
            kind: 'condition',
            cond_op: '>=',
            cond_amount: '500.00',
            then: [{ id: 'wn1001', kind: 'approval', workflow_role_key: liveRoleKey }],
            else: [],
          },
        ],
      },
    })
    expect(res.status, 'the draft write should return 200').toBe(200)

    const fetched = await getApprovalPolicy(token, probe.id)
    const ids = collectStepIds(fetched.steps)
    // Both depths, or the loop below could pass over an empty tree.
    expect(ids.length, 'both steps came back').toBe(2)
    for (const id of ids) {
      expect(id, 'a step id is a server-minted uuid').toMatch(UUID_RE)
    }
    // Restated by name, though the uuid pattern already covers them: a client-supplied id
    // reaching the store is the slip this test exists to catch.
    expect(ids, 'no client-supplied root id survived').not.toContain('wn1000')
    expect(ids, 'no client-supplied nested id survived').not.toContain('wn1001')
  })

  // APPR-09-08 AC-7. The shape api/isolation.spec.ts AC3 uses for portfolio: an owner
  // positive control, 404 rather than 403 (RLS row-invisibility, not authz — Decision A9),
  // and an owner re-read proving zero side effects.
  //
  // WHAT THIS ADDS, stated honestly. The DB layer already proves cross-tenant get/delete/put/
  // publish, with controls of its own — internal/approval/policy_crud_test.go's
  // TestPolicy_CrossTenantGetIsNotFound and policy_delete_test.go's
  // TestDeletePolicy_CrossTenantIsNotFound. Those drive the store against a pool the test
  // wires by hand. What had no proof anywhere is the same refusal THROUGH THE DEPLOYED
  // GATEWAY: the JWT → tenant-claim → RLS path the SPA actually calls. api/isolation.spec.ts
  // makes exactly that claim for portfolio and stops at /v1/me and /v1/memberships for
  // tenancy; approval policies were outside both.
  //
  // Three things make the refusal REAL rather than incidental:
  //   - B is an ACTIVE ADMIN of her own tenant (api/isolation.spec.ts AC1 pins
  //     meB.user.role === 'admin'), so requireActiveAdmin cannot be what stops her — the
  //     403 sentence this file pins elsewhere is a different refusal;
  //   - the message is pinned, which separates an RLS 404 from a route-level one;
  //   - B creates her own policy first, or the absence check would pass vacuously against an
  //     empty list — nothing seeds this table.
  test('AC-7: tenant B can neither read nor delete tenant A policy — 404 not 403, and A row untouched', async () => {
    const probeA = await createPolicyProbe()
    const tokenB = await login(PERSONAS.B)

    // Registered for the B sweep BEFORE any assertion below can abort the test.
    const probeB = await createApprovalPolicy(tokenB, { name: freshPolicyName() })
    createdPolicyIdsB.push(probeB.id)

    // The positive control and the negative in one read: B's list is not empty, and A's row
    // is not in it. Containment, never a length — neither tenant's list is this test's to size.
    const idsB = (await listApprovalPolicies(tokenB)).approval_policies.map((p) => p.id)
    expect(idsB, "B's own list carries the policy B just created").toContain(probeB.id)
    expect(idsB, "and never carries A's").not.toContain(probeA.id)

    const readB = await rawFetch(`/api/invoice/v1/approval-policies/${probeA.id}`, {
      headers: { Authorization: `Bearer ${tokenB}` },
    })
    assertWireError(readB, 404, "B reading A's policy by id")
    expect((readB.body as Record<string, unknown>).error, 'the row-invisibility 404, not a route-level one').toBe(
      'approval policy not found',
    )

    const deleteB = await rawFetch(`/api/invoice/v1/approval-policies/${probeA.id}`, {
      method: 'DELETE',
      headers: { Authorization: `Bearer ${tokenB}` },
    })
    assertWireError(deleteB, 404, "B deleting A's policy")
    expect((deleteB.body as Record<string, unknown>).error, 'the row-invisibility 404, not a route-level one').toBe(
      'approval policy not found',
    )

    // Zero side effect: a 404 response could in principle mask a delete that went through
    // anyway. DELETE here is soft (deleted_at IS NULL is the existence predicate), so A's own
    // read is what proves the row is still live rather than merely still present.
    const afterAttack = await getApprovalPolicy(token, probeA.id)
    expect(afterAttack.id, "A's row survives B's refused delete").toBe(probeA.id)
    expect(afterAttack.name, 'and is unchanged').toBe(probeA.name)
  })
})
