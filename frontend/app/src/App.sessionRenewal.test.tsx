// @vitest-environment jsdom
// Every request waits for a due renewal; a refused one returns to landing (AUTH-06 D5, D8, D9).

import { StrictMode } from 'react'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { DEEP_LINK_KEY, readDestination } from './lib/deepLink'
import type { Member } from './lib/members'
import { SESSION_KEY } from './lib/session'
import type { PlatformCtx } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const STATE_RE = '[A-Za-z0-9_-]{43}'
const REFRESH = `${GATEWAY}/auth/refresh`
const PROBE = `${GATEWAY}/api/probe`

const MIN = 60_000
const HOUR = 60 * MIN
const NOW = Date.UTC(2026, 8, 28, 12, 0, 0)
const nowSec = (ms: number) => Math.floor(ms / 1000)

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated' },
}
const OTHER_ME: Me = {
  tenant: { id: '44444444-4444-4444-4444-444444444444', name: 'Earlier Holdings' },
  user: { id: 'e0000000-0000-0000-0000-000000000004', role: 'authenticated' },
}

const b64url = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
function jwt(me: Me, iat: number, mark: string): string {
  const claims = { sub: me.user.id, iat, exp: iat + 3600, app_metadata: { tenant_id: me.tenant.id }, mark }
  return `${b64url('{"alg":"RS256"}')}.${b64url(JSON.stringify(claims))}.sig`
}

// Received 1 min ago: renews at +47 min, deadline at +59 min.
const FRESH_AT = NOW - MIN
const A0_FRESH = jwt(ME, nowSec(FRESH_AT), 'A0')
const MID_RENEW_AT = FRESH_AT + 0.8 * HOUR
const MID_DEADLINE = FRESH_AT + HOUR
// Received 50 min ago: due now, still valid for 10 min.
const DUE_AT = NOW - 50 * MIN
const A0_DUE = jwt(ME, nowSec(DUE_AT), 'A0')
// Received 2 h ago: the access token expired an hour ago.
const OLD_AT = NOW - 2 * HOUR
const A0_OLD = jwt(ME, nowSec(OLD_AT), 'A0')

// The renewed token is minted when the refresh answers.
const renewedToken = () => jwt(ME, nowSec(Date.now()), 'A1')

function record(token: string, receivedAt: number, me: Me = ME, refresh = 'R0'): string {
  return JSON.stringify({ v: 1, personaId: 'firm', token, me, verified: true, handoff: true, refresh_token: refresh, received_at: receivedAt })
}

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

// Node 22 (CI) has no web storage globals; Node 25's collide with jsdom's.
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  }
}

// Real jsdom location; href writes are recorded only.
function interceptHref() {
  const hrefWrites: string[] = []
  const real = window.location
  const proxy = new Proxy(real, {
    set(target, prop, value) {
      if (prop === 'href') {
        hrefWrites.push(value)
        return true
      }
      return Reflect.set(target, prop, value)
    },
    get(target, prop) {
      const v = (target as unknown as Record<PropertyKey, unknown>)[prop]
      return typeof v === 'function' ? v.bind(target) : v
    },
  })
  Object.defineProperty(window, 'location', { configurable: true, value: proxy })
  return { hrefWrites }
}

type Reply = () => Promise<unknown>
const answer = (status: number, body: unknown): Reply => () =>
  Promise.resolve({ ok: status >= 200 && status < 300, status, statusText: String(status), json: () => Promise.resolve(body) })
const renewed = (refresh = 'R1'): Reply => () => answer(200, { access_token: renewedToken(), refresh_token: refresh })()
const REFUSED = answer(401, { error: 'invalid or expired refresh token' })
const UNAVAILABLE = answer(502, { error: 'renewal is unavailable' })

interface Call {
  url: string
  auth: string | null
  body: unknown
  // A refresh was sent and not yet answered when this request went out.
  duringRefresh: boolean
}

let calls: Call[] = []
let refreshesOut = 0
let refreshReply: Reply = renewed()
let meReply: Reply = answer(200, ME)

// The refresh waits until the test releases it.
function deferRefresh(): { release: (r: Reply) => void } {
  let release!: (r: Reply) => void
  const gate = new Promise<Reply>((r) => {
    release = r
  })
  refreshReply = () => gate.then((r) => r())
  return { release }
}

function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { headers?: Headers; body?: string }) => {
      calls.push({
        url,
        auth: init?.headers?.get('Authorization') ?? null,
        body: init?.body === undefined ? undefined : JSON.parse(init.body),
        duringRefresh: refreshesOut > 0,
      })
      if (url === REFRESH) {
        refreshesOut++
        return refreshReply().finally(() => refreshesOut--)
      }
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return meReply()
      if (url === `${GATEWAY}/auth/login`) {
        return answer(200, { access_token: jwt(OTHER_ME, nowSec(Date.now()), 'P') })()
      }
      return answer(200, {
        entities: [],
        policies: [],
        members: [],
        roles: [],
        invoices: [],
        pagination: { limit: 50, offset: 0, total: 0 },
        total: 0,
        clients: [],
        totals: EMPTY_BUCKET,
        rejection_reasons: [],
      })()
    }),
  )
}

const refreshes = (from = 0) => calls.slice(from).filter((c) => c.url === REFRESH)
const apiCalls = (from = 0) => calls.slice(from).filter((c) => c.url.startsWith(`${GATEWAY}/api/`))
const probes = (from = 0) => calls.slice(from).filter((c) => c.url.startsWith(PROBE))
const errorName = (e: unknown) => (e instanceof Error ? e.name : String(e))

async function settle(ms = 30) {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms))
  })
}

async function bootApp(opts: { strict?: boolean } = {}) {
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(opts.strict ? (
      <StrictMode>
        <App />
      </StrictMode>
    ) : (
      <App />
    ))
  })
}

function storedRecord(): Record<string, unknown> | null {
  const raw = localStorage.getItem(SESSION_KEY)
  return raw == null ? null : (JSON.parse(raw) as Record<string, unknown>)
}

async function waitForVerifiedWorkspace(tenantName = ME.tenant.name) {
  await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
  expect(capturedCtx?.user).toEqual({
    name: APP_PERSONAS.firm.name,
    initials: APP_PERSONAS.firm.initials,
    tenantName,
    verified: true,
  })
}

// Stores a session, boots on `path` and returns the recorded navigations.
async function bootWith(raw: string | null, path = '/', opts: { strict?: boolean } = {}) {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubEnv('VITE_LANDING_URL', LANDING)
  if (raw !== null) localStorage.setItem(SESSION_KEY, raw)
  window.history.replaceState(null, '', path)
  const nav = interceptHref()
  await bootApp(opts)
  return nav
}

// Mounted on a session that is not due, all boot loads answered.
async function mountFresh(path = '/') {
  const nav = await bootWith(record(A0_FRESH, FRESH_AT), path)
  await waitForVerifiedWorkspace()
  await settle()
  expect(refreshes(), 'a fresh boot renews nothing').toEqual([])
  return nav
}

async function probe(ctx: PlatformCtx | undefined, path = ''): Promise<unknown> {
  let out: unknown
  await act(async () => {
    out = await ctx!.authedFetch(`${PROBE}${path}`).then(
      () => 'resolved',
      (e: unknown) => e,
    )
  })
  return out
}

let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  // Only Date: becomePersona's real BUSY_MS delay must still run.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
  capturedCtx = undefined
  calls = []
  refreshesOut = 0
  refreshReply = renewed()
  meReply = answer(200, ME)
  routeFetch()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  window.history.replaceState(null, '', '/')
})

describe('a due stored session renews at boot (AC-1, AC-2, AC-3, AC-10, AC-16)', () => {
  it('a due session renews before the workspace mounts', async () => {
    const refresh = deferRefresh()
    await bootWith(record(A0_DUE, DUE_AT))

    expect(screen.queryByText('Opening your workspace…'), 'a due boot shows the splash').not.toBeNull()
    expect(capturedCtx, 'the workspace waits for the renewal').toBeUndefined()
    expect(apiCalls()).toEqual([])

    await act(async () => refresh.release(renewed()))
    await waitForVerifiedWorkspace()
    await settle()

    expect(calls[0]?.url).toBe(REFRESH)
    expect(calls[0]?.body).toEqual({ refresh_token: 'R0' })
    expect(calls[0]?.auth, 'the refresh carries no bearer').toBeNull()
    expect(refreshes()).toHaveLength(1)
    expect(apiCalls().length).toBeGreaterThan(0)
    expect(apiCalls().filter((c) => c.auth !== `Bearer ${renewedToken()}`)).toEqual([])
    expect(storedRecord()?.token).toBe(renewedToken())
    expect(storedRecord()?.refresh_token).toBe('R1')
  })

  it('a fresh session mounts with no renewal', async () => {
    await bootWith(record(A0_FRESH, FRESH_AT))

    expect(screen.queryByText('Opening your workspace…')).toBeNull()
    await waitForVerifiedWorkspace()
    await settle()

    expect(refreshes()).toEqual([])
    expect(apiCalls().length).toBeGreaterThan(0)
    expect(apiCalls().filter((c) => c.auth !== `Bearer ${A0_FRESH}`)).toEqual([])
  })

  it('a session two hours old still opens the workspace', async () => {
    await bootWith(record(A0_OLD, OLD_AT))
    await waitForVerifiedWorkspace()
    await settle()

    expect(refreshes(), 'one renewal').toHaveLength(1)
    expect(calls[0]?.url).toBe(REFRESH)
    expect(apiCalls().length).toBeGreaterThan(0)
    expect(apiCalls().filter((c) => c.auth === `Bearer ${A0_OLD}`), 'the expired token is never sent').toEqual([])
  })

  it('StrictMode renews once at boot', async () => {
    await bootWith(record(A0_DUE, DUE_AT), '/', { strict: true })
    await waitForVerifiedWorkspace()
    await settle()

    expect(refreshes()).toHaveLength(1)
  })

  it('a due-but-unexpired session mounts through a transient failure', async () => {
    const refresh = deferRefresh()
    const { hrefWrites } = await bootWith(record(A0_DUE, DUE_AT))

    expect(screen.queryByText('Opening your workspace…'), 'a due boot shows the splash').not.toBeNull()
    await act(async () => refresh.release(UNAVAILABLE))
    await waitForVerifiedWorkspace()
    await settle()

    // D5: the loaders' first requests share one retry, so the count is not pinned.
    expect(calls[0]?.url, 'the boot renewal comes first').toBe(REFRESH)
    expect(refreshes().length).toBeGreaterThanOrEqual(1)
    expect(apiCalls().length).toBeGreaterThan(0)
    expect(apiCalls().filter((c) => c.duringRefresh), 'no request goes out while a refresh is unanswered').toEqual([])
    expect(apiCalls().filter((c) => c.auth !== `Bearer ${A0_DUE}`)).toEqual([])
    expect(storedRecord()?.token).toBe(A0_DUE)
    expect(hrefWrites).toEqual([])
  })
})

describe('a refused renewal returns to landing with the destination (AC-4, AC-6, AC-7)', () => {
  it('a refused renewal at boot returns to landing with the destination', async () => {
    refreshReply = REFUSED
    const { hrefWrites } = await bootWith(record(A0_DUE, DUE_AT), '/audit')

    await waitFor(() => expect(hrefWrites, 'a refused boot renewal navigates to landing').toHaveLength(1))
    await settle()

    expect(hrefWrites).toHaveLength(1)
    expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}$`))
    expect(refreshes()).toHaveLength(1)
    expect(apiCalls()).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(readDestination()?.path).toBe('/audit')
  })

  it('mid-session: a refused renewal keeps the destination', async () => {
    const { hrefWrites } = await mountFresh('/invoices')
    refreshReply = REFUSED
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    const err = await probe(capturedCtx)
    await waitFor(() => expect(hrefWrites.length, 'a refused renewal navigates to landing').toBeGreaterThan(0))
    await settle()

    expect(errorName(err)).toBe('SessionEndedError')
    expect(refreshes(mark)).toHaveLength(1)
    expect(probes(mark), 'the waiting request is never sent').toEqual([])
    expect(hrefWrites).toHaveLength(1)
    expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}$`))
    expect(hrefWrites.filter((h) => h === LANDING || h === `${LANDING}/`), "signOut's bare landing").toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(JSON.parse(sessionStorage.getItem(DEEP_LINK_KEY) ?? 'null')?.path).toBe('/invoices')
  })

  it('transient before the deadline lets the request through', async () => {
    const { hrefWrites } = await mountFresh()
    refreshReply = UNAVAILABLE
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    const out = await probe(capturedCtx)

    expect(refreshes(mark), 'the renewal was attempted').toHaveLength(1)
    expect(out).toBe('resolved')
    expect(probes(mark).map((c) => c.auth)).toEqual([`Bearer ${A0_FRESH}`])
    expect(storedRecord()?.token).toBe(A0_FRESH)
    expect(hrefWrites).toEqual([])
  })

  it('transient after the deadline ends the session', async () => {
    const { hrefWrites } = await mountFresh()
    refreshReply = UNAVAILABLE
    vi.setSystemTime(MID_DEADLINE)
    const mark = calls.length

    const err = await probe(capturedCtx)
    await waitFor(() => expect(hrefWrites.length, 'a transient failure past the deadline navigates to landing').toBeGreaterThan(0))
    await settle()

    expect(errorName(err)).toBe('SessionEndedError')
    expect(refreshes(mark)).toHaveLength(1)
    expect(probes(mark)).toEqual([])
    expect(hrefWrites).toHaveLength(1)
    expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}$`))
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })
})

describe('mid-session renewal (AC-5, AC-8, AC-9)', () => {
  it('mid-session: the next request renews first', async () => {
    await mountFresh()
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    const ctx = capturedCtx
    await act(async () => {
      await Promise.all([1, 2, 3].map((i) => ctx!.authedFetch(`${PROBE}/${i}`)))
    })

    expect(calls[mark]?.url, 'the renewal precedes the requests').toBe(REFRESH)
    expect(refreshes(mark), 'concurrent requests share one renewal').toHaveLength(1)
    expect(probes(mark)).toHaveLength(3)
    expect(probes(mark).filter((c) => c.auth !== `Bearer ${renewedToken()}`)).toEqual([])
  })

  it('a persona session never renews', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    vi.stubEnv('VITE_LANDING_URL', LANDING)
    window.history.replaceState(null, '', '/?persona=firm')
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    await settle()
    const login = calls.filter((c) => c.url === `${GATEWAY}/auth/login`)
    expect(login).toHaveLength(1)
    vi.setSystemTime(NOW + 2 * HOUR)
    const mark = calls.length

    const out = await probe(capturedCtx)

    expect(out).toBe('resolved')
    expect(probes(mark)).toHaveLength(1)
    expect(refreshes()).toEqual([])
  })

  it('byte transports use the renewed token', async () => {
    await mountFresh()
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    let token: string | null = null
    const ctx = capturedCtx
    await act(async () => {
      token = await ctx!.getToken()
    })

    expect(token).toBe(renewedToken())
    expect(refreshes(mark)).toHaveLength(1)
  })
})

describe('a renewal never outlives its session (AC-14, AC-15, AC-17)', () => {
  it('sign-out during a renewal stays signed out', async () => {
    const { hrefWrites } = await mountFresh()
    const refresh = deferRefresh()
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    const ctx = capturedCtx
    let pending: Promise<unknown> = Promise.resolve()
    await act(async () => {
      pending = ctx!.authedFetch(PROBE).then(
        () => 'resolved',
        (e: unknown) => e,
      )
    })
    await act(async () => {
      ctx!.signOut()
    })
    const afterSignOut = [...hrefWrites]
    await act(async () => refresh.release(renewed()))
    await act(async () => {
      await pending
    })
    await settle()

    expect(probes(mark), 'the waiting request is never sent').toEqual([])
    expect(refreshes(mark), 'the request waited on a renewal').toHaveLength(1)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(document.querySelector('.pf-shell'), 'no workspace returns').toBeNull()
    expect(afterSignOut[0]).toBe(LANDING)
    expect(hrefWrites, 'the settled renewal adds no navigation').toEqual(afterSignOut)
  })

  it("another user's record survives a foreign-subject end", async () => {
    const { hrefWrites } = await mountFresh()
    // Another tab signed in a different user.
    const X_RAW = record(jwt(OTHER_ME, nowSec(NOW), 'X'), NOW, OTHER_ME, 'RX')
    localStorage.setItem(SESSION_KEY, X_RAW)
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    const err = await probe(capturedCtx)
    await waitFor(() => expect(hrefWrites.length, 'the foreign record ends this session').toBeGreaterThan(0))
    await settle()

    expect(errorName(err)).toBe('SessionEndedError')
    expect(localStorage.getItem(SESSION_KEY), "the other user's record is untouched").toBe(X_RAW)
    expect(refreshes(mark), 'nothing is renewed for the foreign record').toEqual([])
    expect(probes(mark)).toEqual([])
  })

  it('returning from a stand-in renews the seat', async () => {
    vi.stubEnv('VITE_DEMO_MODE', 'true')
    await mountFresh()
    const seatMember: Member = {
      id: ME.user.id,
      name: APP_PERSONAS.firm.name,
      initials: APP_PERSONAS.firm.initials,
      email: null,
      role: 'admin',
      status: 'active',
      isYou: true,
    }
    const standIn: Member = {
      id: OTHER_ME.user.id,
      name: 'Tunde Bello',
      initials: 'TB',
      email: 'tunde@example.ng',
      role: 'preparer',
      status: 'active',
      isYou: false,
    }
    meReply = answer(200, OTHER_ME)
    await act(async () => {
      await capturedCtx!.becomePersona!(standIn, 'dashboard')
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe('Tunde Bello'))
    await settle()
    expect(refreshes(), 'a stand-in never renews').toEqual([])
    meReply = answer(200, ME)
    vi.setSystemTime(NOW + 2 * HOUR)
    const mark = calls.length

    await act(async () => {
      await capturedCtx!.returnToSeat!('dashboard', seatMember)
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe(APP_PERSONAS.firm.name))
    await settle()
    const ctx = capturedCtx
    const out = await probe(ctx)

    expect(out).toBe('resolved')
    expect(refreshes(mark), 'the seat renews on return').toHaveLength(1)
    expect(calls[mark]?.url, 'the renewal precedes every request').toBe(REFRESH)
    expect(apiCalls(mark).length).toBeGreaterThan(0)
    expect(apiCalls(mark).filter((c) => c.auth !== `Bearer ${renewedToken()}`)).toEqual([])
  })
})
