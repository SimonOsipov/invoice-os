// M3-14-04 (Core AC 4): the portfolio CRUD lifecycle, over the wire — through the SAME
// typed seam (api/client.ts) every api/ spec shares. Drives the FULL create -> read ->
// list -> update -> offboard -> onboard lifecycle on ONE freshly-created entity, as
// tenant A, asserting only the HTTP-observable results at each stage (Core AC 4).
//
// Audit note (Decision A2): portfolio.entity.created/updated/offboarded/onboarded audit
// rows are written in-tx by the backend and are asserted IN-PROCESS by M3-10
// (internal/portfolio/cross_tenant_integration_test.go). There is no audit read endpoint
// on the gateway or any service (verified — see A2), so those side-effects are NOT
// observable over the wire. This spec therefore asserts the HTTP-observable lifecycle
// results only; the audit-in-tx guarantee remains M3-10's in-process proof.
import { test, expect } from '@playwright/test'
import {
  login,
  createEntity,
  getEntity,
  listEntities,
  updateEntity,
  offboardEntity,
  onboardEntity,
  PERSONAS,
} from './client'
import { freshTin } from './fixtures'

// Serial: every stage after Create depends on the entity id captured there, and each
// stage mutates the SAME row in sequence (playwright.api.config.ts already runs the
// whole suite workers:1/fullyParallel:false — this is belt-and-braces for this file's
// internal ordering, matching validation.spec.ts's convention).
test.describe.configure({ mode: 'serial' })

test.describe('portfolio CRUD lifecycle (API E2E, over the deployed gateway)', () => {
  let token: string
  let entityId: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test('AC1: create — POST /entities with a fresh unique Luhn-valid TIN returns a 201-shaped active Entity', async () => {
    // Audit note (A2): see the top-of-file comment — audit side-effects for every
    // lifecycle stage below (created/updated/offboarded/onboarded) are written in-tx
    // and are asserted IN-PROCESS by M3-10 (internal/portfolio/cross_tenant_integration_test.go).
    // There is no audit read endpoint (no wire-observable audit trail), so this whole
    // spec asserts HTTP-observable lifecycle results only.
    test.info().annotations.push({
      type: 'audit-note',
      description:
        'Audit side-effects (portfolio.entity.created/updated/offboarded/onboarded) are written in-tx and asserted IN-PROCESS by M3-10 (internal/portfolio/cross_tenant_integration_test.go); they are NOT observable over the wire (no audit read endpoint — decision A2), so this spec asserts the HTTP-observable lifecycle results only.',
    })

    const tin = freshTin()
    const name = `M3-14-04 CRUD ${tin}`
    const created = await createEntity(token, { name, tin, sector: 'Retail' })

    expect(created.id.length).toBeGreaterThan(0)
    expect(created.name).toBe(name)
    expect(created.tin).toBe(tin)
    expect(created.status).toBe('active')

    entityId = created.id
  })

  test('AC2: read — GET /entities/{id} returns the created row; GET /entities lists it with the pagination envelope', async () => {
    expect(entityId, 'entityId must have been captured by the preceding create test').toBeTruthy()

    const read = await getEntity(token, entityId)
    expect(read.id).toBe(entityId)
    expect(read.status).toBe('active')

    const list = await listEntities(token, { limit: 200 })
    const found = list.entities.find((e) => e.id === entityId)
    expect(found, 'created entity should be present in the list').toBeTruthy()
    expect(found?.name).toBe(read.name)
    expect(found?.status).toBe('active')

    // Pagination envelope: numeric fields present, and at least this one entity
    // contributes to the total.
    expect(typeof list.pagination.limit).toBe('number')
    expect(typeof list.pagination.offset).toBe('number')
    expect(typeof list.pagination.total).toBe('number')
    expect(list.pagination.total).toBeGreaterThanOrEqual(1)
  })

  test('AC3: update — PATCH /entities/{id} (name + sector) is reflected on the next read', async () => {
    expect(entityId, 'entityId must have been captured by the preceding create test').toBeTruthy()

    const updatedName = `M3-14-04 CRUD updated ${freshTin()}`
    await updateEntity(token, entityId, { name: updatedName, sector: 'Manufacturing' })

    const read = await getEntity(token, entityId)
    expect(read.name).toBe(updatedName)
    expect(read.sector).toBe('Manufacturing')
  })

  test('AC3: offboard — POST /entities/{id}/offboard archives the entity; it moves from the active list to the archived list', async () => {
    expect(entityId, 'entityId must have been captured by the preceding create test').toBeTruthy()

    const offboarded = await offboardEntity(token, entityId)
    expect(offboarded.status).toBe('archived')

    const read = await getEntity(token, entityId)
    expect(read.status).toBe('archived')

    const activeList = await listEntities(token, { status: 'active', limit: 200 })
    expect(activeList.entities.some((e) => e.id === entityId)).toBe(false)

    const archivedList = await listEntities(token, { status: 'archived', limit: 200 })
    expect(archivedList.entities.some((e) => e.id === entityId)).toBe(true)
  })

  test('AC3: onboard — POST /entities/{id}/onboard reactivates the entity back to active', async () => {
    expect(entityId, 'entityId must have been captured by the preceding create test').toBeTruthy()

    const onboarded = await onboardEntity(token, entityId)
    expect(onboarded.status).toBe('active')

    const read = await getEntity(token, entityId)
    expect(read.status).toBe('active')

    const activeList = await listEntities(token, { status: 'active', limit: 200 })
    expect(activeList.entities.some((e) => e.id === entityId)).toBe(true)
  })
})
