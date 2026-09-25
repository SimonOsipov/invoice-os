// @vitest-environment jsdom
// AUTH-05-08 Mode A: the app redeems a landing hand-off code (D8, D9, D18, D23, D25).

import { StrictMode } from 'react'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from './auth'
import { captureDestination } from './lib/deepLink'
import { SESSION_KEY, serializeSession } from './lib/session'
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
    interceptHref()
    await bootApp({ strict: true })
    await waitFor(() => expect(exchangeBodies.length).toBeGreaterThan(0))
    await waitForVerifiedWorkspace()
    expect(exchangeBodies).toHaveLength(1)
  })

  it('a live hand-off session is not replaced by a URL', async () => {
    const OLD_T = jwt(OLD_ME.user.id, nowSec() + 3600)
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
