// RED specs (M3-08-01, task-56, P1-P11) — pin the portfolio entity data-access helpers,
// the entityStatusStyle pill mapper, and the shouldFetchEntities/clientsViewState
// render-decision helpers before the executor implements the bodies in portfolio.ts.
//
// P1-P6 mirror authedFetch.test.ts's / client.test.ts's `vi.stubGlobal('fetch', ...)`
// pattern: `fetch` is stubbed, but `createAuthedFetch`/`apiFetch` are the REAL
// @invoice-os/api-client + src/lib/authedFetch.ts exports, so a stubbed 200/400/409
// produces a genuine ApiError{kind:'http', ...} — proof at the integration level, not a
// re-implementation of apiFetch's own contract (already covered by C1-C8 in
// packages/api-client/src/client.test.ts).
//
// Every spec below currently fails because listEntities/createEntity/updateEntity/
// entityStatusStyle/shouldFetchEntities/clientsViewState's stub bodies throw `new
// Error('not implemented')` before ever calling the real authedFetch/fetch (or, for the
// pure helpers, before returning anything) — that IS the correct RED reason (assertion /
// not-implemented), not an import/compile/setup error.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncState, type AsyncStatus } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import {
  clientsViewState,
  createEntity,
  entityStatusStyle,
  listEntities,
  shouldFetchEntities,
  updateEntity,
  type Entity,
  type EntityInput,
} from './portfolio'

interface MockResponse {
  ok: boolean
  status: number
  statusText?: string
  json: () => Promise<unknown>
}

function mockFetchOnce(response: MockResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function mockFetchRejecting(err: unknown) {
  const fetchMock = vi.fn().mockRejectedValue(err)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Calls a (currently throwing) helper and returns the caught error, tolerating both a
// synchronous throw (today's stub) and an eventual async rejection — mirrors
// client.test.ts's / authedFetch.test.ts's captureRejection helper.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const base = 'https://gw'

const activeEntity: Entity = {
  id: 'e1',
  name: 'Okafor & Partners',
  tin: '00000000001',
  registration: 'RC123456',
  sector: 'logistics',
  address: '12 Marina Rd, Lagos',
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
}

const archivedEntity: Entity = {
  ...activeEntity,
  id: 'e2',
  name: 'Honeywell Group',
  status: 'archived',
}

describe('listEntities', () => {
  it('P1: GETs .../v1/entities?limit=200 with Authorization: Bearer <token>, resolves the unwrapped Entity[]', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve({
          entities: [activeEntity, archivedEntity],
          pagination: { limit: 200, offset: 0, total: 2 },
        }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listEntities(af, base)

    expect(result).toEqual([activeEntity, archivedEntity])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/portfolio/v1/entities?limit=200')
    expect(init.method).toBe('GET')
    const headers = new Headers(init.headers)
    expect(headers.get('Authorization')).toBe('Bearer tok')
  })

  it('P2: resolves [] when the tenant has no entities (drives the useAsync empty branch)', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ entities: [], pagination: { limit: 200, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listEntities(af, base)

    expect(result).toEqual([])
  })
})

describe('createEntity / updateEntity', () => {
  it('P3: createEntity POSTs a full-input JSON body and resolves the created Entity', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 201, json: () => Promise.resolve(activeEntity) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await createEntity(af, base, { name: 'Acme', tin: '0000000000' })

    expect(result).toEqual(activeEntity)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/portfolio/v1/entities')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ name: 'Acme', tin: '0000000000' }))
    const headers = new Headers(init.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
  })

  it('P4: updateEntity PATCHes a name-only PARTIAL JSON body (tin untouched) and resolves the updated Entity', async () => {
    const updated: Entity = { ...activeEntity, name: 'New' }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await updateEntity(af, base, 'e1', { name: 'New' })

    expect(result).toEqual(updated)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/portfolio/v1/entities/e1')
    expect(init.method).toBe('PATCH')
    expect(init.body).toBe(JSON.stringify({ name: 'New' }))
  })
})

describe('createEntity: non-2xx rejects with the ApiError unchanged (not swallowed)', () => {
  it('P5: a 400 rejects ApiError{kind:"http", status:400} carrying the body message', async () => {
    mockFetchOnce({ ok: false, status: 400, json: () => Promise.resolve({ error: 'tin invalid' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => createEntity(af, base, { name: 'Acme', tin: 'bad' }))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(400)
    expect(apiErr.message).toContain('tin invalid')
  })

  it('P6: a 409 rejects ApiError{status:409} (duplicate tin)', async () => {
    mockFetchOnce({ ok: false, status: 409, json: () => Promise.resolve({ error: 'duplicate tin' }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => createEntity(af, base, { name: 'Acme', tin: '0000000000' }))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
  })
})

describe('entityStatusStyle', () => {
  it('P7: "active" maps to the green pill, label ACTIVE', () => {
    expect(entityStatusStyle('active')).toEqual({
      bg: 'var(--status-green-bg)',
      border: 'var(--status-green-border)',
      text: 'var(--status-green-text)',
      label: 'ACTIVE',
    })
  })

  it('P8: "archived" maps to the muted pill, label ARCHIVED', () => {
    expect(entityStatusStyle('archived')).toEqual({
      bg: 'var(--status-muted-bg)',
      border: 'var(--status-muted-border)',
      text: 'var(--status-muted-text)',
      label: 'ARCHIVED',
    })
  })
})

describe('shouldFetchEntities', () => {
  it('P9: false iff base == null', () => {
    expect(shouldFetchEntities(null)).toBe(false)
    expect(shouldFetchEntities('https://gw')).toBe(true)
  })
})

describe('clientsViewState', () => {
  it('P10: base==null is "idle" regardless of async status (no-gateway zero-network short-circuit wins)', () => {
    const readyState: AsyncState<Entity[]> = { status: 'ready', data: [activeEntity], error: null }

    expect(clientsViewState(null, readyState)).toBe('idle')
  })

  it('P11: base present mirrors async.status exactly, for loading/error/empty/ready', () => {
    const cases: Array<AsyncState<Entity[]>> = [
      { status: 'loading', data: null, error: null },
      { status: 'error', data: null, error: new ApiError('network', 'boom') },
      { status: 'empty', data: null, error: null },
      { status: 'ready', data: [activeEntity], error: null },
    ]

    for (const asyncState of cases) {
      expect(clientsViewState(base, asyncState)).toBe(asyncState.status)
    }
  })
})

// --- Adversarial / edge / negative coverage added in QA (M3-08-01 verify pass) ---
// Appends only — the P1-P11 specs above are untouched.

describe('listEntities: malformed envelope (200 OK, but the body does not match EntityListResponse)', () => {
  // apiFetch casts `res.json()` as T with no runtime validation (client.ts L75-76,
  // "return (await res.json()) as T") — listEntities inherits that same trust-the-
  // gateway-contract posture (it does no validation of its own either). These specs
  // pin the ACTUAL runtime behavior so a future tightening (e.g. adding validation) is
  // a deliberate, visible change, not a silent one.
  it('P12: `entities` key absent → returns undefined (not [], not a throw)', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ pagination: { limit: 200, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listEntities(af, base)

    expect(result).toBeUndefined()
  })

  it('P13: `entities` present but not an array → passed through unchanged, uncoerced', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ entities: 'not-an-array', pagination: { limit: 200, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listEntities(af, base)

    expect(result).toBe('not-an-array')
  })
})

describe('listEntities: transport/auth failures propagate unchanged (not swallowed or reshaped)', () => {
  it('P14: a network failure (fetch itself rejects) propagates as ApiError{kind:"network", status:null}', async () => {
    mockFetchRejecting(new TypeError('Failed to fetch'))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const caught = await captureRejection(() => listEntities(af, base))

    expect(caught).toBeInstanceOf(ApiError)
    const apiErr = caught as ApiError
    expect(apiErr.kind).toBe('network')
    expect(apiErr.status).toBeNull()
  })

  it('P15: a 401 propagates as ApiError{status:401} through listEntities (the pure helper does not catch it) while the authedFetch seam still fires onUnauthorized', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'token expired' }) })
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    const caught = await captureRejection(() => listEntities(af, base))

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).status).toBe(401)
    // Not this helper's job to call onUnauthorized (that's the seam's, M3-07-02) —
    // asserted here only to prove listEntities didn't intercept/swallow the error
    // before it reached the seam.
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })
})

describe('createEntity / updateEntity: exact body serialization at the transport layer', () => {
  it('P16: createEntity with every optional field set serializes ALL of them, none dropped', async () => {
    const fullInput: EntityInput = {
      name: 'Acme',
      tin: '0000000000',
      registration: 'RC999999',
      sector: 'retail',
      address: '1 Broad St, Lagos',
    }
    const fetchMock = mockFetchOnce({ ok: true, status: 201, json: () => Promise.resolve(activeEntity) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await createEntity(af, base, fullInput)

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.body).toBe(JSON.stringify(fullInput))
  })

  it('P17: updateEntity sends an explicit empty string for a cleared optional field — "" is serialized, not dropped (F6 clear-an-optional path)', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(activeEntity) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await updateEntity(af, base, 'e1', { address: '' })

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.body).toBe(JSON.stringify({ address: '' }))
    expect(init.body).not.toBe('{}')
    const parsed = JSON.parse(init.body as string) as Record<string, unknown>
    expect(parsed).toHaveProperty('address', '')
  })
})

describe('entityStatusStyle: exhaustiveness', () => {
  it('P18: active/archived both return well-formed (all 4 fields non-empty) StatusStyle objects, and they are distinct', () => {
    const active = entityStatusStyle('active')
    const archived = entityStatusStyle('archived')

    for (const style of [active, archived]) {
      expect(style.bg).toBeTruthy()
      expect(style.border).toBeTruthy()
      expect(style.text).toBeTruthy()
      expect(style.label).toBeTruthy()
    }
    expect(active).not.toEqual(archived)
  })
})

describe('clientsViewState: every branch', () => {
  it('P19: base==null short-circuits to idle for EVERY async status, not just "ready" (belt-and-suspenders on the zero-network decision)', () => {
    const cases: Array<AsyncState<Entity[]>> = [
      { status: 'idle', data: null, error: null },
      { status: 'loading', data: null, error: null },
      { status: 'error', data: null, error: new ApiError('network', 'boom') },
      { status: 'empty', data: null, error: null },
      { status: 'ready', data: [activeEntity], error: null },
    ]

    for (const asyncState of cases) {
      expect(clientsViewState(null, asyncState)).toBe('idle')
    }
  })

  it('P20: base present + status "idle" mirrors idle too (P11 only covered loading/error/empty/ready)', () => {
    const asyncState: AsyncState<Entity[]> = { status: 'idle', data: null, error: null }

    const result: AsyncStatus = clientsViewState(base, asyncState)

    expect(result).toBe('idle')
  })
})

describe('shouldFetchEntities: strict null-check, not truthiness', () => {
  it('P21: an empty-string base is non-null and falsy — must still return true (base != null, not base ? ... )', () => {
    expect(shouldFetchEntities('')).toBe(true)
  })
})
