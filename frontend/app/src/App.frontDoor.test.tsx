// @vitest-environment jsdom
// F-201 unit half: the NAVIGATING arm of the front door (App.tsx:1629-1633, render gate
// :1644) plus the one control (FD-5) proving it isn't an unconditional redirect. The
// STANDALONE arm's own assertions belong to App.suspended.test.tsx; FD-5 does not repeat
// them beyond this file's own discriminator.

import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import { ensureSignInState } from './lib/signInState'
import App from './App'

// landingBase() is a call-time read of import.meta.env.VITE_LANDING_URL (auth.ts:70-73),
// so vi.stubEnv alone flips the arm -- no vi.resetModules()/dynamic import needed here.

let originalLocation: PropertyDescriptor | undefined

// Node v25's native localStorage collides with jsdom's.
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

// One stub serves both jobs: capturing an `href` assignment AND seeding the query string.
// history.replaceState writes to jsdom's real, un-stubbed internal location and never touches it.
function stubLocation(overrides: { search?: string } = {}) {
  Object.defineProperty(window, 'location', {
    configurable: true,
    writable: true,
    value: {
      href: 'http://localhost/',
      pathname: '/',
      hash: '',
      hostname: 'localhost',
      origin: 'http://localhost',
      search: overrides.search ?? '',
    },
  })
}

// Same two-signal check App.suspended.test.tsx's workspaceIsRendered() uses -- one alone
// could survive a half-done replacement.
function workspaceIsRendered(): boolean {
  return screen.queryByTestId('env-banner') !== null || document.querySelector('.pf-shell') !== null
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }

function storedSignInState(): string | null {
  const raw = sessionStorage.getItem('invoice-os.signInState')
  if (raw == null) return null
  try {
    const s = JSON.parse(raw)?.s
    return typeof s === 'string' ? s : null
  } catch {
    return null
  }
}

const GATEWAY = 'https://gw.test'
const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: null },
}

// Records every href write; `search` seeds the boot query.
function stubRecordingLocation(search: string, pathname = '/') {
  const hrefWrites: string[] = []
  Object.defineProperty(window, 'location', {
    configurable: true,
    writable: true,
    value: {
      get href() {
        return hrefWrites.length ? hrefWrites[hrefWrites.length - 1] : 'http://localhost/'
      },
      set href(v: string) {
        hrefWrites.push(v)
      },
      replace(v: string) {
        hrefWrites.push(v)
      },
      pathname,
      hash: '',
      hostname: 'localhost',
      origin: 'http://localhost',
      search,
    },
  })
  return { hrefWrites }
}

// Returns the list every fetched URL is pushed onto.
function stubGatewayFetch(): string[] {
  const urls: string[] = []
  const reply = (body: unknown) =>
    Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body) })
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      urls.push(url)
      if (url === `${GATEWAY}/auth/login`) return reply({ access_token: 'a.b.c' })
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return reply(ME)
      return reply({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0, clients: [] })
    }),
  )
  return urls
}

async function settle(ms = 30) {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms))
  })
}

// Matches 5/5 sibling App test files that mount a live session (FD-4 is the row that
// reaches Workspace); harmless no-op for the other rows.
// The probe shows `ctx.staff` only; the real icon is pinned in Sidebar.test.tsx.
vi.mock('./components/Sidebar', () => ({ Sidebar: ({ ctx }: { ctx: { staff: boolean } }) => (ctx.staff ? <a href="#">Open the Support Console</a> : null) }))

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
})

describe('front door: the redirect arm (F-201)', () => {
  it('vacuity control: the location stub captures a direct assignment', () => {
    // Without this, every "href untouched" assertion below would pass against an
    // untouched jsdom location that swallows the write silently (measured: no throw,
    // no href change, only jsdom's virtual console sees it).
    stubLocation()
    window.location.href = 'https://example.com/probe'
    expect(window.location.href).toBe('https://example.com/probe')
  })

  it('FD-1: a sessionless visit navigates to landing', () => {
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    const s = storedSignInState()
    expect(window.location.href).toBe(`https://landing.example/?state=${s}`)
    expect(s).toEqual(expect.stringMatching(/^[A-Za-z0-9_-]{43}$/))
  })

  it('FD-2: it offers no second sign-in', () => {
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(screen.queryByText('Choose an account')).toBeNull()
  })

  // `?persona=` is unowned: the sweep covers the two openable ids and three that never were.
  it.each(['firm', 'inhouse', 'developer', 'support', 'bogus'])(
    'FD-3: a ?persona= link is not a credential (×5 params) [%s]',
    async (p) => {
      const { hrefWrites } = stubRecordingLocation(`?persona=${p}`)
      vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
      vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
      const fetched = stubGatewayFetch()
      await act(async () => {
        render(<App />)
      })
      await settle()

      expect(hrefWrites, 'the link must leave for the landing sign-in URL once').toHaveLength(1)
      expect(hrefWrites[0]).toBe(`https://landing.example/?state=${storedSignInState()}`)
      expect(storedSignInState()).toEqual(expect.stringMatching(/^[A-Za-z0-9_-]{43}$/))
      expect(fetched.filter((u) => u === `${GATEWAY}/auth/login`), 'no mint').toEqual([])
      expect(localStorage.getItem(SESSION_KEY), 'no session is stored').toBeNull()
    },
  )

  // Control: the same spy and harness see a mint when the in-app picker drives one.
  it('FD-3-neg: the sweep\'s spy sees a mint', async () => {
    const { hrefWrites } = stubRecordingLocation('')
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    const fetched = stubGatewayFetch()
    await act(async () => {
      render(<App />)
    })
    fireEvent.click(screen.getByText(APP_PERSONAS.firm.name))
    await waitFor(() => expect(fetched.filter((u) => u === `${GATEWAY}/auth/login`)).toHaveLength(1))
    await settle()

    expect(hrefWrites).toEqual([])
    expect(localStorage.getItem(SESSION_KEY), 'the picker mint stores a session').not.toBeNull()
  })

  it('FD-4: an existing session suppresses the redirect', () => {
    // Fails if `seat ||` is dropped -- the effect would fire the assignment over
    // a live session and bounce every signed-in reload back to landing.
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(window.location.href).toBe('http://localhost/')
    // Second, independent oracle: a malformed session blob would crash before the effect
    // ever ran, making href read "untouched" for the wrong reason.
    expect(workspaceIsRendered()).toBe(true)
  })

  it('FD-5 control: the standalone arm keeps its own picker', () => {
    // Fails if the guard is dropped entirely (unconditional assignment) -- with
    // VITE_LANDING_URL unset dest is null, so this proves FD-1/FD-2 aren't passing
    // against an app that redirects no matter what.
    stubLocation()
    render(<App />)
    expect(window.location.href).toBe('http://localhost/')
    expect(screen.getByText('Choose an account')).toBeTruthy()
  })

  // Adversarial: landingBase() trims before checking truthiness (auth.ts:71), so a
  // whitespace-only VITE_LANDING_URL is the boundary between the two arms, not just "unset".
  it('FD-6: a landing URL that trims to empty behaves like unset', () => {
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', '   ')
    render(<App />)
    expect(window.location.href).toBe('http://localhost/')
    expect(screen.getByText('Choose an account')).toBeTruthy()
  })
})

const BASE64URL_43 = /^[A-Za-z0-9_-]{43}$/

describe('front door: a Library visit goes to registration (LIB-06)', () => {
  it('frontDoor_viaLibraryGoesToLandingRegistration', async () => {
    const { hrefWrites } = stubRecordingLocation('?via=library', '/invoices')
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    await act(async () => {
      render(<App />)
    })
    await settle()

    const s = storedSignInState()
    expect(s).toEqual(expect.stringMatching(BASE64URL_43))
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}&register`])
  })

  it.each(['?via=other', '?via=LIBRARY', '?via=', '?via=library2'])(
    'frontDoor_otherViaValuesKeepSignIn [%s]',
    async (search) => {
      const { hrefWrites } = stubRecordingLocation(search)
      vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
      await act(async () => {
        render(<App />)
      })
      await settle()

      expect(hrefWrites).toEqual([`https://landing.example/?state=${storedSignInState()}`])
    },
  )

  it('frontDoor_noViaIsUnchanged', async () => {
    const { hrefWrites } = stubRecordingLocation('')
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    await act(async () => {
      render(<App />)
    })
    await settle()

    expect(hrefWrites).toEqual([`https://landing.example/?state=${storedSignInState()}`])
  })

  it('frontDoor_viaLibraryWithoutALandingKeepsThePicker', async () => {
    const { hrefWrites } = stubRecordingLocation('?via=library')
    await act(async () => {
      render(<App />)
    })
    await settle()

    expect(hrefWrites).toEqual([])
    expect(screen.getByText('Choose an account')).toBeTruthy()
  })

  it('frontDoor_aPendingHandoffWinsOverVia', async () => {
    const code = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
    const { hrefWrites } = stubRecordingLocation(`?handoff=${code}&via=library`)
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    ensureSignInState()
    const fetchSpy = vi.fn(() => new Promise(() => {}))
    vi.stubGlobal('fetch', fetchSpy)
    await act(async () => {
      render(<App />)
    })
    await settle()

    expect(fetchSpy, 'the redemption is in flight').toHaveBeenCalled()
    expect(hrefWrites).toEqual([])
  })

  it('frontDoor_aSessionWinsOverVia', async () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    const { hrefWrites } = stubRecordingLocation('?via=library', '/invoices')
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    await act(async () => {
      render(<App />)
    })
    await settle()

    expect(workspaceIsRendered(), 'the signed-in workspace rendered').toBe(true)
    expect(hrefWrites).toEqual([])
  })

  it('frontDoor_authStartWinsOverVia', async () => {
    const { hrefWrites } = stubRecordingLocation('?auth=start&via=library')
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    await act(async () => {
      render(<App />)
    })
    await settle()

    expect(hrefWrites).toEqual([`https://landing.example/?state=${storedSignInState()}&signin=ready`])
  })
})

describe('front door: the workspace ctx staff flag', () => {
  const jwtWith = (appMetadata: unknown) => `h.${btoa(JSON.stringify({ app_metadata: appMetadata })).replace(/=+$/, '')}.s`

  it('workspaceCtx_staffComesFromTheToken', () => {
    vi.stubEnv('VITE_SUPPORT_URL', 'https://sup.x')
    stubLocation()
    localStorage.setItem(SESSION_KEY, serializeSession({ ...SEAT_SESSION, token: jwtWith({ staff: true }) }))
    render(<App />)
    expect(screen.queryByRole('link', { name: 'Open the Support Console' })).not.toBeNull()
    cleanup()
    localStorage.setItem(SESSION_KEY, serializeSession({ ...SEAT_SESSION, token: jwtWith({ staff: 'true' }) }))
    render(<App />)
    expect(workspaceIsRendered()).toBe(true)
    expect(screen.queryByRole('link', { name: 'Open the Support Console' })).toBeNull()
    for (const token of [jwtWith({ staff: false }), jwtWith(undefined), 'tok']) {
      cleanup()
      localStorage.setItem(SESSION_KEY, serializeSession({ ...SEAT_SESSION, token }))
      render(<App />)
      expect(workspaceIsRendered(), token).toBe(true)
      expect(screen.queryByRole('link', { name: 'Open the Support Console' }), token).toBeNull()
    }
  })
})

describe('front door: ?auth=console forwards a code to the console', () => {
  const SUPPORT = 'https://sup.x'
  const STATE = 'S'.repeat(43)
  const CODE = 'c'.repeat(43)
  const QUERY = `?auth=console&console=support&state=${STATE}`
  const JWT_RE = /eyJ|[\w-]+\.[\w-]+\.[\w-]+/
  const jwtWith = (appMetadata: unknown) => {
    const iat = Math.floor(Date.now() / 1000)
    return `h.${btoa(JSON.stringify({ iat, exp: iat + 3600, app_metadata: appMetadata })).replace(/=+$/, '')}.s`
  }
  const STAFF = jwtWith({ staff: true })
  const seatWith = (token: string) =>
    localStorage.setItem(
      SESSION_KEY,
      JSON.stringify({ v: 1, personaId: 'firm', token, me: ME, verified: true, handoff: true, refresh_token: 'R0', received_at: Date.now() }),
    )

  type Answer = { status: number; body: unknown } | 'held' | 'network' | 'badjson'
  function stubHandoff(answer: Answer) {
    const calls: { url: string; init: RequestInit }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: RequestInit) => {
        calls.push({ url, init })
        if (answer === 'held') return new Promise(() => {})
        if (url === `${GATEWAY}/auth/console-handoff` && answer === 'network') return Promise.reject(new TypeError('Failed to fetch'))
        if (url === `${GATEWAY}/auth/console-handoff` && answer === 'badjson') {
          return Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.reject(new SyntaxError('bad json')) })
        }
        if (url !== `${GATEWAY}/auth/console-handoff` || typeof answer === 'string') {
          return Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve({}) })
        }
        return Promise.resolve({
          ok: answer.status < 400,
          status: answer.status,
          statusText: '',
          json: () => Promise.resolve(answer.body),
        })
      }),
    )
    return { handoffCalls: () => calls.filter((c) => c.url === `${GATEWAY}/auth/console-handoff`) }
  }
  const mount = async (strict = false) => {
    await act(async () => {
      render(strict ? <StrictMode><App /></StrictMode> : <App />)
    })
    await settle()
  }
  beforeEach(() => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    vi.stubEnv('VITE_SUPPORT_URL', SUPPORT)
  })

  it('frontDoor_consoleStartForwardsACodeToTheConsole', async () => {
    const { hrefWrites } = stubRecordingLocation(QUERY)
    seatWith(STAFF)
    const net = stubHandoff({ status: 200, body: { code: CODE } })
    await mount(true)

    const calls = net.handoffCalls()
    expect(calls).toHaveLength(1)
    expect(JSON.parse(calls[0].init.body as string)).toEqual({ state: STATE })
    expect(new Headers(calls[0].init.headers).get('Authorization')).toBe(`Bearer ${STAFF}`)
    expect(hrefWrites).toEqual([`${SUPPORT}/?handoff=${CODE}`])
    expect(hrefWrites.filter((u) => JWT_RE.test(u))).toEqual([])
  })

  it('frontDoor_consoleStartIgnoredForACustomer', async () => {
    const { hrefWrites } = stubRecordingLocation(QUERY)
    seatWith(jwtWith({ staff: false }))
    const net = stubHandoff({ status: 200, body: { code: CODE } })
    await mount()
    expect(net.handoffCalls()).toHaveLength(0)
    expect(workspaceIsRendered()).toBe(true)
    expect(hrefWrites).toEqual([])

    cleanup()
    vi.unstubAllEnvs()
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    seatWith(STAFF)
    stubRecordingLocation(QUERY)
    const net2 = stubHandoff({ status: 200, body: { code: CODE } })
    await mount()
    expect(net2.handoffCalls()).toHaveLength(0)
    expect(workspaceIsRendered()).toBe(true)
  })

  it.each([
    ['a 403', { status: 403, body: { error: 'forbidden' } }],
    ['a 200 without a code', { status: 200, body: {} }],
    ['a 401', { status: 401, body: { error: 'unauthorized' } }],
    ['a 500', { status: 500, body: { error: 'boom' } }],
    ['a network error', 'network'],
    ['malformed JSON', 'badjson'],
  ] as const)('frontDoor_consoleStartRefusalRendersTheWorkspace [%s]', async (_n, answer) => {
    const { hrefWrites } = stubRecordingLocation(QUERY)
    seatWith(STAFF)
    const net = stubHandoff(answer)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    await mount(true)

    expect(net.handoffCalls()).toHaveLength(1)
    expect(warn.mock.calls.filter((c) => String(c[0]).includes('console hand-off failed'))).toHaveLength(1)
    expect(hrefWrites).toEqual([])
    expect(workspaceIsRendered()).toBe(true)
    warn.mockRestore()
  })

  it('frontDoor_consoleStartWithoutASeatGoesToLanding', async () => {
    const { hrefWrites } = stubRecordingLocation(QUERY)
    const net = stubHandoff({ status: 200, body: { code: CODE } })
    await mount()
    expect(hrefWrites).toEqual([`https://landing.example/?state=${storedSignInState()}`])
    expect(net.handoffCalls()).toHaveLength(0)
  })

  it('frontDoor_aHandoffCodeWinsOverConsoleStart', async () => {
    const { hrefWrites } = stubRecordingLocation(`?handoff=${CODE}&auth=console&console=support&state=${STATE}`, '/invoices')
    ensureSignInState()
    const net = stubHandoff({ status: 200, body: { code: CODE } })
    const base = globalThis.fetch as unknown as (u: string, i: RequestInit) => Promise<unknown>
    const ok = (body: unknown) => Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body) })
    const seen: string[] = []
    vi.stubGlobal('fetch', (u: string, i: RequestInit) => {
      seen.push(u)
      if (u === `${GATEWAY}/auth/exchange`) return ok({ access_token: STAFF, refresh_token: 'R0' })
      if (u === `${GATEWAY}/api/tenancy/v1/me`) return ok(ME)
      return base(u, i)
    })
    const replaceState = vi.spyOn(window.history, 'replaceState')
    await mount()
    expect(seen).toContain(`${GATEWAY}/auth/exchange`)
    expect(workspaceIsRendered()).toBe(true)
    expect(net.handoffCalls()).toHaveLength(0)
    expect(hrefWrites).toEqual([])
    expect(replaceState).toHaveBeenCalledWith(null, '', '/invoices')
    replaceState.mockRestore()
  })

  it('frontDoor_consoleStartShowsTheLoadingCard', async () => {
    stubRecordingLocation(QUERY)
    seatWith(STAFF)
    const net = stubHandoff('held')
    await mount()
    expect(net.handoffCalls()).toHaveLength(1)
    expect(screen.queryByText('Opening your workspace…')).not.toBeNull()
    expect(workspaceIsRendered()).toBe(false)
    expect(screen.queryByRole('link', { name: 'Open the Support Console' })).toBeNull()
  })
  it('frontDoor_consoleStartSuccessLeavesNoTraceOfTheCode', async () => {
    stubRecordingLocation(QUERY)
    seatWith(STAFF)
    stubHandoff({ status: 200, body: { code: CODE } })
    const replaceState = vi.spyOn(window.history, 'replaceState')
    const pushState = vi.spyOn(window.history, 'pushState')
    const spies = (['log', 'info', 'warn', 'error'] as const).map((m) => vi.spyOn(console, m).mockImplementation(() => {}))
    await mount()
    expect(replaceState.mock.calls.length).toBeGreaterThan(0)
    for (const c of replaceState.mock.calls) expect(String(c[2])).toBe('/')
    expect(pushState).not.toHaveBeenCalled()
    const stored = Object.keys(localStorage).concat(Object.keys(sessionStorage)).map((k) => localStorage.getItem(k) ?? sessionStorage.getItem(k) ?? '')
    expect(stored.length).toBeGreaterThan(0)
    expect(stored.filter((v) => v.includes(CODE) || v.includes(STATE))).toEqual([])
    expect(spies.flatMap((x) => x.mock.calls).filter((c) => JSON.stringify(c).includes(CODE))).toEqual([])
    replaceState.mockRestore()
    pushState.mockRestore()
    spies.forEach((x) => x.mockRestore())
  })

  it('frontDoor_consoleStartSendsTheCodeOnlyToTheConfiguredConsole', async () => {
    const { hrefWrites } = stubRecordingLocation(`${QUERY}&target=https://evil.x&next=https://evil.x&redirect=https://evil.x`)
    seatWith(STAFF)
    const net = stubHandoff({ status: 200, body: { code: CODE } })
    await mount()
    expect(net.handoffCalls()).toHaveLength(1)
    expect(hrefWrites).toEqual([`${SUPPORT}/?handoff=${CODE}`])
    expect(JSON.parse(net.handoffCalls()[0].init.body as string)).toEqual({ state: STATE })
  })

  it.each([
    ['console=ops', `?auth=console&console=ops&state=${STATE}`],
    ['a 42-char state', `?auth=console&console=support&state=${STATE.slice(1)}`],
    ['no state', '?auth=console&console=support'],
    ['an oversized state', `?auth=console&console=support&state=${STATE}${STATE}`],
    ['two states', `${QUERY}&state=${STATE}`],
    ['?auth=start', `?auth=start&console=support&state=${STATE}`],
    ['?auth=verify', `?auth=verify&console=support&state=${STATE}`],
    ['?auth=verify-invite', `?auth=verify-invite&console=support&state=${STATE}`],
  ])('frontDoor_consoleStartIgnoresAMalformedOrOtherDoor [%s]', async (_n, q) => {
    const { hrefWrites } = stubRecordingLocation(q)
    seatWith(STAFF)
    const net = stubHandoff({ status: 200, body: { code: CODE } })
    await mount()
    expect(net.handoffCalls()).toHaveLength(0)
    expect(hrefWrites.filter((u) => u.startsWith(SUPPORT))).toEqual([])
  })

  it('frontDoor_consoleStartDropsTheIntentForANonStaffSeat', async () => {
    stubRecordingLocation(QUERY)
    seatWith(jwtWith({}))
    const net = stubHandoff({ status: 200, body: { code: CODE } })
    await mount()
    expect(net.handoffCalls()).toHaveLength(0)
    expect(workspaceIsRendered()).toBe(true)
    expect(screen.queryByText('Opening your workspace…')).toBeNull()
  })
})
