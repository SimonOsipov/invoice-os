// @vitest-environment jsdom
// Sign-out revokes before it leaves; a 401 ends a revoked session without revoking.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Me } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { captureDestination, readDestination } from './lib/deepLink'
import { NOT_ACTIVE_MEMBER_MESSAGE } from './lib/authedFetch'
import type { ImportAuth } from './lib/importApi'
import { SESSION_KEY } from './lib/session'
import type { PlatformCtx } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const SIGN_OUT = `${GATEWAY}/auth/sign-out`
// Answers 401 as the edge does to a revoked session (internal/gateway/session_check.go).
const REVOKED = `${GATEWAY}/api/revoked`
const HANDOFF_EXIT = [LANDING, expect.stringMatching(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}$`))]
const D8_WARNING = '[session] sign-out could not reach the server; other sessions stay signed in'

const MIN = 60_000
const nowSec = (ms: number) => Math.floor(ms / 1000)

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}
const OTHER_ME: Me = {
  tenant: { id: '44444444-4444-4444-4444-444444444444', name: 'Earlier Holdings', kind: 'firm' },
  user: { id: 'e0000000-0000-0000-0000-000000000004', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}

const b64url = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
// `sid` null: a mock-issuer token, which carries no session_id.
function jwt(me: Me, sid: string | null, mark: string): string {
  const iat = nowSec(Date.now() - MIN)
  const claims = { sub: me.user.id, iat, exp: iat + 3600, app_metadata: { tenant_id: me.tenant.id }, mark, ...(sid ? { session_id: sid } : {}) }
  return `${b64url('{"alg":"RS256"}')}.${b64url(JSON.stringify(claims))}.sig`
}

// Received a minute ago, so no renewal is due during a test.
function handoffRecord(token: string, refresh: string, me: Me = ME): string {
  return JSON.stringify({ v: 1, personaId: 'firm', token, me, verified: true, handoff: true, refresh_token: refresh, received_at: Date.now() - MIN })
}
function personaRecord(token: string | null): string {
  return JSON.stringify({ v: 1, personaId: 'firm', token, me: ME, verified: true })
}

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./components/Sidebar')>()
  return {
    Sidebar: (p: { ctx: PlatformCtx }) => {
      capturedCtx = p.ctx
      return <actual.Sidebar {...p} />
    },
  }
})

// The upload transport's auth, as App built it; its onUnauthorized is what an upload 401 fires.
let capturedImportAuth: ImportAuth | undefined
vi.mock('./lib/importApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./lib/importApi')>()
  return {
    ...actual,
    makeImportAuth: (...args: Parameters<typeof actual.makeImportAuth>) => {
      capturedImportAuth = actual.makeImportAuth(...args)
      return capturedImportAuth
    },
  }
})

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
const REVOKED_OK: () => Promise<Response> = () => Promise.resolve(new Response(null, { status: 204 }))

interface Call {
  url: string
  method: string
  auth: string | null
  body: unknown
}

let calls: Call[] = []
let signOutReply: () => Promise<unknown> = REVOKED_OK
let meReply: Reply = answer(200, ME)
// Non-null: every tenant call answers this instead of an empty page.
let apiOverride: Reply | null = null
// Non-null: the entities list answers this.
let entitiesReply: Reply | null = null

// The sign-out answer waits until the test releases it.
function deferSignOut(): { release: (r: () => Promise<unknown>) => Promise<void> } {
  let release!: (r: () => Promise<unknown>) => void
  const gate = new Promise<() => Promise<unknown>>((r) => {
    release = r
  })
  signOutReply = () => gate.then((r) => r())
  return {
    release: async (r) => {
      await act(async () => release(r))
    },
  }
}

function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; headers?: Headers; body?: string }) => {
      calls.push({
        url,
        method: init?.method ?? 'GET',
        auth: init?.headers?.get('Authorization') ?? null,
        body: init?.body === undefined ? undefined : JSON.parse(init.body),
      })
      if (url === SIGN_OUT) return signOutReply()
      if (url === `${GATEWAY}/auth/login`) return answer(200, { access_token: jwt(OTHER_ME, null, 'P') })()
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return meReply()
      if (url.startsWith(REVOKED)) return answer(401, { error: 'unauthorized' })()
      if (entitiesReply && url.startsWith(`${GATEWAY}/api/portfolio/v1/entities`)) return entitiesReply()
      if (apiOverride && url.startsWith(`${GATEWAY}/api/`)) return apiOverride()
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

const signOutPosts = () => calls.filter((c) => c.url === SIGN_OUT)
const sessionWrites = () =>
  [...vi.mocked(localStorage.setItem).mock.calls, ...vi.mocked(localStorage.removeItem).mock.calls].filter(([k]) => k === SESSION_KEY)
const d8Warnings = (warn: { mock: { calls: unknown[][] } }) => warn.mock.calls.filter((c) => String(c[0]).startsWith('[session] sign-out'))

async function settle(ms = 30) {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms))
  })
}

// Stores `raw`, boots on `path` and waits for the workspace.
async function mount(raw: string | null, path = '/') {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubEnv('VITE_LANDING_URL', LANDING)
  if (raw !== null) localStorage.setItem(SESSION_KEY, raw)
  window.history.replaceState(null, '', path)
  const nav = interceptHref()
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(<App />)
  })
  await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
  await settle()
  expect(nav.hrefWrites, 'the boot navigates nowhere').toEqual([])
  return nav
}

const A_SID1 = jwt(ME, 'sid1', 'A0')

async function clickSignOut(button = screen.getByRole('button', { name: 'Sign out' })) {
  await act(async () => {
    fireEvent.click(button)
  })
}

// A tenant call the edge refuses; resolves to what the caller saw.
async function hitRevoked(): Promise<unknown> {
  let out: unknown
  await act(async () => {
    out = await capturedCtx!.authedFetch(REVOKED).then(
      () => 'resolved',
      (e: unknown) => e,
    )
  })
  return out
}
// By shape: App's ApiError comes from the module graph vi.resetModules() rebuilt.
const is401 = (e: unknown) => e instanceof Error && e.name === 'ApiError' && (e as { status?: unknown }).status === 401

let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
  capturedCtx = undefined
  capturedImportAuth = undefined
  calls = []
  signOutReply = REVOKED_OK
  meReply = answer(200, ME)
  apiOverride = null
  entitiesReply = null
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

describe('Sign out revokes every session, then leaves (AC-1..AC-5)', () => {
  it('sign-out revokes, then leaves', async () => {
    const warn = vi.spyOn(console, 'warn')
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))
    captureDestination('/settings')
    expect(readDestination(), 'control: a destination is stored').not.toBeNull()

    await clickSignOut()
    await settle()

    expect(signOutPosts()).toEqual([{ url: SIGN_OUT, method: 'POST', auth: null, body: { refresh_token: 'R1' } }])
    const fetchOrder = vi.mocked(fetch).mock.invocationCallOrder[calls.indexOf(signOutPosts()[0]!)]!
    const removeItem = vi.mocked(localStorage.removeItem)
    const clearOrder = removeItem.mock.invocationCallOrder[removeItem.mock.calls.findIndex(([k]) => k === SESSION_KEY)]
    expect(clearOrder, 'the session record is cleared').toBeDefined()
    expect(fetchOrder, 'the revoke goes out before the record is cleared').toBeLessThan(clearOrder!)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(readDestination()).toBeNull()
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
    expect(d8Warnings(warn), 'a revoked sign-out warns nothing').toEqual([])
  })

  it('the suspended notice signs out the same way', async () => {
    apiOverride = answer(403, { error: NOT_ACTIVE_MEMBER_MESSAGE })
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))
    await waitFor(() => expect(screen.queryByTestId('suspended-notice'), 'a suspended answer shows the notice').not.toBeNull())

    await clickSignOut(screen.getByTestId('suspended-notice').querySelector('button')!)
    await settle()

    expect(signOutPosts()).toEqual([{ url: SIGN_OUT, method: 'POST', auth: null, body: { refresh_token: 'R1' } }])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })

  // Regression guard: green at head.
  it('a persona sign-out sends nothing', async () => {
    const stored = JSON.stringify({ v: 1, personaId: 'firm', token: A_SID1, me: ME, verified: true })
    const { hrefWrites } = await mount(stored)

    await clickSignOut()
    await settle()

    expect(signOutPosts()).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })

  it('a newer stored refresh token is used', async () => {
    await mount(handoffRecord(A_SID1, 'R1'))
    // Another tab renewed the same session.
    localStorage.setItem(SESSION_KEY, handoffRecord(jwt(ME, 'sid1', 'A1'), 'R2'))

    await clickSignOut()
    await settle()

    expect(signOutPosts().map((c) => c.body)).toEqual([{ refresh_token: 'R2' }])
  })

  const notThisAccount: [string, (() => void)][] = [
    ['another account signed in', () => localStorage.setItem(SESSION_KEY, handoffRecord(jwt(OTHER_ME, 'sid9', 'O0'), 'R9', OTHER_ME))],
    ['no stored record', () => localStorage.removeItem(SESSION_KEY)],
  ]

  it.each(notThisAccount)("the seat's own token is sent: %s", async (_name, rewrite) => {
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))
    rewrite()

    await clickSignOut()
    await settle()

    expect(signOutPosts().map((c) => c.body)).toEqual([{ refresh_token: 'R1' }])
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })
})

describe('a failed revoke still signs out (AC-6)', () => {
  // 502 body copied from internal/gateway/signout.go SignOutHandler.
  const failures: [string, () => Promise<unknown>][] = [
    ['502', answer(502, { error: 'sign-out is unavailable' })],
    ['a network error', () => Promise.reject(new TypeError('Failed to fetch'))],
    ['an aborted request', () => Promise.reject(new DOMException('The operation was aborted.', 'AbortError'))],
  ]

  it.each(failures)('a failed revoke still signs out: %s', async (_name, reply) => {
    signOutReply = reply
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))

    await clickSignOut()
    await settle()

    expect(signOutPosts(), 'the revoke was attempted').toHaveLength(1)
    expect(timeout).toHaveBeenCalledWith(5000)
    expect(d8Warnings(warn)).toEqual([[D8_WARNING]])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })
})

describe('one sign-out at a time (AC-7, AC-8)', () => {
  it('a double click revokes once', async () => {
    const pending = deferSignOut()
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))
    const button = screen.getByRole('button', { name: 'Sign out' })

    await clickSignOut(button)
    await clickSignOut(button)

    expect(signOutPosts(), 'one revoke for two clicks').toHaveLength(1)
    expect(hrefWrites, 'nothing leaves before the revoke answers').toEqual([])

    await pending.release(REVOKED_OK)
    await settle()

    expect(signOutPosts()).toHaveLength(1)
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })

  it('a request during sign-out sends nothing', async () => {
    const pending = deferSignOut()
    const raw = handoffRecord(A_SID1, 'R1')
    const { hrefWrites } = await mount(raw)
    vi.mocked(localStorage.setItem).mockClear()
    vi.mocked(localStorage.removeItem).mockClear()

    const sentAtClick = calls.length
    expect(sentAtClick, 'control: the boot sent requests').toBeGreaterThan(0)
    await clickSignOut()
    let seen: unknown
    await act(async () => {
      seen = await capturedCtx!.authedFetch(REVOKED).then(
        () => 'resolved',
        (e: unknown) => e,
      )
    })
    let tokenSeen: unknown
    await act(async () => {
      tokenSeen = await Promise.resolve(capturedCtx!.getToken()).then(
        () => 'resolved',
        (e: unknown) => e,
      )
    })
    await settle()

    expect((seen as Error).name, 'authedFetch rejects').toBe('SessionEndedError')
    expect((tokenSeen as Error).name, 'getToken rejects').toBe('SessionEndedError')
    expect(
      calls.slice(sentAtClick).filter((c) => c.url.includes('/api/')),
      'no API request leaves after the click',
    ).toEqual([])
    expect(hrefWrites).toEqual([])
    expect(sessionWrites()).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBe(raw)

    await pending.release(REVOKED_OK)
    await settle()

    expect(signOutPosts()).toHaveLength(1)
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('a dashboard mounted during the revoke sends nothing', async () => {
    const rejections: unknown[] = []
    const onRejection = (e: unknown) => rejections.push(e)
    process.on('unhandledRejection', onRejection)
    try {
      let releaseEntities!: () => void
      const gate = new Promise<void>((r) => {
        releaseEntities = r
      })
      const entity = { id: 'e1', name: 'Acme', tin: null, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
      entitiesReply = () =>
        gate.then(answer(200, { entities: [entity], pagination: { limit: 200, offset: 0, total: 1 } }))
      const pending = deferSignOut()
      const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))

      expect(screen.queryByText('COMPLIANCE OVERVIEW'), 'control: no dashboard before the entities land').toBeNull()
      await clickSignOut()
      const sent = calls.length
      await act(async () => releaseEntities())
      await waitFor(() => expect(screen.queryByText('COMPLIANCE OVERVIEW'), 'control: the dashboard mounted after the click').not.toBeNull())
      await settle(100)

      expect(
        calls.slice(sent).filter((c) => c.url.includes('/api/')),
        'the dashboard sends nothing after the click',
      ).toEqual([])
      expect(rejections, 'no unhandled rejection').toEqual([])

      await pending.release(REVOKED_OK)
      await settle()
      expect(hrefWrites).toEqual(HANDOFF_EXIT)
    } finally {
      process.off('unhandledRejection', onRejection)
    }
  })

  it('a 401 callback during sign-out is ignored', async () => {
    const pending = deferSignOut()
    const raw = handoffRecord(A_SID1, 'R1')
    const { hrefWrites } = await mount(raw)
    vi.mocked(localStorage.setItem).mockClear()
    vi.mocked(localStorage.removeItem).mockClear()

    await clickSignOut()
    await act(async () => capturedImportAuth!.onUnauthorized())
    await settle()

    expect(hrefWrites, 'the 401 navigates nowhere while the revoke is in flight').toEqual([])
    expect(sessionWrites(), 'the 401 writes no storage').toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBe(raw)

    await pending.release(REVOKED_OK)
    await settle()

    expect(signOutPosts()).toHaveLength(1)
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('a real 401 on a request sent before the click is ignored during the revoke', async () => {
    const pending = deferSignOut()
    const raw = handoffRecord(A_SID1, 'R1')
    const { hrefWrites } = await mount(raw)
    let answerLate!: () => void
    const gate = new Promise<void>((r) => {
      answerLate = r
    })
    apiOverride = () => gate.then(answer(401, { error: 'unauthorized' }))
    const LATE = `${GATEWAY}/api/late`
    const late = capturedCtx!.authedFetch(LATE).then(
      () => 'resolved',
      (e: unknown) => e,
    )
    await waitFor(() => expect(calls.map((c) => c.url), 'control: the request left before the click').toContain(LATE))
    await clickSignOut()
    vi.mocked(localStorage.setItem).mockClear()
    vi.mocked(localStorage.removeItem).mockClear()

    await act(async () => answerLate())
    expect(is401(await late), 'control: the 401 reached the app').toBe(true)
    await settle()

    expect(hrefWrites, 'the 401 navigates nowhere while the revoke is in flight').toEqual([])
    expect(sessionWrites(), 'the 401 writes no storage').toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBe(raw)

    await pending.release(REVOKED_OK)
    await settle()

    expect(signOutPosts()).toHaveLength(1)
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  // The page is still unloading when a request sent before the click answers.
  it('a 401 or a sign-out after the sign-out changes nothing', async () => {
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))
    let answerLate!: () => void
    const gate = new Promise<void>((r) => {
      answerLate = r
    })
    apiOverride = () => gate.then(answer(401, { error: 'unauthorized' }))
    const LATE = `${GATEWAY}/api/late`
    const late = capturedCtx!.authedFetch(LATE).then(
      () => 'resolved',
      (e: unknown) => e,
    )
    await waitFor(() => expect(calls.map((c) => c.url), 'control: the request left before the click').toContain(LATE))
    const staleSignOut = capturedCtx!.signOut

    await clickSignOut()
    await settle()
    expect(hrefWrites, 'control: the sign-out completed').toEqual(HANDOFF_EXIT)
    vi.mocked(localStorage.setItem).mockClear()
    vi.mocked(localStorage.removeItem).mockClear()

    let seen: unknown
    await act(async () => {
      answerLate()
      seen = await late
    })
    await settle()
    expect(is401(seen), 'control: the late 401 reached the app').toBe(true)
    await act(async () => {
      await staleSignOut()
    })
    await settle()

    expect(hrefWrites).toEqual(HANDOFF_EXIT)
    expect(sessionWrites()).toEqual([])
    expect(signOutPosts()).toHaveLength(1)
  })
})

describe('a 401 ends a revoked session (AC-9, AC-10)', () => {
  // Regression guard for the no-POST half.
  it('a 401 ends the session without revoking', async () => {
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))
    captureDestination('/settings')
    expect(readDestination(), 'control: a destination is stored').not.toBeNull()

    const seen = await hitRevoked()
    await settle()

    expect(is401(seen)).toBe(true)
    expect(signOutPosts()).toEqual([])
    expect(readDestination()).toBeNull()
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })

  it('a 401 on an upload ends the session without revoking', async () => {
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))
    captureDestination('/settings')
    expect(readDestination(), 'control: a destination is stored').not.toBeNull()
    expect(capturedImportAuth?.onUnauthorized, 'control: App built the upload transport').toBeTypeOf('function')

    await act(async () => {
      capturedImportAuth!.onUnauthorized()
    })
    await settle()

    expect(signOutPosts()).toEqual([])
    expect(readDestination()).toBeNull()
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })

  // A second bare-landing navigation aborts the first (e2e/topology/auth.spec.ts).
  it('a burst of 401s leaves for landing once', async () => {
    const { hrefWrites } = await mount(handoffRecord(A_SID1, 'R1'))

    let seen: unknown[] = []
    await act(async () => {
      seen = await Promise.all(
        [1, 2, 3].map((i) =>
          capturedCtx!.authedFetch(`${REVOKED}/${i}`).then(
            () => 'resolved',
            (e: unknown) => e,
          ),
        ),
      )
    })
    await settle()

    expect(seen.every(is401), 'control: every 401 reached the app').toBe(true)
    expect(signOutPosts()).toEqual([])
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })

  const rows: { name: string; seat: string; stored: string; kept: boolean }[] = [
    { name: 'another sign-in (sid2)', seat: handoffRecord(A_SID1, 'R1'), stored: handoffRecord(jwt(ME, 'sid2', 'B0'), 'R9'), kept: true },
    { name: 'the same session (sid1)', seat: handoffRecord(A_SID1, 'R1'), stored: handoffRecord(jwt(ME, 'sid1', 'A1'), 'R2'), kept: false },
    { name: 'a stored mock JWT with no session_id', seat: handoffRecord(A_SID1, 'R1'), stored: personaRecord(jwt(ME, null, 'P')), kept: false },
    { name: 'a stored null token', seat: handoffRecord(A_SID1, 'R1'), stored: personaRecord(null), kept: false },
    { name: 'an ended mock JWT with no session_id', seat: personaRecord(jwt(ME, null, 'P')), stored: handoffRecord(jwt(ME, 'sid2', 'B0'), 'R9'), kept: false },
    { name: 'an ended null token', seat: personaRecord(null), stored: handoffRecord(jwt(ME, 'sid2', 'B0'), 'R9'), kept: false },
    { name: 'an unparseable stored record', seat: handoffRecord(A_SID1, 'R1'), stored: '{"v":1,', kept: false },
  ]

  it.each(rows)('a 401 keeps only another sign-in\'s record: $name', async ({ seat, stored, kept }) => {
    const { hrefWrites } = await mount(seat)
    localStorage.setItem(SESSION_KEY, stored)

    const seen = await hitRevoked()
    await settle()

    expect(is401(seen), 'control: the 401 reached the app').toBe(true)
    if (kept) expect(localStorage.getItem(SESSION_KEY), 'another sign-in keeps its record byte-identical').toBe(stored)
    else expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(signOutPosts()).toEqual([])
    expect(hrefWrites).toEqual(HANDOFF_EXIT)
  })
})

// No VITE_LANDING_URL: sign-out does not unload the page, it shows the in-app picker.
describe('a build with no landing page', () => {
  it('a second sign-in can sign out again', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    vi.stubEnv('VITE_LANDING_URL', '')
    localStorage.setItem(SESSION_KEY, personaRecord(jwt(ME, null, 'P')))
    vi.resetModules()
    const { default: App } = await import('./App')
    await act(async () => {
      render(<App />)
    })
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())

    await clickSignOut()
    await settle()
    expect(screen.queryByText('Choose an account'), 'control: the first sign-out shows the picker').not.toBeNull()
    capturedCtx = undefined
    const firm = screen.getAllByRole('button').filter((b) => b.textContent?.includes('Chinedu Okafor'))
    expect(firm, 'the picker offers the firm persona').toHaveLength(1)
    await act(async () => {
      fireEvent.click(firm[0]!)
    })
    await waitFor(() => expect(capturedCtx?.user, 'the second sign-in mounts the workspace').toBeDefined())
    await settle()

    await clickSignOut()
    await settle()

    expect(screen.queryByText('Choose an account'), 'the second Sign out must reach the picker').not.toBeNull()
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })
})
