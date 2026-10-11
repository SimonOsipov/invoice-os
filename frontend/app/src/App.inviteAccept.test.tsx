// @vitest-environment jsdom
// An invitee's sign-in: the app holds the invite across the bounce, accepts it on hand-off and renews the session.

import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me } from './auth'
import { SESSION_KEY } from './lib/session'
import { ensureSignInState } from './lib/signInState'
import { EMPTY_BUCKET } from './lib/dashboard'
import type { PlatformCtx } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const STATE_KEY = 'invoice-os.signInState'
const INVITE_KEY = 'invoice-os.pendingInvite'
const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
const INVITE = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE'
const ACCEPT_PATH = '/api/tenancy/v1/invitations/accept'
const ACCEPT_URL = `${GATEWAY}${ACCEPT_PATH}`
// Copied from the Go constants in internal/tenancy/accept.go.
const MSG_INVALID = 'this invite is no longer valid' // msgInviteNotValid
const MSG_ALREADY_MEMBER = 'you already belong to a workspace' // msgAlreadyMember
const MSG_OTHER_ADDRESS = 'this invite was sent to a different email address' // msgWrongAddress

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}
const ANSWERS = { workspace_name: ME.tenant.name, display_name: ME.user.display_name, kind: 'firm' }

function jwt(sub: string, exp: number, extra: object = {}): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64({ sub, exp, ...extra })}.sig`
}
const nowSec = () => Math.floor(Date.now() / 1000)
// The invitee's first token: no tenant, a complete registration that must not provision.
const T_FIRST = jwt(ME.user.id, nowSec() + 3600, { user_metadata: { registration: ANSWERS } })
const T_RENEWED = jwt(ME.user.id, nowSec() + 7200, { iat: nowSec(), app_metadata: { tenant_id: ME.tenant.id } })

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
  const replaceWrites: string[] = []
  const real = window.location
  // Plain target: jsdom's location.replace is non-configurable, so a get trap on the real location cannot override it.
  const proxy = new Proxy({} as Location, {
    set(_t, prop, value) {
      if (prop === 'href') {
        hrefWrites.push(value)
        return true
      }
      return Reflect.set(real, prop, value)
    },
    get(_t, prop) {
      if (prop === 'replace') return (url: string) => void replaceWrites.push(url)
      const v = (real as unknown as Record<PropertyKey, unknown>)[prop]
      return typeof v === 'function' ? v.bind(real) : v
    },
  })
  Object.defineProperty(window, 'location', { configurable: true, value: proxy })
  return { hrefWrites, replaceWrites }
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

// Written raw so the app tests do not depend on holdPendingInvite.
function holdRaw(token: string = INVITE) {
  sessionStorage.setItem(INVITE_KEY, JSON.stringify({ v: 1, t: token, at: Date.now() }))
}

type Reply = () => Promise<unknown>
const ok = (body: unknown): Reply => () => Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body) })
const fail = (status: number, error: string): Reply => () =>
  Promise.resolve({ ok: false, status, statusText: String(status), json: () => Promise.resolve({ error }) })
const networkDown: Reply = () => Promise.reject(new TypeError('Failed to fetch'))

type Call = { url: string; method: string; auth: string | null; body: unknown }
let calls: Call[] = []
let acceptReply: Reply = ok({ tenant: ME.tenant, user: { id: ME.user.id, role: 'admin' } })
let refreshReply: Reply = ok({ access_token: T_RENEWED, refresh_token: 'R1' })
let renewedMeReply: Reply = ok(ME)

const path = (c: Call) => c.url.replace(GATEWAY, '')
const hits = (p: string) => calls.filter((c) => path(c) === p)

function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; headers?: Headers; body?: string }) => {
      const auth = init?.headers?.get('Authorization') ?? null
      calls.push({ url, method: init?.method ?? 'GET', auth, body: init?.body === undefined ? undefined : JSON.parse(init.body) })
      if (url === `${GATEWAY}/auth/exchange`) return ok({ access_token: T_FIRST, refresh_token: 'R0' })()
      if (url === ACCEPT_URL) return acceptReply()
      if (url === `${GATEWAY}/auth/refresh`) return refreshReply()
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return (auth === `Bearer ${T_FIRST}` ? fail(403, 'forbidden') : renewedMeReply)()
      if (url === `${GATEWAY}/api/tenancy/v1/workspaces`) return ok({ tenant: ME.tenant })()
      if (url === `${GATEWAY}/auth/login`) return ok({ access_token: jwt(APP_PERSONAS.firm.subject, nowSec() + 3600) })()
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
        pagination: { limit: 200, offset: 0, total: 0 },
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

async function bootApp() {
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(<App />)
  })
}

function configure({ landing = true } = {}) {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubEnv('VITE_LANDING_URL', landing ? LANDING : '')
}

let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
  capturedCtx = undefined
  calls = []
  acceptReply = ok({ tenant: ME.tenant, user: { id: ME.user.id, role: 'admin' } })
  refreshReply = ok({ access_token: T_RENEWED, refresh_token: 'R1' })
  renewedMeReply = ok(ME)
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

describe('?auth=start holds the invite across the bounce (D11)', () => {
  it('app_authStartHoldsTheInviteAndBounces', async () => {
    configure()
    window.history.replaceState(null, '', `/?auth=start#invite=${INVITE}`)
    const { hrefWrites, replaceWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toHaveLength(1))
    expect(JSON.parse(sessionStorage.getItem(INVITE_KEY) ?? 'null')).toEqual({ v: 1, t: INVITE, at: expect.any(Number) })
    expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=ready`])
    expect(replaceWrites).toEqual([])
    expect(storedState(), 'the bounce carries a stored state').toEqual(expect.stringMatching(/^[A-Za-z0-9_-]{43}$/))
    expect(window.location.hash, 'the address bar holds no hash').toBe('')
    expect(window.location.search).toBe('')
  })

  it('app_authStartWithoutInviteClearsAHeldOne', async () => {
    configure()
    holdRaw()
    expect(sessionStorage.getItem(INVITE_KEY), 'control: an invite is held').not.toBeNull()
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(replaceWrites).toHaveLength(1))
    expect(replaceWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=ready`])
    expect(hrefWrites).toEqual([])
    expect(sessionStorage.getItem(INVITE_KEY)).toBeNull()
  })
})

describe('a hand-off with a held invite accepts it and renews the session (D11, D12)', () => {
  it('app_heldInviteRedeemsAcceptsRefreshesAndMounts', async () => {
    configure()
    const S = ensureSignInState()
    holdRaw()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(hits('/api/tenancy/v1/workspaces'), 'an invitee never provisions').toEqual([])
    expect(calls.map((c) => `${c.method} ${path(c)}`).slice(0, 4)).toEqual([
      'POST /auth/exchange',
      `POST ${ACCEPT_PATH}`,
      'POST /auth/refresh',
      'GET /api/tenancy/v1/me',
    ])
    expect(calls[0].body).toEqual({ code: CODE, state: S })
    expect(calls[1].body).toEqual({ token: INVITE })
    expect(calls[1].auth).toBe(`Bearer ${T_FIRST}`)
    expect(calls[3].auth).toBe(`Bearer ${T_RENEWED}`)
    expect(JSON.parse(localStorage.getItem(SESSION_KEY) ?? 'null')).toMatchObject({ token: T_RENEWED, handoff: true, refresh_token: 'R1' })
    expect(sessionStorage.getItem(INVITE_KEY), 'the invite is one-shot').toBeNull()
    expect(hrefWrites).toEqual([])
  })

  it('app_heldInviteIsConsumedEvenWithoutAState', async () => {
    configure()
    holdRaw()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(hits('/auth/exchange'), 'a tab without a state never redeems').toEqual([])
    expect(sessionStorage.getItem(INVITE_KEY)).toBeNull()
  })

  it.each([
    ['404', 404, MSG_INVALID, 'invalid'],
    ['409', 409, MSG_ALREADY_MEMBER, 'already-member'],
    ['403', 403, MSG_OTHER_ADDRESS, 'other-address'],
  ] as const)('app_refusedInviteLandsOnTheLandingNotice: accept %s', async (_status, status, error, outcome) => {
    configure()
    ensureSignInState()
    holdRaw()
    acceptReply = fail(status, error)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?invite=${outcome}`]))
    await settle()
    expect(hrefWrites, 'no second navigation').toEqual([`${LANDING}/?invite=${outcome}`])
    expect(hits(ACCEPT_PATH)).toHaveLength(1)
    expect(hits('/api/tenancy/v1/me'), 'no /me').toEqual([])
    expect(hits('/auth/refresh'), 'no refresh').toEqual([])
    expect(hits('/api/tenancy/v1/workspaces'), 'no provisioning').toEqual([])
    expect(localStorage.getItem(SESSION_KEY), 'no session is stored').toBeNull()
    expect(capturedCtx, 'no workspace mounts').toBeUndefined()
  })

  it.each([
    ['accept 500', () => (acceptReply = fail(500, 'internal error'))],
    ['accept network error', () => (acceptReply = networkDown)],
    ['accept 200 then refresh 401', () => (refreshReply = fail(401, 'invalid refresh token'))],
    // D35: only tenancy's own three refusals are notices; a gateway 403 is not "no workspace".
    ['accept 403 forbidden', () => (acceptReply = fail(403, 'forbidden'))],
    ['accept 404 another message', () => (acceptReply = fail(404, 'not found'))],
    ['accept 409 another message', () => (acceptReply = fail(409, 'already has a workspace'))],
    ['accept 200 then /me 403', () => (renewedMeReply = fail(403, 'forbidden'))],
  ] as const)('app_acceptFailureTakesTheFailedPath: %s', async (_name, arrange) => {
    configure()
    ensureSignInState()
    holdRaw()
    arrange()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(hits(ACCEPT_PATH), 'control: the accept was tried').toHaveLength(1)
    expect(localStorage.getItem(SESSION_KEY), 'no session is stored').toBeNull()
    expect(capturedCtx, 'no workspace mounts').toBeUndefined()
  })
})

describe('a held invite redeems a hand-off over a live stored session', () => {
  const OLD_ME: Me = {
    tenant: { id: '44444444-4444-4444-4444-444444444444', name: 'Old Workspace', kind: 'firm' },
    user: { id: 'd0000000-0000-0000-0000-000000000001', role: 'authenticated', display_name: 'Account A', email: 'a@example.com' },
  }
  const OLD_RECORD = JSON.stringify({ v: 1, personaId: 'firm', token: jwt(OLD_ME.user.id, nowSec() + 3600), me: OLD_ME, verified: true, handoff: true })

  it('app_heldInviteBeatsALiveSession', async () => {
    configure()
    ensureSignInState()
    holdRaw()
    localStorage.setItem(SESSION_KEY, OLD_RECORD)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the invitee workspace must mount').toBeDefined())
    expect(hits(ACCEPT_PATH)).toHaveLength(1)
    expect(JSON.parse(localStorage.getItem(SESSION_KEY) ?? 'null')).toMatchObject({ token: T_RENEWED, handoff: true })
    expect(sessionStorage.getItem(INVITE_KEY), 'the invite is one-shot').toBeNull()
  })

  it('app_refusedInviteKeepsTheOldStoredSession', async () => {
    configure()
    ensureSignInState()
    holdRaw()
    acceptReply = fail(403, MSG_OTHER_ADDRESS)
    localStorage.setItem(SESSION_KEY, OLD_RECORD)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?invite=other-address`]))
    await settle()
    expect(localStorage.getItem(SESSION_KEY), 'the old session stays as it was').toBe(OLD_RECORD)
    expect(capturedCtx, 'no workspace mounts').toBeUndefined()
  })

  it('app_expiredHeldInviteLeavesTheLiveSessionWinning', async () => {
    configure()
    ensureSignInState()
    sessionStorage.setItem(INVITE_KEY, JSON.stringify({ v: 1, t: INVITE, at: Date.now() - 24 * 3600 * 1000 }))
    localStorage.setItem(SESSION_KEY, OLD_RECORD)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    await settle()
    expect(hits('/auth/exchange')).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBe(OLD_RECORD)
  })

  it.each([
    ['not json', 'not json'],
    ['a wrong version', JSON.stringify({ v: 2, t: INVITE, at: Date.now() })],
    ['a 42-character token', JSON.stringify({ v: 1, t: INVITE.slice(1), at: Date.now() })],
    ['a future at', JSON.stringify({ v: 1, t: INVITE, at: Date.now() + 3600 * 1000 })],
  ] as const)('app_unusableHeldInviteLeavesTheLiveSessionWinning: %s', async (_name, blob) => {
    configure()
    ensureSignInState()
    sessionStorage.setItem(INVITE_KEY, blob)
    localStorage.setItem(SESSION_KEY, OLD_RECORD)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the old workspace must mount').toBeDefined())
    await settle()
    expect(hits('/auth/exchange'), 'the code is never redeemed').toEqual([])
    expect(hits(ACCEPT_PATH)).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBe(OLD_RECORD)
  })

  it.each([
    ['accept 500', () => (acceptReply = fail(500, 'internal error'))],
    ['accept network error', () => (acceptReply = networkDown)],
    ['accept 403 forbidden', () => (acceptReply = fail(403, 'forbidden'))],
    ['accept 200 then refresh 401', () => (refreshReply = fail(401, 'invalid refresh token'))],
    ['accept 200 then /me 403', () => (renewedMeReply = fail(403, 'forbidden'))],
  ] as const)('app_failedHeldInviteKeepsTheOldStoredSession: %s', async (_name, arrange) => {
    configure()
    ensureSignInState()
    holdRaw()
    arrange()
    localStorage.setItem(SESSION_KEY, OLD_RECORD)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    await settle()
    expect(hits(ACCEPT_PATH), 'control: the accept was tried').toHaveLength(1)
    expect(localStorage.getItem(SESSION_KEY), 'the old session stays as it was').toBe(OLD_RECORD)
    expect(capturedCtx, 'no workspace mounts').toBeUndefined()
  })

  it('app_heldInviteWithoutAStateKeepsTheOldStoredSession', async () => {
    configure()
    holdRaw()
    localStorage.setItem(SESSION_KEY, OLD_RECORD)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    await settle()
    expect(hits('/auth/exchange'), 'a tab without a state never redeems').toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBe(OLD_RECORD)
  })

  it('app_refusedInviteWithoutALandingBaseKeepsTheOldStoredSession', async () => {
    configure({ landing: false })
    ensureSignInState()
    holdRaw()
    acceptReply = fail(409, MSG_ALREADY_MEMBER)
    localStorage.setItem(SESSION_KEY, OLD_RECORD)
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hits(ACCEPT_PATH), 'control: the accept was tried').toHaveLength(1))
    await waitFor(() => expect(screen.getByText('Choose an account')).toBeTruthy())
    await settle()
    expect(hrefWrites, 'no navigation').toEqual([])
    expect(localStorage.getItem(SESSION_KEY), 'the old session stays as it was').toBe(OLD_RECORD)
    expect(capturedCtx, 'no workspace mounts').toBeUndefined()
  })
})

describe('refusals without a landing base (D35)', () => {
  it('app_refusedInviteWithoutALandingBaseStaysOnThePicker', async () => {
    configure({ landing: false })
    ensureSignInState()
    holdRaw()
    acceptReply = fail(409, MSG_ALREADY_MEMBER)
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hits(ACCEPT_PATH), 'control: the accept was tried').toHaveLength(1))
    await waitFor(() => expect(screen.getByText('Choose an account')).toBeTruthy())
    await settle()
    expect(hrefWrites, 'no navigation').toEqual([])
    expect(window.location.href).not.toContain('null')
    expect(hits('/api/tenancy/v1/me'), 'no /me').toEqual([])
    expect(hits('/auth/refresh'), 'no refresh').toEqual([])
    expect(localStorage.getItem(SESSION_KEY), 'no session is stored').toBeNull()
    expect(capturedCtx, 'no workspace mounts').toBeUndefined()
  })
})
