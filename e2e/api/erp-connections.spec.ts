// ERP connections over the deployed gateway: the preview runs the fake connector by posture.
// Every step acts on a client this run created; assertions are containment, never a total count.
import { test, expect } from '@playwright/test'
import { login, createEntity, rawFetch, apiBase, getAuditLog, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

const FAKE = 'fake' // fakeSystem in internal/erp/fake.go
const CO1 = 'fake-co-1' // erpweb.FakeCompanies
const CO2_EXPIRED = 'fake-co-2~expired' // erpweb.FakeExpired
const CO3 = 'fake-co-3'
const CO3_REFRESH_FAILS = 'fake-co-3~refresh_fails' // erpweb.FakeRefreshFails
const PENDING = 'pending' // erp_connections.status literals in internal/erp/store.go, refresh.go
const ACTIVE = 'active'
const RECONSENT = 'reconsent'
const DISCONNECTED = 'disconnected'
const CONNECTED = 'connected' // erp.Outcome* in internal/erp/complete.go
const INVALID = 'invalid'
const FAILED = 'failed'
const EV_REFRESHED = 'erp.token.refreshed' // audit events: internal/erp/refresh.go, release.go
const EV_REFRESH_FAILED = 'erp.token.refresh_failed'
const EV_REAUTH = 'erp.connection.reauth_required'
const EV_DELETED = 'erp.token.deleted'
const PREPARER = 'c0000000-0000-0000-0000-000000000003' // seeded firm preparer, all_clients
const NOT_ADMIN = 'only an admin can manage ERP connections' // statusForErr, ErrNotPermitted
const POLL_MS = 120_000
const FAKE_OPTIONS = 9 // erpweb.FakeCodes: three companies, each plain and with two suffixes

interface Conn {
  id: string
  entity_id: string
  system: string
  external_key: string | null
  status: string
  consent_url?: string
}
interface Listing {
  connections: Conn[]
  systems: string[]
}

async function consent(url: string): Promise<{ status: number; options: string[] }> {
  const res = await fetch(url)
  const html = await res.text()
  const options = [...html.matchAll(/<option value="([^"]+)"/g)].map((m) => m[1])
  return { status: res.status, options }
}

// The gateway's /erp/callback page: HTML, so rawFetch (JSON) does not fit.
async function callback(state: string, code: string): Promise<{ status: number; outcome: string | undefined }> {
  const q = new URLSearchParams({ state, code })
  const res = await fetch(`${apiBase()}/erp/callback?${q}`, { redirect: 'manual' })
  const html = await res.text()
  return { status: res.status, outcome: /data-outcome="([a-z_]+)"/.exec(html)?.[1] }
}

const stateOf = (url: string) => new URL(url).searchParams.get('state') ?? ''

// Polls until fn returns a value or POLL_MS passes; the job queue has no faster oracle.
async function until<T>(what: string, fn: () => Promise<T | undefined>): Promise<T> {
  const deadline = Date.now() + POLL_MS
  for (;;) {
    const v = await fn()
    if (v !== undefined) return v
    if (Date.now() > deadline) throw new Error(`timed out after ${POLL_MS / 1000}s waiting for ${what}`)
    await new Promise((r) => setTimeout(r, 3000))
  }
}

test.describe.serial('ERP connections (API E2E, over the deployed gateway)', () => {
  let admin: string
  let preparer: string
  let entityId: string
  let connId: string
  let base: string
  let lastState: string
  const auth = (t: string) => ({ Authorization: `Bearer ${t}` })
  const baseOf = (id: string) => `/api/portfolio/v1/entities/${id}/erp-connections`
  const list = (t: string, id = entityId) => rawFetch(baseOf(id), { headers: auth(t) })
  const listOf = async (id: string) => ((await list(admin, id)).body as Listing).connections
  const connect = (t: string, body: unknown, id = entityId) => rawFetch(baseOf(id), { method: 'POST', headers: auth(t), body })
  const newClient = async (label: string) => {
    const tin = freshTin()
    return (await createEntity(admin, { name: `ERPF-02 ${label} ${tin}`, tin })).id
  }
  const auditRows = async (event: string) =>
    (await getAuditLog(admin, { event: [event], limit: 100 })).events.map((e) => ({
      id: e.id,
      payload: (e.payload ?? {}) as Record<string, unknown>,
    }))

  test.beforeAll(async () => {
    admin = await login(PERSONAS.A)
    preparer = await login({ ...PERSONAS.A, subject: PREPARER })
    entityId = await newClient('consent')
    base = baseOf(entityId)
  })

  test('preview runs the fake: list shows the fake system and no connections', async () => {
    const res = await list(admin)
    expect(res.status).toBe(200)
    const body = res.body as Listing
    expect(body.systems).toContain(FAKE)
    expect(body.connections).toEqual([])
  })

  test('connect answers a pending connection and a fake consent page', async () => {
    const created = await connect(admin, { system: FAKE })
    expect(created.status).toBe(201)
    const conn = created.body as Conn
    expect(conn.status).toBe(PENDING)
    expect(conn.entity_id).toBe(entityId)
    connId = conn.id
    const url = conn.consent_url ?? ''
    expect(new URL(url).origin).toBe(new URL(apiBase()).origin)
    lastState = stateOf(url)
    const page = await consent(url)
    expect(page.status).toBe(200)
    expect(page.options).toHaveLength(FAKE_OPTIONS)
    expect(page.options).toContain(CO1)
  })

  test('the callback completes the grant', async () => {
    const done = await callback(lastState, CO1)
    expect(done).toEqual({ status: 200, outcome: CONNECTED })
    const conn = (await listOf(entityId)).find((c) => c.id === connId)
    expect(conn?.status).toBe(ACTIVE)
    expect(conn?.external_key).toBe(CO1)

    const again = await connect(admin, { system: FAKE, connection_id: connId })
    assertErrorEnvelope(again, 409, 'connect an active connection')
    expect((again.body as { error: string }).error).toBe('already connected')
  })

  test('a state is single-use and a bad code keeps the connection pending', async () => {
    expect(await callback(lastState, CO1)).toEqual({ status: 400, outcome: INVALID })

    const made = crypto.randomUUID()
    const random = `${made}.${Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString('base64url')}`
    expect(await callback(random, CO1)).toEqual({ status: 400, outcome: INVALID })

    const second = await connect(admin, { system: FAKE })
    expect(second.status).toBe(201)
    const pending = second.body as Conn
    expect(pending.id).not.toBe(connId)
    expect(await callback(stateOf(pending.consent_url ?? ''), 'nope')).toEqual({ status: 502, outcome: FAILED })
    const conn = (await listOf(entityId)).find((c) => c.id === pending.id)
    expect(conn?.status).toBe(PENDING)
  })

  test('an expired token is refreshed by the job', async () => {
    test.setTimeout(POLL_MS + 60_000)
    const id = await newClient('refresh')
    const seen = new Set((await auditRows(EV_REFRESHED)).map((r) => r.id))
    const created = (await connect(admin, { system: FAKE }, id)).body as Conn
    expect(await callback(stateOf(created.consent_url ?? ''), CO2_EXPIRED)).toEqual({ status: 200, outcome: CONNECTED })
    await until(`a new ${EV_REFRESHED} row`, async () => (await auditRows(EV_REFRESHED)).find((r) => !seen.has(r.id)))
    expect((await listOf(id)).find((c) => c.id === created.id)?.status).toBe(ACTIVE)
  })

  test('a refresh failure asks for consent again', async () => {
    test.setTimeout(POLL_MS + 60_000)
    const id = await newClient('reconsent')
    const seenFailed = new Set((await auditRows(EV_REFRESH_FAILED)).map((r) => r.id))
    const created = (await connect(admin, { system: FAKE }, id)).body as Conn
    expect(await callback(stateOf(created.consent_url ?? ''), CO3_REFRESH_FAILS)).toEqual({ status: 200, outcome: CONNECTED })

    await until(`connection ${created.id} to read ${RECONSENT}`, async () =>
      (await listOf(id)).find((c) => c.id === created.id && c.status === RECONSENT),
    )
    await until(`${EV_REAUTH} for ${created.id}`, async () =>
      (await auditRows(EV_REAUTH)).find((r) => r.payload.connection_id === created.id),
    )
    await until(`a new ${EV_REFRESH_FAILED} row`, async () => (await auditRows(EV_REFRESH_FAILED)).find((r) => !seenFailed.has(r.id)))

    const again = await connect(admin, { system: FAKE, connection_id: created.id }, id)
    expect(again.status).toBe(200)
    const re = again.body as Conn
    expect(re.id).toBe(created.id)
    expect(re.status).toBe(PENDING)
    expect(await callback(stateOf(re.consent_url ?? ''), CO3)).toEqual({ status: 200, outcome: CONNECTED })
    const conn = (await listOf(id)).find((c) => c.id === created.id)
    expect(conn?.status).toBe(ACTIVE)
  })

  test('a preparer lists but cannot change ERP connections', async () => {
    const listed = await list(preparer)
    expect(listed.status).toBe(200)
    expect((listed.body as Listing).connections.map((c) => c.id)).toContain(connId)

    const refused = [
      await connect(preparer, { system: FAKE }),
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

    const conn = (await listOf(entityId)).find((c) => c.id === connId)
    expect(conn?.status).toBe(ACTIVE)
  })

  test('disconnect deletes the token', async () => {
    test.setTimeout(POLL_MS + 60_000)
    const off = await rawFetch(`${base}/${connId}/disconnect`, { method: 'POST', headers: auth(admin) })
    expect(off.status).toBe(200)
    expect((off.body as Conn).status).toBe(DISCONNECTED)
    expect((await listOf(entityId)).find((c) => c.id === connId)?.status).toBe(DISCONNECTED)
    await until(`${EV_DELETED} for ${connId}`, async () =>
      (await auditRows(EV_DELETED)).find((r) => r.payload.connection_id === connId),
    )
  })

  test('another tenant\'s admin cannot see the client', async () => {
    const tokenB = await login(PERSONAS.B)
    assertErrorEnvelope(await list(tokenB), 404, 'tenant B list')
  })

  test('an unknown system is refused', async () => {
    const res = await connect(admin, { system: 'sage' })
    assertErrorEnvelope(res, 400, 'unknown system')
    expect((res.body as { error: string }).error).toBe('unknown system')
  })
})
