// @vitest-environment jsdom
// AUTH-05-08 Mode A: the app redeems a landing hand-off code (D8, D9, D18, D23, D25).

import { StrictMode } from 'react'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from './auth'
import { captureDestination } from './lib/deepLink'
import { SESSION_KEY, parseStoredSession, serializeSession } from './lib/session'
import { ensureSignInState } from './lib/signInState'
import { EMPTY_BUCKET } from './lib/dashboard'
import type { PlatformCtx } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const STATE_KEY = 'invoice-os.signInState'
const STATE_RE = '[A-Za-z0-9_-]{43}'
const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated' },
}
const OLD_ME: Me = {
  tenant: { id: '44444444-4444-4444-4444-444444444444', name: 'Earlier Holdings' },
  user: { id: 'e0000000-0000-0000-0000-000000000004', role: 'authenticated' },
}

function jwt(sub: string, exp: number): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64({ sub, exp })}.sig`
}
const nowSec = () => Math.floor(Date.now() / 1000)
const T = jwt(ME.user.id, nowSec() + 3600)

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

// Real jsdom location (replaceState strips stay observable); href writes are recorded only.
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

function storedState(): string | null {
  const raw = sessionStorage.getItem(STATE_KEY)
  if (raw == null) return null
  try {
    const s = JSON.parse(raw)?.s
    return typeof s === 'string' ? s : null
  } catch {
    return null
  }
}

type Reply = () => Promise<unknown>
const ok = (body: unknown): Reply => () => Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body) })
const fail = (status: number, error: string): Reply => () =>
  Promise.resolve({ ok: false, status, statusText: String(status), json: () => Promise.resolve({ error }) })
const networkDown: Reply = () => Promise.reject(new TypeError('Failed to fetch'))

let fetchUrls: string[] = []
let exchangeBodies: unknown[] = []
let meAuth: (string | null)[] = []
let exchangeAuth: (string | null)[] = []
let loginCalls = 0
let exchangeReply: Reply = ok({ access_token: T })
let meReply: Reply = ok(ME)

function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; headers?: Headers; body?: string }) => {
      fetchUrls.push(url)
      if (url === `${GATEWAY}/auth/exchange`) {
        exchangeBodies.push(JSON.parse(init?.body ?? 'null'))
        exchangeAuth.push(init?.headers?.get('Authorization') ?? null)
        return exchangeReply()
      }
      if (url === `${GATEWAY}/api/tenancy/v1/me`) {
        meAuth.push(init?.headers?.get('Authorization') ?? null)
        return meReply()
      }
      if (url === `${GATEWAY}/auth/login`) {
        loginCalls++
        return ok({ access_token: jwt(APP_PERSONAS.firm.subject, nowSec() + 3600) })()
      }
      return ok({
        entities: [],
        policies: [],
        members: [],
        roles: [],
        invoices: [],
        total: 0,
        clients: [],
        totals: EMPTY_BUCKET,
        rejection_reasons: [],
      })()
    }),
  )
}

// Lets pending fetches and effects run, so a late write would be seen.
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

// Hand-written so the red phase does not depend on serializeSession learning `handoff`.
function handoffRecord(token: string, me: Me): string {
  return JSON.stringify({ v: 1, personaId: 'firm', token, me, verified: true, handoff: true })
}

function historyUrls(spies: { mock: { calls: unknown[][] } }[]): string[] {
  return spies.flatMap((s) => s.mock.calls.map((c) => String(c[2] ?? '')))
}

let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
  capturedCtx = undefined
  fetchUrls = []
  exchangeBodies = []
  meAuth = []
  exchangeAuth = []
  loginCalls = 0
  exchangeReply = ok({ access_token: T })
  meReply = ok(ME)
  routeFetch()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  window.history.replaceState(null, '', '/')
})

function configure(opts: { gateway?: boolean; landing?: boolean } = {}) {
  if (opts.gateway ?? true) vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  if (opts.landing ?? true) vi.stubEnv('VITE_LANDING_URL', LANDING)
}

async function waitForVerifiedWorkspace() {
  await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
  expect(capturedCtx?.user).toEqual({
    name: APP_PERSONAS.firm.name,
    initials: APP_PERSONAS.firm.initials,
    tenantName: ME.tenant.name,
    verified: true,
  })
}

describe('a hand-off boot redeems the code (AC-1, D9, D25)', () => {
  it('a hand-off boot redeems, reads /me and mounts verified', async () => {
    configure()
    const S = ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(exchangeBodies).toHaveLength(1))
    expect(exchangeBodies).toEqual([{ code: CODE, state: S }])
    await waitForVerifiedWorkspace()
    expect(fetchUrls.slice(0, 2)).toEqual([`${GATEWAY}/auth/exchange`, `${GATEWAY}/api/tenancy/v1/me`])
    expect(meAuth).toEqual([`Bearer ${T}`])
    expect(sessionStorage.getItem(STATE_KEY), 'the state is consumed').toBeNull()
    expect(hrefWrites, 'the front door waits on the redemption').toEqual([])
    const rec = storedRecord()
    expect(rec?.handoff).toBe(true)
    expect(rec?.me).toEqual(ME)
  })

  it('a tab without a state never redeems', async () => {
    configure()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}&signin=failed$`))
    expect(exchangeBodies).toHaveLength(0)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(window.location.search).toBe('')
  })

  it('?handoff= wins over ?auth=start', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}&auth=start`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(exchangeBodies).toHaveLength(1))
    await waitForVerifiedWorkspace()
    expect(hrefWrites.filter((h) => h.includes('signin=ready'))).toEqual([])
    expect(hrefWrites).toEqual([])
    expect(window.location.search).toBe('')
  })
})

describe('the code leaves the URL (AC-3, AC-4)', () => {
  it('the code leaves the URL on success', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const replace = vi.spyOn(window.history, 'replaceState')
    const push = vi.spyOn(window.history, 'pushState')
    interceptHref()
    await bootApp()
    expect(window.location.search, 'stripped at mount, before redemption resolves').not.toContain('handoff')
    await waitForVerifiedWorkspace()
    expect(window.location.search).not.toContain('handoff')
    const urls = historyUrls([replace, push])
    expect(urls.length, 'the strip writes history').toBeGreaterThan(0)
    expect(urls.filter((u) => u.includes('handoff'))).toEqual([])
  })

  it('the code leaves the URL on failure', async () => {
    configure()
    ensureSignInState()
    exchangeReply = fail(400, 'invalid or expired code')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const replace = vi.spyOn(window.history, 'replaceState')
    const push = vi.spyOn(window.history, 'pushState')
    const { hrefWrites } = interceptHref()
    await bootApp()
    expect(window.location.search).not.toContain('handoff')
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    const urls = historyUrls([replace, push])
    expect(urls.length).toBeGreaterThan(0)
    expect(urls.filter((u) => u.includes('handoff'))).toEqual([])
    expect(hrefWrites.filter((u) => u.includes('handoff'))).toEqual([])
  })

  it('no written URL contains the token', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const replace = vi.spyOn(window.history, 'replaceState')
    const push = vi.spyOn(window.history, 'pushState')
    const { hrefWrites } = interceptHref()
    await bootApp()
    // Positive half: the token did arrive.
    await waitFor(() => expect(meAuth).toEqual([`Bearer ${T}`]))
    await waitForVerifiedWorkspace()
    const written = [...historyUrls([replace, push]), ...hrefWrites, window.location.href]
    expect(written.length).toBeGreaterThan(0)
    expect(written.filter((u) => u.includes(T))).toEqual([])
  })
})

describe('a hand-off session persists (AC-5, AC-6, AC-8)', () => {
  it('a reload resumes without redeeming again', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(exchangeBodies).toHaveLength(1))
    await waitForVerifiedWorkspace()
    expect(storedRecord()?.handoff).toBe(true)

    cleanup()
    capturedCtx = undefined
    exchangeBodies = []
    meAuth = []
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    window.history.replaceState(null, '', '/')
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(exchangeBodies).toHaveLength(0)
    expect(hrefWrites).toEqual([])
  })

  it('a captured destination is restored after the hand-off', async () => {
    configure()
    captureDestination('/audit', '', Date.now())
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(capturedCtx?.view).toBe('audit')
    expect(window.location.pathname).toBe('/audit')
  })
})

describe('a failed redemption bounces to landing (AC-7, D23)', () => {
  it('a failed redemption stores nothing and reports failed', async () => {
    const cases: [string, Reply][] = [
      ['exchange 400', fail(400, 'invalid or expired code')],
      ['network error', networkDown],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [name, reply] of cases) {
      configure()
      ensureSignInState()
      exchangeReply = reply
      exchangeBodies = []
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      window.history.replaceState(null, '', `/?handoff=${CODE}`)
      const { hrefWrites } = interceptHref()
      await bootApp()
      await waitFor(() => expect(hrefWrites, name).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
      expect(storedState(), name).toEqual(expect.stringMatching(new RegExp(`^${STATE_RE}$`)))
      expect(exchangeBodies, name).toHaveLength(1)
      expect(localStorage.getItem(SESSION_KEY), name).toBeNull()
      expect(warn, name).toHaveBeenCalledTimes(1)
      cleanup()
      warn.mockRestore()
      sessionStorage.clear()
      if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
      window.history.replaceState(null, '', '/')
    }
  })

  it('a workspace-less account reports no-workspace', async () => {
    configure()
    ensureSignInState()
    meReply = fail(403, 'forbidden')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=no-workspace`]))
    expect(exchangeBodies).toHaveLength(1)
    expect(meAuth).toEqual([`Bearer ${T}`])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('a failed hand-off over a persona session leaves no session', async () => {
    configure()
    const persona: Session = { persona: APP_PERSONAS.firm, token: jwt(APP_PERSONAS.firm.subject, nowSec() + 3600), me: null, verified: true }
    localStorage.setItem(SESSION_KEY, serializeSession(persona))
    ensureSignInState()
    exchangeReply = fail(400, 'invalid or expired code')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(exchangeBodies).toHaveLength(1)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('a failed hand-off with no landing URL shows the picker', async () => {
    configure({ landing: false })
    ensureSignInState()
    exchangeReply = fail(400, 'invalid or expired code')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(exchangeBodies).toHaveLength(1))
    await waitFor(() => expect(screen.getByText('Choose an account')).toBeTruthy())
    expect(hrefWrites).toEqual([])
    expect(window.location.href).not.toContain('null')
    expect(window.location.search).toBe('')
  })
})

describe('precedence (AC-9..AC-13, D9, D18)', () => {
  it('the hand-off wins over ?persona=', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}&persona=firm`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(exchangeBodies).toHaveLength(1))
    await waitForVerifiedWorkspace()
    expect(loginCalls).toBe(0)
    expect(window.location.search).toBe('')
  })

  it('a malformed code is stripped and ignored', async () => {
    configure()
    window.history.replaceState(null, '', '/?handoff=short')
    const { hrefWrites } = interceptHref()
    await bootApp()
    expect(window.location.search).toBe('')
    expect(exchangeBodies).toHaveLength(0)
    expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}`])
    expect(storedState()).toEqual(expect.stringMatching(new RegExp(`^${STATE_RE}$`)))
  })

  it('StrictMode posts the code once', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const warn = vi.spyOn(console, 'warn')
    const { hrefWrites } = interceptHref()
    await bootApp({ strict: true })
    await waitFor(() => expect(exchangeBodies.length).toBeGreaterThan(0))
    await waitForVerifiedWorkspace()
    await settle()
    expect(exchangeBodies).toHaveLength(1)
    // A second effect run finds the state consumed; without the latch it takes the failure arm.
    expect(hrefWrites).toEqual([])
    expect(warn.mock.calls.filter((c) => String(c[0]).includes('hand-off'))).toEqual([])
  })

  it('a live hand-off session is not replaced by a URL', async () => {
    const OLD_T = jwt(OLD_ME.user.id, nowSec() + 3600)
    // Baseline: the same stored session booted with no param. A URL boot must render the same text.
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(OLD_T, OLD_ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    await settle()
    const baseline = document.body.textContent ?? ''
    expect(baseline.length, 'the baseline workspace rendered text').toBeGreaterThan(0)
    cleanup()
    capturedCtx = undefined as PlatformCtx | undefined
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    window.history.replaceState(null, '', '/')
    const urls = [`/?handoff=${CODE}`, '/?persona=firm']
    expect(urls.length).toBeGreaterThan(0)
    for (const url of urls) {
      configure()
      localStorage.setItem(SESSION_KEY, handoffRecord(OLD_T, OLD_ME))
      ensureSignInState()
      exchangeBodies = []
      loginCalls = 0
      window.history.replaceState(null, '', url)
      const { hrefWrites } = interceptHref()
      await bootApp()
      await waitFor(() => expect(capturedCtx?.user, url).toBeDefined())
      expect(exchangeBodies, url).toHaveLength(0)
      expect(loginCalls, url).toBe(0)
      expect(window.location.search, url).toBe('')
      expect(hrefWrites, url).toEqual([])
      expect(capturedCtx?.user.tenantName, url).toBe(OLD_ME.tenant.name)
      const rec = storedRecord()
      expect(rec?.handoff, url).toBe(true)
      expect(rec?.token, url).toBe(OLD_T)
      expect(rec?.me, url).toEqual(OLD_ME)
      // D18 accepted limit (QA N5): no notice is shown.
      await settle()
      expect(document.body.textContent, url).toBe(baseline)
      expect(screen.queryByRole('alert'), url).toBeNull()
      cleanup()
      capturedCtx = undefined
      if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
      window.history.replaceState(null, '', '/')
    }
  })

  it('an expired hand-off session does not block a new hand-off', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(jwt(OLD_ME.user.id, nowSec() - 60), OLD_ME))
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(exchangeBodies).toHaveLength(1))
    await waitForVerifiedWorkspace()
    expect((storedRecord()?.me as Me | undefined)?.user.id).toBe(ME.user.id)
  })

  it('an unconfigured gateway ignores the code', async () => {
    configure({ gateway: false })
    const S = ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    expect(window.location.search).toBe('')
    expect(fetchUrls).toEqual([])
    expect(hrefWrites, 'the front door runs with the unconsumed state').toEqual([`${LANDING}/?state=${S}`])
  })
})

// QA Mode B: adversarial coverage for the redemption.
describe('AUTH-05-08 adversarial', () => {
  function consoleSpies() {
    return (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m).mockImplementation(() => {}))
  }
  function consoleText(spies: { mock: { calls: unknown[][] } }[]): string {
    return spies
      .flatMap((s) => s.mock.calls.flat())
      .map((a) => (a instanceof Error ? `${a.name} ${a.message} ${a.stack ?? ''}` : typeof a === 'string' ? a : (JSON.stringify(a) ?? String(a))))
      .join('\n')
  }
  const stateRemovals = () =>
    (sessionStorage.removeItem as unknown as { mock: { calls: unknown[][] } }).mock.calls.filter((c) => c[0] === STATE_KEY).length
  // Accepts only the token the exchange issued, like the real gateway.
  function gatewayMe() {
    meReply = () => (meAuth[meAuth.length - 1] === `Bearer ${T}` ? ok(ME)() : fail(401, 'unauthorized')())
  }
  function resetTab() {
    cleanup()
    vi.restoreAllMocks()
    vi.stubGlobal('sessionStorage', createMemoryStorage())
    vi.stubGlobal('localStorage', createMemoryStorage())
    routeFetch()
    capturedCtx = undefined
    meAuth = []
    exchangeAuth = []
    exchangeBodies = []
    exchangeReply = ok({ access_token: T })
    meReply = ok(ME)
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    window.history.replaceState(null, '', '/')
  }

  it('an exchange 200 without a usable access_token stores no session', async () => {
    const bodies: [string, unknown][] = [
      ['no access_token', {}],
      ['numeric access_token', { access_token: 12345 }],
      ['null access_token', { access_token: null }],
      ['empty access_token', { access_token: '' }],
    ]
    expect(bodies.length).toBeGreaterThan(0)
    for (const [name, body] of bodies) {
      configure()
      ensureSignInState()
      exchangeReply = ok(body)
      gatewayMe()
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      window.history.replaceState(null, '', `/?handoff=${CODE}`)
      const { hrefWrites } = interceptHref()
      await bootApp()
      await waitFor(() => expect(hrefWrites, name).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
      expect(exchangeBodies, name).toHaveLength(1)
      expect(localStorage.getItem(SESSION_KEY), name).toBeNull()
      expect(capturedCtx, name).toBeUndefined()
      expect(warn, name).toHaveBeenCalledTimes(1)
      resetTab()
    }
  })

  it('a /me 500, 401, network or malformed failure reports failed', async () => {
    const cases: [string, Reply][] = [
      ['me 500', fail(500, 'internal server error')],
      ['me 401', fail(401, 'unauthorized')],
      ['me network', networkDown],
      ['me malformed body', () => Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.reject(new SyntaxError('bad json')) })],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [name, reply] of cases) {
      configure()
      const S = ensureSignInState()
      meReply = reply
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      window.history.replaceState(null, '', `/?handoff=${CODE}`)
      const { hrefWrites } = interceptHref()
      await bootApp()
      await waitFor(() => expect(hrefWrites, name).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
      expect(meAuth, name).toEqual([`Bearer ${T}`])
      expect(storedState(), `${name}: the retry carries a fresh state, not the consumed one`).not.toBe(S)
      expect(localStorage.getItem(SESSION_KEY), name).toBeNull()
      expect(warn, name).toHaveBeenCalledTimes(1)
      expect(hrefWrites.filter((h) => h.includes(T)), name).toEqual([])
      resetTab()
    }
  })

  it('a /me 200 with no tenant reports failed', async () => {
    configure()
    ensureSignInState()
    meReply = ok({ user: ME.user })
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  // Pinned, advisory: cmd/tenancy's MeHandler always returns tenant.id, so this is unreachable.
  // Redemption stores a record the parser rejects; flip if redemption should apply the parse guard.
  it('pinned: a /me 200 with a tenant but no tenant id mounts and stores an unparseable record', async () => {
    configure()
    ensureSignInState()
    meReply = ok({ tenant: { name: 'No Id' }, user: ME.user })
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    expect(storedRecord()?.handoff).toBe(true)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(parseStoredSession(localStorage.getItem(SESSION_KEY))).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  // Pinned: /auth/exchange never answers 403 (signin.go ExchangeHandler: 200/400/405; CORS: 204),
  // so any ApiError 403 is read as /me's. Flip if the arm should key on the failing call.
  it('pinned: an exchange 403 reports no-workspace', async () => {
    configure()
    ensureSignInState()
    exchangeReply = fail(403, 'forbidden')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=no-workspace`]))
    expect(meAuth).toEqual([])
  })

  it('a non-ApiError carrying status 403 reports failed', async () => {
    configure()
    ensureSignInState()
    // apiFetch wraps every fetch failure in ApiError, so the non-ApiError is thrown while reading /me.
    meReply = ok({
      user: ME.user,
      get tenant(): never {
        throw Object.assign(new Error('not an ApiError'), { status: 403 })
      },
    })
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(String(warn.mock.calls[0]?.[1])).toContain('not an ApiError')
  })

  it('a /me 404 reports failed, not no-workspace', async () => {
    configure()
    ensureSignInState()
    meReply = fail(404, 'not found')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
  })

  it('the token and the code never reach a URL, an href or the console', async () => {
    const cases: [string, Reply][] = [
      ['success', ok(ME)],
      ['me 403', fail(403, 'forbidden')],
      ['me 500', fail(500, 'boom')],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [name, reply] of cases) {
      configure()
      ensureSignInState()
      meReply = reply
      const spies = consoleSpies()
      window.history.replaceState(null, '', `/?handoff=${CODE}`)
      const replace = vi.spyOn(window.history, 'replaceState')
      const push = vi.spyOn(window.history, 'pushState')
      const { hrefWrites } = interceptHref()
      await bootApp()
      await waitFor(() => expect(meAuth, `${name}: the token did arrive`).toEqual([`Bearer ${T}`]))
      await settle()
      const written = [...historyUrls([replace, push]), ...hrefWrites, window.location.href]
      expect(written.length, name).toBeGreaterThan(0)
      expect(written.filter((u) => u.includes(T) || u.includes(CODE)), name).toEqual([])
      expect(consoleText(spies).includes(T), `${name}: console output carries the token`).toBe(false)
      expect(consoleText(spies).includes(CODE), `${name}: console output carries the code`).toBe(false)
      resetTab()
    }
  })

  it('no storage write carries the code or the consumed state', async () => {
    configure()
    const S = ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    const raw = localStorage.getItem(SESSION_KEY) ?? ''
    expect(raw).toContain(ME.user.id)
    expect(raw).not.toContain(CODE)
    expect(raw).not.toContain(S)
    const writes = [localStorage, sessionStorage].flatMap((st) =>
      (st.setItem as unknown as { mock: { calls: unknown[][] } }).mock.calls.map((c) => String(c[1])),
    )
    expect(writes.length).toBeGreaterThan(0)
    expect(writes.filter((w) => w.includes(CODE))).toEqual([])
    expect(sessionStorage.getItem(STATE_KEY)).toBeNull()
  })

  it('the state is consumed exactly once on success and on failure', async () => {
    const cases: [string, Reply][] = [
      ['success', ok({ access_token: T })],
      ['failure', fail(400, 'invalid or expired code')],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [name, reply] of cases) {
      configure()
      const S = ensureSignInState()
      exchangeReply = reply
      vi.spyOn(console, 'warn').mockImplementation(() => {})
      window.history.replaceState(null, '', `/?handoff=${CODE}`)
      const { hrefWrites } = interceptHref()
      await bootApp({ strict: true })
      await waitFor(() => expect(exchangeBodies, name).toHaveLength(1))
      await settle()
      expect(exchangeBodies, name).toEqual([{ code: CODE, state: S }])
      expect(stateRemovals(), name).toBe(1)
      if (name === 'failure') {
        expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`])
        expect(storedState(), 'the consumed state is never reused').not.toBe(S)
      } else {
        expect(sessionStorage.getItem(STATE_KEY)).toBeNull()
      }
      resetTab()
    }
  })

  it('a second tab with the same code never exchanges', async () => {
    // First tab fails; the second tab has its own empty sessionStorage and no stored session.
    configure()
    ensureSignInState()
    exchangeReply = fail(400, 'invalid or expired code')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    let { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toHaveLength(1))
    cleanup()
    vi.stubGlobal('sessionStorage', createMemoryStorage())
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    ;({ hrefWrites } = interceptHref())
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(exchangeBodies, 'only the first tab exchanged').toHaveLength(1)

    // First tab succeeds; the second tab shares localStorage, so the live session wins.
    resetTab()
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    cleanup()
    capturedCtx = undefined
    vi.stubGlobal('sessionStorage', createMemoryStorage())
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    ;({ hrefWrites } = interceptHref())
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(exchangeBodies, 'the second tab resumed the stored session').toHaveLength(1)
    expect(hrefWrites).toEqual([])
    expect(window.location.search).toBe('')
  })

  it('pinned: a repeated ?handoff= redeems the first value only', async () => {
    const OTHER = 'QPONMLKJIHGFEDCBAzyxwvutsrqponmlkjihgfedcba'
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}&handoff=${OTHER}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(exchangeBodies.map((b) => (b as { code: string }).code)).toEqual([CODE])
    expect(window.location.search).toBe('')
  })

  it('pinned: a repeated ?handoff= whose first value is malformed is ignored', async () => {
    configure()
    const S = ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=short&handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    expect(exchangeBodies).toHaveLength(0)
    expect(window.location.search).toBe('')
    expect(hrefWrites).toEqual([`${LANDING}/?state=${S}`])
  })

  it('an expired hand-off record on reload goes to the front door', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(jwt(OLD_ME.user.id, nowSec() - 1), OLD_ME))
    const { hrefWrites } = interceptHref()
    await bootApp()
    expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}`])
    expect(storedState()).toEqual(expect.stringMatching(new RegExp(`^${STATE_RE}$`)))
    expect(capturedCtx).toBeUndefined()
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(fetchUrls).toEqual([])
  })

  it('sign-out clears a hand-off session', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(storedRecord()?.handoff).toBe(true)
    await act(async () => {
      capturedCtx?.signOut()
    })
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(hrefWrites[0]).toBe(LANDING)
  })

  it('sign-out of a hand-off session with no landing URL shows the picker', async () => {
    configure({ landing: false })
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    await act(async () => {
      capturedCtx?.signOut()
    })
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(screen.getByText('Choose an account')).toBeTruthy()
    expect(screen.queryByText('Opening your workspace…')).toBeNull()
  })

  it('the Bearer header goes only to /me during redemption', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(exchangeAuth).toEqual([null])
    expect(meAuth).toEqual([`Bearer ${T}`])
  })

  it('StrictMode fails with one warn and one href write', async () => {
    configure()
    ensureSignInState()
    exchangeReply = fail(400, 'invalid or expired code')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp({ strict: true })
    await waitFor(() => expect(hrefWrites.length).toBeGreaterThan(0))
    await settle()
    expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`])
    expect(exchangeBodies).toHaveLength(1)
    expect(warn.mock.calls.filter((c) => String(c[0]).includes('hand-off redemption failed'))).toHaveLength(1)
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('a slow navigation after failure keeps the loading splash and navigates once', async () => {
    configure()
    ensureSignInState()
    exchangeReply = fail(400, 'invalid or expired code')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toHaveLength(1))
    await settle(60)
    expect(hrefWrites).toHaveLength(1)
    expect(screen.getByText('Opening your workspace…')).toBeTruthy()
    expect(screen.queryByText('Choose an account')).toBeNull()
  })

  it('a pending redemption shows "Opening your workspace…" and no picker', async () => {
    configure({ landing: false })
    ensureSignInState()
    exchangeReply = () => new Promise(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(exchangeBodies).toHaveLength(1))
    expect(screen.getByText('Opening your workspace…')).toBeTruthy()
    expect(screen.queryByText(/Signing in as/)).toBeNull()
    expect(screen.queryByText('Choose an account')).toBeNull()
    expect(hrefWrites).toEqual([])
    expect(window.location.search, 'the code leaves before the redemption resolves').toBe('')
  })

  it('?auth=start bounces over a live hand-off session and keeps it', async () => {
    const OLD_T = jwt(OLD_ME.user.id, nowSec() + 3600)
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(OLD_T, OLD_ME))
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    await bootApp()
    expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=ready`])
    expect(storedRecord()?.token).toBe(OLD_T)
    expect(storedRecord()?.handoff).toBe(true)
    expect(exchangeBodies).toHaveLength(0)
  })

  it('pinned: a corrupt record on a ?persona= boot warns once and the persona signs in', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, '{not json')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', '/?persona=firm')
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    expect(loginCalls).toBe(1)
    expect(warn.mock.calls.filter((c) => String(c[0]).startsWith('[session]'))).toHaveLength(1)
  })

  it('a live hand-off session boot writes no history entry carrying handoff or persona', async () => {
    const OLD_T = jwt(OLD_ME.user.id, nowSec() + 3600)
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(OLD_T, OLD_ME))
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}&persona=firm`)
    const replace = vi.spyOn(window.history, 'replaceState')
    const push = vi.spyOn(window.history, 'pushState')
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    await settle()
    const urls = historyUrls([replace, push])
    expect(urls.length).toBeGreaterThan(0)
    expect(urls.filter((u) => /handoff=|persona=/.test(u))).toEqual([])
    expect(window.location.search).toBe('')
    expect(exchangeBodies).toHaveLength(0)
    expect(loginCalls).toBe(0)
  })
})

// F5: each redemption call aborts after 15 s and takes the failure arm.
describe('a hung redemption times out', () => {
  const TIMEOUT_MS = 15_000
  let signals: Record<'exchange' | 'me', (AbortSignal | undefined)[]>

  // A fetch that never settles unless its signal aborts, as a real fetch does.
  function hang(signal?: AbortSignal | null): Promise<never> {
    return new Promise((_, reject) => {
      if (!signal) return
      if (signal.aborted) return reject(signal.reason)
      signal.addEventListener('abort', () => reject(signal.reason), { once: true })
    })
  }

  function stubFetch(exchangeHangs: boolean) {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init?: RequestInit) => {
        if (url === `${GATEWAY}/auth/exchange`) {
          signals.exchange.push(init?.signal ?? undefined)
          return exchangeHangs ? hang(init?.signal) : ok({ access_token: T })()
        }
        if (url === `${GATEWAY}/api/tenancy/v1/me`) {
          signals.me.push(init?.signal ?? undefined)
          return hang(init?.signal)
        }
        return hang(init?.signal)
      }),
    )
  }

  async function tick(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms)
      for (let i = 0; i < 20; i++) await Promise.resolve()
    })
  }

  beforeEach(() => {
    signals = { exchange: [], me: [] }
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    // jsdom's AbortSignal.timeout runs on the window's real timers; route it through the fake ones.
    vi.spyOn(AbortSignal, 'timeout').mockImplementation((ms: number) => {
      const c = new AbortController()
      setTimeout(() => c.abort(new DOMException('The operation timed out.', 'TimeoutError')), ms)
      return c.signal
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  for (const [leg, exchangeHangs] of [
    ['exchange', true],
    ['/me', false],
  ] as const) {
    it(`a hung ${leg} fails once at 15 s, not before`, async () => {
      configure()
      ensureSignInState()
      stubFetch(exchangeHangs)
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      window.history.replaceState(null, '', `/?handoff=${CODE}`)
      const { hrefWrites } = interceptHref()
      await bootApp()
      await tick(0)
      expect(signals.exchange, 'one exchange call').toHaveLength(1)
      expect(signals.me, 'the /me call').toHaveLength(exchangeHangs ? 0 : 1)

      await tick(TIMEOUT_MS - 1)
      expect(hrefWrites, 'no navigation before 15 s').toEqual([])
      expect(warn).not.toHaveBeenCalled()
      expect(screen.getByText('Opening your workspace…')).toBeTruthy()

      await tick(1)
      expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`])
      expect(AbortSignal.timeout).toHaveBeenCalledWith(TIMEOUT_MS)
      expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}&signin=failed$`))
      expect(warn.mock.calls.filter((c) => String(c[0]).includes('hand-off redemption failed'))).toHaveLength(1)
      expect(warn).toHaveBeenCalledTimes(1)
      expect(localStorage.getItem(SESSION_KEY)).toBeNull()

      await tick(TIMEOUT_MS)
      expect(hrefWrites, 'still one navigation').toHaveLength(1)
    })
  }

  it('both redemption calls carry an abort signal', async () => {
    configure()
    ensureSignInState()
    stubFetch(false)
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await tick(0)
    expect(signals.exchange).toHaveLength(1)
    expect(signals.me).toHaveLength(1)
    expect(signals.exchange[0], 'exchange signal').toBeInstanceOf(AbortSignal)
    expect(signals.me[0], '/me signal').toBeInstanceOf(AbortSignal)
  })
})
