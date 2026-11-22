// ERP connections over the deployed gateway: the preview runs the fake connector by posture.
// Every step acts on a client this run created; assertions are containment, never a total count.
import { test, expect } from '@playwright/test'
import { login, createEntity, rawFetch, apiBase, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

const FAKE = 'fake' // fakeSystem in internal/erp/fake.go
const FAKE_KEY = 'fake-co-1' // fakeSeed key in internal/erp/fake_seed.go
const ACTIVE = 'active' // store.go status literals
const DISCONNECTED = 'disconnected'
const PREPARER = 'c0000000-0000-0000-0000-000000000003' // seeded firm preparer, all_clients
const NOT_ADMIN = 'only an admin can manage ERP connections' // statusForErr, ErrNotPermitted

interface Conn {
  id: string
  entity_id: string
  system: string
  external_key: string | null
  status: string
}
interface Listing {
  connections: Conn[]
  systems: string[]
}

test.describe.serial('ERP connections (API E2E, over the deployed gateway)', () => {
  let admin: string
  let preparer: string
  let entityId: string
  let connId: string
  let base: string
  const auth = (t: string) => ({ Authorization: `Bearer ${t}` })
  const list = (t: string, id = entityId) => rawFetch(base.replace(entityId, id), { headers: auth(t) })
  const connect = (t: string, body: unknown) => rawFetch(base, { method: 'POST', headers: auth(t), body })

  test.beforeAll(async () => {
    admin = await login(PERSONAS.A)
    preparer = await login({ ...PERSONAS.A, subject: PREPARER })
    const tin = freshTin()
    entityId = (await createEntity(admin, { name: `ERPF-01 ${tin}`, tin })).id
    base = `/api/portfolio/v1/entities/${entityId}/erp-connections`
  })

  test('preview runs the fake: list shows the fake system and no connections', async () => {
    const res = await list(admin)
    expect(res.status).toBe(200)
    const body = res.body as Listing
    expect(body.systems).toContain(FAKE)
    expect(body.connections).toEqual([])
  })

  test('admin connects, lists, disconnects and reconnects the fake ERP', async () => {
    const created = await connect(admin, { system: FAKE, external_key: FAKE_KEY })
    expect(created.status).toBe(201)
    const conn = created.body as Conn
    expect(conn.status).toBe(ACTIVE)
    expect(conn.entity_id).toBe(entityId)
    connId = conn.id

    let listed = (await list(admin)).body as Listing
    expect(listed.connections.find((c) => c.id === connId)?.status).toBe(ACTIVE)

    const again = await connect(admin, { system: FAKE, external_key: FAKE_KEY })
    assertErrorEnvelope(again, 409, 'connect twice')
    expect((again.body as { error: string }).error).toBe('already connected')

    const off = await rawFetch(`${base}/${connId}/disconnect`, { method: 'POST', headers: auth(admin) })
    expect(off.status).toBe(200)
    expect((off.body as Conn).status).toBe(DISCONNECTED)

    listed = (await list(admin)).body as Listing
    expect(listed.connections.find((c) => c.id === connId)?.status).toBe(DISCONNECTED)

    const back = await connect(admin, { system: FAKE, external_key: FAKE_KEY })
    expect(back.status).toBe(200)
    expect((back.body as Conn).id).toBe(connId)
    expect((back.body as Conn).status).toBe(ACTIVE)
  })

  test('a preparer lists but cannot change ERP connections', async () => {
    const listed = await list(preparer)
    expect(listed.status).toBe(200)
    expect((listed.body as Listing).connections.map((c) => c.id)).toContain(connId)

    const refused = [
      await connect(preparer, { system: FAKE, external_key: FAKE_KEY }),
      // Not JSON at all: the admin gate answers before the body is read.
      await fetch(`${apiBase()}${base}`, { method: 'POST', headers: { ...auth(preparer), 'Content-Type': 'application/json' }, body: 'not json' }).then(
        async (r) => ({ status: r.status, body: await r.json().catch(() => undefined) as unknown }),
      ),
      await rawFetch(`${base}/${connId}/disconnect`, { method: 'POST', headers: auth(preparer) }),
    ]
    for (const [i, res] of refused.entries()) {
      assertErrorEnvelope(res, 403, `preparer change #${i}`)
      expect((res.body as { error: string }).error).toBe(NOT_ADMIN)
    }

    const after = (await list(admin)).body as Listing
    const conn = after.connections.find((c) => c.id === connId)
    expect(conn?.status).toBe(ACTIVE)
  })

  test('another tenant\'s admin cannot see the client', async () => {
    const tokenB = await login(PERSONAS.B)
    assertErrorEnvelope(await list(tokenB), 404, 'tenant B list')
  })

  test('an unknown system is refused', async () => {
    const res = await connect(admin, { system: 'sage', external_key: 'x' })
    assertErrorEnvelope(res, 400, 'unknown system')
    expect((res.body as { error: string }).error).toBe('unknown system')
  })
})
