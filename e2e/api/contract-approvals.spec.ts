// The workflow-role contract, over the wire — through the SAME typed seam
// (api/client.ts) every api/ spec shares. Error paths go through rawFetch, so the
// exact status and {error:…} envelope are observable; apiFetch resolves on ANY 2xx,
// so the create test uses rawFetch too, to see the 201 rather than merely a success.
//
// This is the only guard against a desc/description slip, a null `members`, or a lost
// staffing order reaching the SPA: roles.test.ts covers the pure functions, not the wire.
//
// WRITES to a SHARED, never-reset environment, so, following contract-tenancy.spec.ts:
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
  createWorkflowRole,
  deleteWorkflowRole,
  listWorkflowRoles,
  login,
  rawFetch,
  staffWorkflowRole,
  updateWorkflowRole,
  PERSONAS,
} from './client'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'
import { freshRoleTitle } from './fixtures'

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
