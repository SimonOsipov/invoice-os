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
import * as portfolioModule from './portfolio'
import {
  clientsViewState,
  createEntity,
  entityStatusStyle,
  listEntities,
  shouldFetchEntities,
  updateEntity,
  visibleEntityIds,
  type AuthedFetch,
  type Entity,
  type EntityInput,
  type EntityListResponse,
  type EntityStatus,
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
  it('P1: GETs .../v1/entities?limit=200 with Authorization: Bearer <token>, resolves the {entities,pagination} envelope', async () => {
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

    expect(result.entities).toEqual([activeEntity, archivedEntity])
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

    expect(result.entities).toEqual([])
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
  it('P12: `entities` key absent → `.entities` is undefined (not [], not a throw)', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ pagination: { limit: 200, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listEntities(af, base)

    expect(result.entities).toBeUndefined()
  })

  it('P13: `entities` present but not an array → passed through unchanged, uncoerced', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ entities: 'not-an-array', pagination: { limit: 200, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listEntities(af, base)

    expect(result.entities).toBe('not-an-array')
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

// --- visibleEntityIds (persona-handoff-fix step 5, Task A) --------------------------
// Sidebar.tsx's workspace switcher offers only these ids: active entities, plus the
// currently-selected one even when archived (the workspace open right now must never
// vanish from its own switcher — see the function's own doc comment for the full
// rationale). `clients` itself (App.tsx's `active` resolution, switchClient()) is NEVER
// filtered by this — only what Sidebar's dropdown renders.
describe('visibleEntityIds', () => {
  const active1: Entity = { ...activeEntity, id: 'e1', status: 'active' }
  const active2: Entity = { ...activeEntity, id: 'e2', status: 'active' }
  const archived1: Entity = { ...activeEntity, id: 'e3', status: 'archived' }

  it('V1: active entities are included, archived ones are not, when selectedId is null', () => {
    const result = visibleEntityIds([active1, active2, archived1], null)

    expect(result).toEqual(new Set(['e1', 'e2']))
  })

  it('V2: an archived entity IS included when it is the selectedId — the open workspace never disappears from its own switcher', () => {
    const result = visibleEntityIds([active1, active2, archived1], 'e3')

    expect(result).toEqual(new Set(['e1', 'e2', 'e3']))
  })

  it('V3: selecting an already-active entity is a no-op on the set (no duplicate/second entry)', () => {
    const result = visibleEntityIds([active1, active2, archived1], 'e1')

    expect(result).toEqual(new Set(['e1', 'e2']))
  })

  it('V4: zero active entities + a null selectedId returns an empty set (the caller falls back to clients[0] elsewhere, not here)', () => {
    const result = visibleEntityIds([archived1], null)

    expect(result).toEqual(new Set())
  })

  it('V5: a selectedId that matches no entity in the list (e.g. the in-house/empty synthetic fallback, entityId:null never reaches this — but a stale id could) is still added, since this helper trusts its caller rather than re-validating', () => {
    const result = visibleEntityIds([active1], 'does-not-exist')

    expect(result).toEqual(new Set(['e1', 'does-not-exist']))
  })

  it('V6: empty entities list + null selectedId returns an empty set', () => {
    expect(visibleEntityIds([], null)).toEqual(new Set())
  })
})

// --- Status filter (Clients portfolio) ---------------------------------------------
// Read off the module namespace (mirrors invoices.test.ts's own idiom) rather than a
// typed import, so a missing export or a signature drift fails as an assertion, never an
// import/compile error -- the top-level `listEntities` import above is left untouched for
// the pre-existing P1-P21/V1-V6 specs.
const portfolioNS = portfolioModule as unknown as Record<string, unknown>

type EntityFilterPosShape = 'all' | 'active' | 'archived'
type EntityStatusParamFn = (pos: EntityFilterPosShape) => EntityStatus | undefined
type ListEntitiesFn = (
  authedFetch: AuthedFetch,
  base: string,
  opts?: { status?: EntityStatus; limit?: number },
) => Promise<EntityListResponse>
type EntityListIsEmptyFn = (r: EntityListResponse) => boolean
type PortfolioCountLabelFn = (shown: number, total: number) => string

function entityStatusParamUnderTest(pos: EntityFilterPosShape): EntityStatus | undefined {
  const fn = portfolioNS.entityStatusParam as EntityStatusParamFn | undefined
  expect(fn, 'entityStatusParam is not exported by portfolio.ts yet').toBeDefined()
  return fn!(pos)
}

function listEntitiesUnderTest(
  authedFetch: AuthedFetch,
  base: string,
  opts?: { status?: EntityStatus; limit?: number },
): Promise<EntityListResponse> {
  const fn = portfolioNS.listEntities as ListEntitiesFn
  return fn(authedFetch, base, opts)
}

function entityListIsEmptyUnderTest(r: EntityListResponse): boolean {
  const fn = portfolioNS.entityListIsEmpty as EntityListIsEmptyFn | undefined
  expect(fn, 'entityListIsEmpty is not exported by portfolio.ts yet').toBeDefined()
  return fn!(r)
}

function portfolioCountLabelUnderTest(shown: number, total: number): string {
  const fn = portfolioNS.portfolioCountLabel as PortfolioCountLabelFn | undefined
  expect(fn, 'portfolioCountLabel is not exported by portfolio.ts yet').toBeDefined()
  return fn!(shown, total)
}

describe('entityStatusParam maps each position', () => {
  it("'all' -> undefined, 'active' -> 'active', 'archived' -> 'archived'", () => {
    expect(entityStatusParamUnderTest('all')).toBeUndefined()
    expect(entityStatusParamUnderTest('active')).toBe('active')
    expect(entityStatusParamUnderTest('archived')).toBe('archived')
  })
})

describe('listEntities emits status only when narrowing', () => {
  it('opts.status forwards status=<value>; {} sends no status param at all', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const narrowedFetch = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ entities: [archivedEntity], pagination: { limit: 200, offset: 0, total: 6 } }),
    })
    await listEntitiesUnderTest(af, base, { status: 'archived' })
    const [narrowedUrl] = narrowedFetch.mock.calls[0] as [string, RequestInit]
    expect(new URL(narrowedUrl).searchParams.get('status'), '{status:"archived"} must put status=archived on the wire').toBe('archived')

    const allFetch = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ entities: [activeEntity, archivedEntity], pagination: { limit: 200, offset: 0, total: 27 } }),
    })
    await listEntitiesUnderTest(af, base, {})
    const [allUrl] = allFetch.mock.calls[0] as [string, RequestInit]
    expect(new URL(allUrl).searchParams.has('status'), '{} (the All position) must omit the key entirely, not send status=').toBe(false)
  })
})

describe('listEntities returns the pagination envelope', () => {
  it('the resolved value carries both `entities` and `pagination`, not just the unwrapped array', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ entities: [activeEntity, archivedEntity], pagination: { limit: 200, offset: 0, total: 27 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listEntitiesUnderTest(af, base)

    expect(result.entities, 'listEntities must resolve the whole envelope, not the already-unwrapped array').toBeDefined()
    expect(result.pagination, 'listEntities must resolve the whole envelope, not the already-unwrapped array').toBeDefined()
    expect(result.entities).toEqual([activeEntity, archivedEntity])
    expect(result.pagination).toEqual({ limit: 200, offset: 0, total: 27 })
  })
})

describe('entityListIsEmpty is true only when the portfolio is empty', () => {
  it('total===0 is true; a mid-set empty page (offset:200,total:27) is false', () => {
    const genuineEmpty: EntityListResponse = { entities: [], pagination: { limit: 200, offset: 0, total: 0 } }
    expect(entityListIsEmptyUnderTest(genuineEmpty)).toBe(true)

    const midSetEmptyPage: EntityListResponse = { entities: [], pagination: { limit: 200, offset: 200, total: 27 } }
    expect(entityListIsEmptyUnderTest(midSetEmptyPage)).toBe(false)
  })
})

describe('portfolioCountLabel agrees with the rows shown', () => {
  it('(21,21) -> "21 companies"; (200,247) -> "200 of 247 companies"; (0,0) -> "0 companies"', () => {
    expect(portfolioCountLabelUnderTest(21, 21)).toBe('21 companies')
    expect(portfolioCountLabelUnderTest(200, 247)).toBe('200 of 247 companies')
    expect(portfolioCountLabelUnderTest(0, 0)).toBe('0 companies')
  })
})
