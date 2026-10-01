// @vitest-environment jsdom
// Every request waits for a due renewal; a refused one returns to landing.

import { StrictMode } from 'react'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'

import { APP_PERSONAS, type Me } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { DEEP_LINK_KEY, readDestination } from './lib/deepLink'
import type { Member } from './lib/members'
import { SESSION_KEY } from './lib/session'
import { ensureSignInState } from './lib/signInState'
import type { PlatformCtx } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const STATE_RE = '[A-Za-z0-9_-]{43}'
const REFRESH = `${GATEWAY}/auth/refresh`
const PROBE = `${GATEWAY}/api/probe`
const EXCHANGE = `${GATEWAY}/auth/exchange`

const MIN = 60_000
const HOUR = 60 * MIN
const NOW = Date.UTC(2026, 8, 28, 12, 0, 0)
const nowSec = (ms: number) => Math.floor(ms / 1000)

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}
const OTHER_ME: Me = {
  tenant: { id: '44444444-4444-4444-4444-444444444444', name: 'Earlier Holdings', kind: 'firm' },
  user: { id: 'e0000000-0000-0000-0000-000000000004', role: 'authenticated', display_name: 'Ifeanyi Chukwu', email: 'ifeanyi.chukwu@example.com' },
}

const IN_HOUSE_ME: Me = { ...ME, tenant: { ...ME.tenant, kind: 'in_house' } }

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

// The /auth/login mint below answers this token for the DEMO_MODE stand-in.
const standInToken = (at: number) => jwt(OTHER_ME, nowSec(at), 'P')
const SEAT_MEMBER: Member = {
  id: ME.user.id,
  name: APP_PERSONAS.firm.name,
  initials: APP_PERSONAS.firm.initials,
  email: null,
  role: 'admin',
  status: 'active',
  isYou: true,
}
const STAND_IN: Member = {
  id: OTHER_ME.user.id,
  name: 'Tunde Bello',
  initials: 'TB',
  email: 'tunde@example.ng',
  role: 'preparer',
  status: 'active',
  isYou: false,
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
let exchangeReply: Reply = answer(500, { error: 'no exchange expected' })

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
      // As the gateway: no bearer, no identity.
      if (url.startsWith(PROBE) && !init?.headers?.get('Authorization')) return answer(401, { error: 'unauthorized' })()
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return meReply()
      if (url === EXCHANGE) return exchangeReply()
      if (url === `${GATEWAY}/auth/login`) {
        return answer(200, { access_token: standInToken(Date.now()) })()
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

// The multipart transport: each send is recorded in `calls`, ordered against the refresh.
class FakeXhr {
  url = ''
  auth: string | null = null
  upload = { onprogress: null, onload: null }
  onload = null
  onerror = null
  ontimeout = null
  open(_method: string, url: string) {
    this.url = url
  }
  setRequestHeader(name: string, value: string) {
    if (name === 'Authorization') this.auth = value
  }
  send() {
    calls.push({ url: `xhr:${this.url}`, auth: this.auth, body: undefined, duringRefresh: refreshesOut > 0 })
  }
}

const refreshes = (from = 0) => calls.slice(from).filter((c) => c.url === REFRESH)
const apiCalls = (from = 0) => calls.slice(from).filter((c) => c.url.startsWith(`${GATEWAY}/api/`))
const probes = (from = 0) => calls.slice(from).filter((c) => c.url.startsWith(PROBE))
const errorName = (e: unknown) => (e instanceof Error ? e.name : String(e))

// Records any node carrying `text` that was ever added, even one removed in the same act().
function watchForText(text: string) {
  let seen = false
  const observer = new MutationObserver((records) => {
    for (const r of records) for (const n of r.addedNodes) if (n.textContent?.includes(text)) seen = true
  })
  observer.observe(document.body, { childList: true, subtree: true })
  onTestFinished(() => observer.disconnect())
  return { seen: () => seen }
}

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

async function waitForVerifiedWorkspace(tenantName = ME.tenant.name, who = { name: ME.user.display_name, initials: 'AN' }) {
  await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
  expect(capturedCtx?.user).toEqual({
    name: who.name,
    initials: who.initials,
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

// A second page load in the same browser: storage stays, the DOM and location go.
async function reload(path: string) {
  cleanup()
  capturedCtx = undefined
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  return bootWith(null, path)
}

// A hand-off boot whose code answers `token` with refresh token RH.
async function bootHandoff(token: string) {
  exchangeReply = answer(200, { access_token: token, refresh_token: 'RH' })
  ensureSignInState()
  const nav = await bootWith(null, `/?handoff=${'a'.repeat(43)}`)
  await waitForVerifiedWorkspace()
  await settle()
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
  exchangeReply = answer(500, { error: 'no exchange expected' })
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
    const splash = watchForText('Opening your workspace…')
    await bootWith(record(A0_DUE, DUE_AT))

    expect(splash.seen(), 'control: the watcher sees the splash').toBe(true)
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

  it('a due in-house hand-off session renews and stays in-house', async () => {
    await bootWith(record(A0_DUE, DUE_AT, IN_HOUSE_ME))
    await waitForVerifiedWorkspace()
    await settle()

    expect(refreshes(), 'the record renewed').toHaveLength(1)
    expect(capturedCtx?.mode).toBe('inhouse')
    expect(storedRecord()?.token).toBe(renewedToken())
    expect((storedRecord()?.me as Me | undefined)?.tenant.kind, 'the renewed record keeps its kind').toBe('in_house')
  })

  it('a fresh session mounts with no renewal', async () => {
    const splash = watchForText('Opening your workspace…')
    await bootWith(record(A0_FRESH, FRESH_AT))

    expect(splash.seen(), 'the splash never renders, not even for one commit').toBe(false)
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

  it('an expired renewable hand-off session loses to ?persona=', async () => {
    const { hrefWrites } = await bootWith(record(A0_OLD, OLD_AT), '/?persona=firm')
    await waitFor(() => expect(capturedCtx?.user, 'the persona workspace must mount').toBeDefined())
    await settle()

    expect(calls.filter((c) => c.url === `${GATEWAY}/auth/login`), 'the persona is minted').toHaveLength(1)
    expect(refreshes(), 'the stored refresh token is never sent').toEqual([])
    expect(storedRecord()?.handoff, 'the persona session replaces the record').toBeUndefined()
    expect(storedRecord()?.refresh_token).toBeUndefined()
    expect(storedRecord()?.token).toBe(standInToken(NOW))
    expect(window.location.search).toBe('')
    expect(hrefWrites).toEqual([])
  })

  // Due, so the workspace waits on the renewal: only the boot strip clears the param meanwhile.
  it('a live renewable hand-off session still beats ?persona=', async () => {
    const refresh = deferRefresh()
    const { hrefWrites } = await bootWith(record(A0_DUE, DUE_AT), '/?persona=firm')

    expect(screen.queryByText('Opening your workspace…'), 'a due boot shows the splash').not.toBeNull()
    expect(window.location.search, 'the suppressed persona is stripped before the renewal answers').toBe('')

    await act(async () => refresh.release(renewed()))
    await waitForVerifiedWorkspace()
    await settle()

    expect(calls.filter((c) => c.url === `${GATEWAY}/auth/login`), 'the persona is not minted').toEqual([])
    expect(refreshes()).toHaveLength(1)
    expect(apiCalls().length).toBeGreaterThan(0)
    expect(apiCalls().filter((c) => c.auth !== `Bearer ${renewedToken()}`)).toEqual([])
    expect(storedRecord()?.handoff).toBe(true)
    expect(storedRecord()?.refresh_token).toBe('R1')
    expect(window.location.search).toBe('')
    expect(hrefWrites).toEqual([])
  })

  // A code is a sign-in the user just made; only an unexpired stored session beats it.
  it('a ?handoff= code wins over an expired renewable hand-off session', async () => {
    const NEW_T = jwt(OTHER_ME, nowSec(NOW), 'N')
    exchangeReply = answer(200, { access_token: NEW_T, refresh_token: 'RN' })
    meReply = answer(200, OTHER_ME)
    ensureSignInState()
    const { hrefWrites } = await bootWith(record(A0_OLD, OLD_AT), `/?handoff=${'a'.repeat(43)}`)
    await waitForVerifiedWorkspace(OTHER_ME.tenant.name, { name: OTHER_ME.user.display_name, initials: 'IC' })
    await settle()

    expect(calls.filter((c) => c.url === EXCHANGE), 'the code is redeemed').toHaveLength(1)
    expect(refreshes(), 'the old refresh token is never sent').toEqual([])
    expect(storedRecord()?.token).toBe(NEW_T)
    expect(storedRecord()?.refresh_token).toBe('RN')
    expect(storedRecord()?.me).toEqual(OTHER_ME)
    expect(window.location.search).toBe('')
    expect(hrefWrites).toEqual([])
  })

  it('an expired hand-off session without renewal loses to ?persona=', async () => {
    const bare = JSON.stringify({ v: 1, personaId: 'firm', token: A0_OLD, me: ME, verified: true, handoff: true })
    const { hrefWrites } = await bootWith(bare, '/?persona=firm')
    await waitFor(() => expect(capturedCtx?.user, 'the persona workspace must mount').toBeDefined())
    await settle()

    expect(calls.filter((c) => c.url === `${GATEWAY}/auth/login`), 'the persona is minted').toHaveLength(1)
    expect(refreshes()).toEqual([])
    expect(storedRecord()?.handoff, 'the persona session replaces the record').toBeUndefined()
    expect(storedRecord()?.token).toBe(standInToken(NOW))
    expect(window.location.search).toBe('')
    expect(hrefWrites).toEqual([])
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

    // The loaders' first requests share one retry, so the count is not pinned.
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
    expect(capturedCtx, 'the workspace never mounts').toBeUndefined()
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

  // The refresh token may still be valid: the record stays for the next boot to renew.
  it('transient after the deadline ends the session', async () => {
    const { hrefWrites } = await mountFresh('/invoices')
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
    expect(JSON.parse(sessionStorage.getItem(DEEP_LINK_KEY) ?? 'null')?.path).toBe('/invoices')
    expect(storedRecord(), 'the stored record survives a transient end').not.toBeNull()
    expect(storedRecord()?.token).toBe(A0_FRESH)
    expect(storedRecord()?.refresh_token).toBe('R0')
  })
})

describe('an ended renewal and the next boot', () => {
  // The refused record is gone, so the next ?persona= link signs the persona in without a refresh.
  it('a refused renewal at boot clears the record for the next persona link', async () => {
    refreshReply = REFUSED
    const first = await bootWith(record(A0_OLD, OLD_AT), '/')
    await waitFor(() => expect(first.hrefWrites, 'a refused boot renewal navigates to landing').toHaveLength(1))
    await settle()

    expect(first.hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}$`))
    expect(refreshes()).toHaveLength(1)
    expect(calls.filter((c) => c.url === `${GATEWAY}/auth/login`)).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()

    const mark = calls.length
    const second = await reload('/?persona=firm')
    await waitFor(() => expect(capturedCtx?.user, 'the persona workspace must mount').toBeDefined())
    await settle()

    expect(calls.slice(mark).filter((c) => c.url === `${GATEWAY}/auth/login`), 'the persona is minted').toHaveLength(1)
    expect(refreshes(mark), 'the refused record is never tried again').toEqual([])
    expect(storedRecord()?.token).toBe(standInToken(NOW))
    expect(second.hrefWrites).toEqual([])
  })

  // The refresh token may outlive the access token, so the next boot tries it again.
  it('a transient failure at boot past the deadline keeps the record for the next boot', async () => {
    refreshReply = UNAVAILABLE
    const first = await bootWith(record(A0_OLD, OLD_AT), '/audit')
    await waitFor(() => expect(first.hrefWrites, 'a failed boot renewal navigates to landing').toHaveLength(1))
    await settle()

    expect(first.hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}$`))
    expect(refreshes()).toHaveLength(1)
    expect(apiCalls(), 'the expired token is never sent').toEqual([])
    expect(capturedCtx, 'the workspace never mounts').toBeUndefined()
    expect(readDestination()?.path).toBe('/audit')
    expect(storedRecord()?.token).toBe(A0_OLD)
    expect(storedRecord()?.refresh_token).toBe('R0')

    refreshReply = renewed()
    const mark = calls.length
    const second = await reload('/')
    await waitForVerifiedWorkspace()
    await settle()

    expect(calls[mark]?.url, 'the next boot renews the kept record first').toBe(REFRESH)
    expect(calls[mark]?.body).toEqual({ refresh_token: 'R0' })
    expect(storedRecord()?.refresh_token).toBe('R1')
    expect(second.hrefWrites).toEqual([])
  })
})

describe('a kept record and a ?persona= link', () => {
  // A kept record must not beat a ?persona= link, or while renewal is down every link bounces to landing.
  it('a ?persona= link after a transient boot failure does not bounce to landing again', async () => {
    refreshReply = UNAVAILABLE
    const first = await bootWith(record(A0_OLD, OLD_AT), '/')
    await waitFor(() => expect(first.hrefWrites, 'a failed boot renewal navigates to landing').toHaveLength(1))
    await settle()

    expect(refreshes(), 'the first boot tries the renewal').toHaveLength(1)
    expect(capturedCtx, 'the workspace never mounts').toBeUndefined()
    expect(storedRecord()?.token, 'the transient end keeps the record').toBe(A0_OLD)
    expect(storedRecord()?.refresh_token).toBe('R0')

    const mark = calls.length
    const second = await reload('/?persona=firm')
    await waitFor(() => expect(capturedCtx?.user, 'the persona workspace must mount').toBeDefined())
    await settle()

    expect(calls.slice(mark).filter((c) => c.url === `${GATEWAY}/auth/login`), 'the persona is minted').toHaveLength(1)
    expect(refreshes(mark), 'the kept refresh token is not sent').toEqual([])
    expect(second.hrefWrites, 'no second bounce to landing').toEqual([])
    expect(storedRecord()?.token).toBe(standInToken(NOW))
    expect(storedRecord()?.handoff).toBeUndefined()
    expect(window.location.search).toBe('')
  })
})

describe('a request after the session ended sends nothing (D-1)', () => {
  it('a request after a refused renewal sends nothing', async () => {
    const { hrefWrites } = await mountFresh('/invoices')
    refreshReply = REFUSED
    vi.setSystemTime(MID_RENEW_AT)
    // A chain that outlives the workspace, e.g. the document-run poll, keeps this ctx.
    const stale = capturedCtx
    await probe(stale)
    await waitFor(() => expect(hrefWrites.length, 'a refused renewal navigates to landing').toBeGreaterThan(0))
    await settle()
    const navigated = [...hrefWrites]
    const mark = calls.length

    const late = await probe(stale, '/late')
    await settle()

    expect(probes(mark), 'a request after the session ended is never sent').toEqual([])
    expect(errorName(late)).toBe('SessionEndedError')
    expect(navigated).toHaveLength(1)
    expect(hrefWrites, "no signOut bare-landing navigation is added").toEqual(navigated)
    expect(JSON.parse(sessionStorage.getItem(DEEP_LINK_KEY) ?? 'null')?.path).toBe('/invoices')
  })

  it('a request after sign-out sends nothing', async () => {
    const { hrefWrites } = await mountFresh('/invoices')
    const stale = capturedCtx
    await act(async () => {
      stale!.signOut()
    })
    await settle()
    const navigated = [...hrefWrites]
    const mark = calls.length

    const late = await probe(stale, '/late')
    await settle()

    expect(probes(mark), 'a request after sign-out is never sent').toEqual([])
    expect(errorName(late)).toBe('SessionEndedError')
    expect(navigated[0]).toBe(LANDING)
    expect(hrefWrites, 'no second sign-out navigation').toEqual(navigated)
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
    const { hrefWrites } = interceptHref()
    await bootApp()
    // The persona keeps its own name although /me answers a display name (D1).
    await waitForVerifiedWorkspace(ME.tenant.name, { name: APP_PERSONAS.firm.name, initials: APP_PERSONAS.firm.initials })
    expect(capturedCtx?.handoff, 'a persona session is not a hand-off session').toBe(false)
    await settle()
    const login = calls.filter((c) => c.url === `${GATEWAY}/auth/login`)
    expect(login).toHaveLength(1)
    vi.setSystemTime(NOW + 2 * HOUR)
    const mark = calls.length

    const out = await probe(capturedCtx)

    expect(out).toBe('resolved')
    expect(probes(mark).map((c) => c.auth), 'the expired persona token is still sent').toEqual([`Bearer ${standInToken(NOW)}`])
    expect(refreshes()).toEqual([])
    expect(hrefWrites).toEqual([])
  })

  // The code may have waited HandoffTTL (60 s) in the gateway, so receipt counts from then.
  it('a hand-off session renews at 80% of its lifetime from the backdated receipt', async () => {
    const H0 = jwt(ME, nowSec(NOW - MIN), 'H0')
    const { hrefWrites } = await bootHandoff(H0)
    const renewAt = NOW - MIN + 0.8 * HOUR

    vi.setSystemTime(renewAt - 1000)
    let mark = calls.length
    expect(await probe(capturedCtx)).toBe('resolved')
    expect(refreshes(mark), 'not due one second before').toEqual([])
    expect(probes(mark).map((c) => c.auth)).toEqual([`Bearer ${H0}`])

    vi.setSystemTime(renewAt)
    mark = calls.length
    expect(await probe(capturedCtx)).toBe('resolved')
    expect(calls[mark]?.url, 'due: the renewal comes first').toBe(REFRESH)
    expect(calls[mark]?.body).toEqual({ refresh_token: 'RH' })
    expect(probes(mark).map((c) => c.auth)).toEqual([`Bearer ${renewedToken()}`])
    expect(hrefWrites).toEqual([])
  })

  // The token's exp is 59 min after receipt; failing renewals must end the session by then.
  it('a hand-off token is never sent at its exp while renewal keeps failing', async () => {
    const H0 = jwt(ME, nowSec(NOW - MIN), 'H0')
    const { hrefWrites } = await bootHandoff(H0)
    refreshReply = UNAVAILABLE
    vi.setSystemTime(NOW - MIN + HOUR)
    const mark = calls.length

    const err = await probe(capturedCtx)
    await waitFor(() => expect(hrefWrites.length, 'the session ends at exp').toBeGreaterThan(0))
    await settle()

    expect(errorName(err)).toBe('SessionEndedError')
    expect(refreshes(mark)).toHaveLength(1)
    expect(probes(mark), 'the expired token is never sent').toEqual([])
    expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}$`))
    expect(storedRecord()?.refresh_token).toBe('RH')
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

  it('the import upload waits for the renewal and carries the renewed token', async () => {
    vi.stubGlobal('XMLHttpRequest', FakeXhr)
    await mountFresh()
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    act(() => capturedCtx!.addPickedFiles([new File(['invoice_number,total\nINV-1,100'], 'a.csv', { type: 'text/csv' })]))
    act(() => capturedCtx!.readAllColumns())
    const uploads = () => calls.slice(mark).filter((c) => c.url === `xhr:${GATEWAY}/api/invoice/v1/imports/preview`)
    await waitFor(() => expect(uploads(), 'the preview upload was never sent').toHaveLength(1))

    expect(calls[mark]?.url, 'the renewal precedes the upload').toBe(REFRESH)
    expect(refreshes(mark)).toHaveLength(1)
    expect(uploads()[0]?.duringRefresh).toBe(false)
    expect(uploads()[0]?.auth).toBe(`Bearer ${renewedToken()}`)
  })
})

describe('a renewed hand-off session stays a hand-off session (AUTH-10-06, F17)', () => {
  it('a hand-off session still carries handoff:true after a renewal', async () => {
    await mountFresh()
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    expect(await probe(capturedCtx)).toBe('resolved')
    await settle()

    expect(refreshes(mark), 'the renewal ran').toHaveLength(1)
    expect(storedRecord()?.token, 'the renewed token is stored').toBe(renewedToken())
    expect(capturedCtx?.handoff).toBe(true)
  })

  it('a hand-off session renewed at boot carries handoff:true', async () => {
    await bootWith(record(A0_DUE, DUE_AT))
    await waitForVerifiedWorkspace()
    await settle()
    expect(refreshes(), 'the boot renewal ran').toHaveLength(1)
    expect(capturedCtx?.handoff).toBe(true)
  })

  it('a stand-in is not a hand-off session, and the seat is again after the return', async () => {
    vi.stubEnv('VITE_DEMO_MODE', 'true')
    await mountFresh()
    expect(capturedCtx?.handoff, 'the seat').toBe(true)
    meReply = answer(200, OTHER_ME)
    await act(async () => {
      await capturedCtx!.becomePersona!(STAND_IN, 'dashboard')
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe('Tunde Bello'))
    expect(capturedCtx?.handoff, 'the stand-in').toBe(false)
    meReply = answer(200, ME)
    await act(async () => {
      await capturedCtx!.returnToSeat!('dashboard', SEAT_MEMBER)
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe(ME.user.display_name))
    expect(capturedCtx?.handoff, 'back on the seat').toBe(true)
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

  it('a stand-in begun during a seat renewal keeps its own token', async () => {
    vi.stubEnv('VITE_DEMO_MODE', 'true')
    const { hrefWrites } = await mountFresh()
    const refresh = deferRefresh()
    vi.setSystemTime(MID_RENEW_AT)
    const mark = calls.length

    const seatCtx = capturedCtx
    let pending: Promise<unknown> = Promise.resolve()
    await act(async () => {
      pending = seatCtx!.authedFetch(`${PROBE}/seat`).then(
        () => 'resolved',
        (e: unknown) => e,
      )
    })
    meReply = answer(200, OTHER_ME)
    await act(async () => {
      await seatCtx!.becomePersona!(STAND_IN, 'dashboard')
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe('Tunde Bello'))
    await act(async () => refresh.release(renewed()))
    let seatOut: unknown
    await act(async () => {
      seatOut = await pending
    })
    await settle()
    const out = await probe(capturedCtx, '/stand-in')

    expect(refreshes(mark), 'the seat request waited on a renewal').toHaveLength(1)
    expect(errorName(seatOut)).toBe('SessionEndedError')
    expect(out).toBe('resolved')
    expect(probes(mark).map((c) => [c.url, c.auth]), 'only the stand-in request is sent, on its own token').toEqual([
      [`${PROBE}/stand-in`, `Bearer ${standInToken(MID_RENEW_AT)}`],
    ])
    expect(storedRecord()?.token, 'the discarded answer is never stored').toBe(A0_FRESH)
    expect(storedRecord()?.refresh_token).toBe('R0')
    expect(hrefWrites).toEqual([])
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

  it('a stand-in from an in-house hand-off seat is in-house, and so is the return to the seat', async () => {
    vi.stubEnv('VITE_DEMO_MODE', 'true')
    await bootWith(record(A0_FRESH, FRESH_AT, IN_HOUSE_ME))
    await waitForVerifiedWorkspace()
    await settle()
    expect(capturedCtx?.mode, 'the seat').toBe('inhouse')
    meReply = answer(200, OTHER_ME)
    await act(async () => {
      await capturedCtx!.becomePersona!(STAND_IN, 'dashboard')
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe('Tunde Bello'))
    expect(capturedCtx?.mode, 'the stand-in').toBe('inhouse')
    meReply = answer(200, IN_HOUSE_ME)
    await act(async () => {
      await capturedCtx!.returnToSeat!('dashboard', SEAT_MEMBER)
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe(ME.user.display_name))
    expect(capturedCtx?.mode, 'back on the seat').toBe('inhouse')
  })

  it('returning from a stand-in renews the seat', async () => {
    vi.stubEnv('VITE_DEMO_MODE', 'true')
    await mountFresh()
    meReply = answer(200, OTHER_ME)
    await act(async () => {
      await capturedCtx!.becomePersona!(STAND_IN, 'dashboard')
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe('Tunde Bello'))
    await settle()
    expect(refreshes(), 'a stand-in never renews').toEqual([])
    expect(await probe(capturedCtx, '/stand-in')).toBe('resolved')
    expect(
      probes().map((c) => c.auth),
      "a stand-in's requests carry its own token",
    ).toEqual([`Bearer ${standInToken(NOW)}`])
    meReply = answer(200, ME)
    vi.setSystemTime(NOW + 2 * HOUR)
    const mark = calls.length

    await act(async () => {
      await capturedCtx!.returnToSeat!('dashboard', SEAT_MEMBER)
    })
    await waitFor(() => expect(capturedCtx?.user.name).toBe(ME.user.display_name))
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
