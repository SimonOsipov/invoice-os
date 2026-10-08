// @vitest-environment jsdom
// The app redeems a landing hand-off code.

import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, expectTypeOf, it, onTestFinished, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from './auth'
import { captureDestination } from './lib/deepLink'
import { SESSION_KEY, serializeSession } from './lib/session'
import { ensureSignInState } from './lib/signInState'
import { holdPendingVerify } from './lib/verifyBounce'
import { EMPTY_BUCKET } from './lib/dashboard'
import { SUGGESTED_RULES } from './lib/rules'
import type { PlatformCtx, SignedInUser } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const STATE_KEY = 'invoice-os.signInState'
const STATE_RE = '[A-Za-z0-9_-]{43}'
const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}
const OLD_ME: Me = {
  tenant: { id: '44444444-4444-4444-4444-444444444444', name: 'Earlier Holdings', kind: 'firm' },
  user: { id: 'e0000000-0000-0000-0000-000000000004', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}

function jwt(sub: string, exp: number, extra: object = {}): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64({ sub, exp, ...extra })}.sig`
}
const nowSec = () => Math.floor(Date.now() / 1000)
const T = jwt(ME.user.id, nowSec() + 3600)
const ANSWERS = { workspace_name: ME.tenant.name, display_name: ME.user.display_name, kind: 'firm' }
// A registered account's first token: no tenant, the answers in user_metadata.
const T_ANSWERS = jwt(ME.user.id, nowSec() + 3600, { user_metadata: { registration: ANSWERS } })
const T2 = jwt(ME.user.id, nowSec() + 7200, { iat: nowSec(), app_metadata: { tenant_id: ME.tenant.id } })

let capturedCtx: PlatformCtx | undefined
let seenUsers: PlatformCtx['user'][] = []
let seenDemoState: { handoff: boolean; connected: number; rules: number }[] = []
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    seenUsers.push(p.ctx.user)
    seenDemoState.push({ handoff: p.ctx.handoff, connected: Object.values(p.ctx.connectors).filter(Boolean).length, rules: p.ctx.customRules.length })
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
let entityRows: unknown[] = []
let meQueue: Reply[] = []
let workspacesReply: Reply = ok({ tenant: ME.tenant })
let refreshReply: Reply = ok({ access_token: T2, refresh_token: 'R1' })
let workspacesCalls: { auth: string | null; body: unknown }[] = []
let refreshBodies: unknown[] = []
let mineReply: Reply | null = null

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
        return (meQueue.shift() ?? meReply)()
      }
      if (mineReply && url === `${GATEWAY}/api/tenancy/v1/invitations/mine`) return mineReply()
      if (url === `${GATEWAY}/api/tenancy/v1/workspaces`) {
        workspacesCalls.push({ auth: init?.headers?.get('Authorization') ?? null, body: JSON.parse(init?.body ?? 'null') })
        return workspacesReply()
      }
      if (url === `${GATEWAY}/auth/refresh`) {
        refreshBodies.push(JSON.parse(init?.body ?? 'null'))
        return refreshReply()
      }
      if (url === `${GATEWAY}/auth/login`) {
        loginCalls++
        return ok({ access_token: jwt(APP_PERSONAS.firm.subject, nowSec() + 3600) })()
      }
      if (url.startsWith(`${GATEWAY}/api/portfolio/v1/entities`)) {
        return ok({ entities: entityRows, pagination: { limit: 200, offset: 0, total: entityRows.length } })()
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
  seenUsers = []
  seenDemoState = []
  fetchUrls = []
  exchangeBodies = []
  meAuth = []
  exchangeAuth = []
  loginCalls = 0
  exchangeReply = ok({ access_token: T })
  meReply = ok(ME)
  entityRows = []
  meQueue = []
  workspacesReply = ok({ tenant: ME.tenant })
  refreshReply = ok({ access_token: T2, refresh_token: 'R1' })
  workspacesCalls = []
  refreshBodies = []
  mineReply = ok({ invitations: [] })
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
    name: ME.user.display_name,
    initials: 'AN',
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

describe('a hand-off session mounts the workspace its tenant kind names (AUTH-08)', () => {
  const withKind = (kind: Me['tenant']['kind']): Me => ({ ...ME, tenant: { ...ME.tenant, kind } })

  it('an in-house hand-off mounts the in-house workspace', async () => {
    configure()
    ensureSignInState()
    meReply = ok(withKind('in_house'))
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.mode).toBe('inhouse')
  })

  it('a firm hand-off mounts the firm workspace', async () => {
    configure()
    ensureSignInState()
    meReply = ok(withKind('firm'))
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.mode).toBe('firm')
  })

  it('a stored in-house hand-off session boots in-house', async () => {
    configure()
    const inHouse = { ...OLD_ME, tenant: { ...OLD_ME.tenant, kind: 'in_house' as const } }
    localStorage.setItem(SESSION_KEY, handoffRecord(jwt(inHouse.user.id, nowSec() + 3600), inHouse))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(exchangeBodies, 'no redemption on a stored session').toHaveLength(0)
    expect(capturedCtx?.mode).toBe('inhouse')
  })
})

describe('the identity card names the person /me names (AUTH-09-02)', () => {
  it('a stored hand-off session boots with its /me name', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(T, ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(exchangeBodies, 'no redemption on a stored session').toHaveLength(0)
    expect(capturedCtx?.user.name).toBe('Adaeze Nwankwo')
    expect(capturedCtx?.user.initials).toBe('AN')
  })

  it('a pre-AUTH-09 stored hand-off record shows no name, not a persona', async () => {
    configure()
    const old = { ...ME, user: { id: ME.user.id, role: ME.user.role } }
    localStorage.setItem(SESSION_KEY, handoffRecord(T, old as unknown as Me))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.user.name).toBe('')
    expect(capturedCtx?.user.initials).toBe('')
    expect(capturedCtx?.user.verified).toBe(true)
  })

  it('no render shows the badge with a name that did not come from /me', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    const badged = seenUsers.filter((u) => u.verified && u.tenantName)
    expect(badged.length, 'a badged render happened').toBeGreaterThan(0)
    expect(badged.filter((u) => u.name !== 'Adaeze Nwankwo')).toEqual([])
  })

  it('a stored hand-off boot never badges a name that did not come from /me', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(T, ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    const badged = seenUsers.filter((u) => u.verified && u.tenantName)
    expect(badged.length, 'a badged render happened').toBeGreaterThan(0)
    expect(badged.filter((u) => u.name !== 'Adaeze Nwankwo')).toEqual([])
  })

  it('a pre-AUTH-09 stored record badges only an empty identity, never a persona name', async () => {
    configure()
    const old = { ...ME, user: { id: ME.user.id, role: ME.user.role } }
    localStorage.setItem(SESSION_KEY, handoffRecord(T, old as unknown as Me))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    const badged = seenUsers.filter((u) => u.verified && u.tenantName)
    expect(badged.length, 'a badged render happened').toBeGreaterThan(0)
    expect(badged.filter((u) => u.name !== '' || u.initials !== '')).toEqual([])
  })

  it('a stored record with non-string identity keys mounts with no name', async () => {
    configure()
    const shapes: Record<string, unknown>[] = [
      { display_name: 7, email: {} },
      { display_name: ['x'], email: 7 },
      { display_name: '   ', email: '\t' },
    ]
    for (const shape of shapes) {
      cleanup()
      capturedCtx = undefined
      seenUsers = []
      const tampered = { ...ME, user: { id: ME.user.id, role: ME.user.role, ...shape } }
      localStorage.setItem(SESSION_KEY, handoffRecord(T, tampered as unknown as Me))
      interceptHref()
      await bootApp()
      await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
      expect((capturedCtx as PlatformCtx | undefined)?.user, JSON.stringify(shape)).toEqual({
        name: '',
        initials: '',
        tenantName: ME.tenant.name,
        verified: true,
      })
    }
  })

  it('the card and the context carry no email field (D5)', async () => {
    expectTypeOf<SignedInUser>().not.toHaveProperty('email')
    expectTypeOf<PlatformCtx>().not.toHaveProperty('email')
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(T, ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(Object.keys(capturedCtx!.user).sort()).toEqual(['initials', 'name', 'tenantName', 'verified'])
    expect(Object.keys(capturedCtx!)).not.toContain('email')
    expect(JSON.stringify(capturedCtx!.user)).not.toContain(ME.user.email)
  })
})

describe('a hand-off session hides the demo data (AUTH-10-06, F17)', () => {
  const NO_CONNECTORS = { sap: false, quickbooks: false, oracle: false, sage: false, odoo: false, dynamics: false }

  it("a hand-off session's workspace carries handoff:true and blank demo state", async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(T, ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.handoff).toBe(true)
    expect(capturedCtx?.connectors).toEqual(NO_CONNECTORS)
    expect(capturedCtx?.customRules).toEqual([])
  })

  // Control: green before and after.
  it('a stored persona session keeps handoff:false and the demo state', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, JSON.stringify({ v: 1, personaId: 'firm', token: T, me: ME, verified: true }))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.handoff).toBe(false)
    expect(capturedCtx?.connectors.sap).toBe(true)
    expect(capturedCtx?.customRules).toHaveLength(5)
  })

  it("a hand-off workspace's draft carries no demo invoice", async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(T, ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.handoff).toBe(true)
    expect(capturedCtx?.draft.number).toBe('')
    expect(capturedCtx?.draft.buyer).toBe('')
  })

  // Control: green before and after.
  it('a stored persona session keeps the demo draft', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, JSON.stringify({ v: 1, personaId: 'firm', token: T, me: ME, verified: true }))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.handoff).toBe(false)
    expect(capturedCtx?.draft.number).toBe('INV-2026-00482')
  })

  describe('the draft reseed paths (AUTH-10-07)', () => {
    const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
    const ENTITY_B = 'bbbbbbbb-0000-4000-8000-000000000002'
    const BLANK = { number: '', buyer: '', buyerTin: '', date: '', currency: 'NGN', items: [{ desc: '', qty: 1, price: 0 }] }
    const personaRecord = () => JSON.stringify({ v: 1, personaId: 'firm', token: T, me: ME, verified: true })

    function entityRow(id: string, name: string, tin: string) {
      return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
    }

    async function bootTwoCompanies(record: string) {
      configure()
      entityRows = [entityRow(ENTITY_A, 'Alpha Ltd', '12345678-0001'), entityRow(ENTITY_B, 'Beta Ltd', '12345678-0002')]
      localStorage.setItem(SESSION_KEY, record)
      interceptHref()
      await bootApp()
      await waitFor(() => expect(capturedCtx?.activeEntity?.id, 'the entity list must resolve').toBe(ENTITY_A))
    }

    async function typeIntoDraft() {
      await act(async () => {
        capturedCtx!.updateDraft('number', 'TYPED-1')
        capturedCtx!.updateDraft('buyer', 'Typed Buyer')
        capturedCtx!.updateDraft('buyerTin', '12345678-0009')
        capturedCtx!.updateDraft('date', '2026-02-03')
        capturedCtx!.updateItemDesc(0, 'typed line')
      })
      expect(capturedCtx!.draft.number, 'control: the typed value landed before the reseed').toBe('TYPED-1')
      expect(capturedCtx!.draft.items[0]?.desc).toBe('typed line')
    }

    it('a hand-off session switching company gets a blank draft', async () => {
      await bootTwoCompanies(handoffRecord(T, ME))
      expect(capturedCtx?.handoff).toBe(true)
      await typeIntoDraft()
      await act(async () => {
        capturedCtx!.switchClient(ENTITY_B)
      })
      await waitFor(() => expect(capturedCtx?.activeEntity?.id).toBe(ENTITY_B))
      expect(capturedCtx!.draft).toEqual(BLANK)
    })

    it('a hand-off session opening a new invoice gets a blank draft', async () => {
      await bootTwoCompanies(handoffRecord(T, ME))
      expect(capturedCtx?.handoff).toBe(true)
      await typeIntoDraft()
      await act(async () => {
        capturedCtx!.openCreate()
      })
      expect(capturedCtx!.draft).toEqual(BLANK)
    })

    // Control: green before and after.
    it('a persona session switching company gets the demo draft', async () => {
      await bootTwoCompanies(personaRecord())
      expect(capturedCtx?.handoff).toBe(false)
      await typeIntoDraft()
      await act(async () => {
        capturedCtx!.switchClient(ENTITY_B)
      })
      await waitFor(() => expect(capturedCtx?.activeEntity?.id).toBe(ENTITY_B))
      expect(capturedCtx!.draft.number).toBe('INV-2026-00482')
      expect(capturedCtx!.draft.date).toBe('2026-06-16')
      expect(capturedCtx!.draft.buyer).not.toBe('')
      expect(capturedCtx!.draft.items).toHaveLength(2)
    })

    // Control: green before and after.
    it('a persona session opening a new invoice gets the demo draft', async () => {
      await bootTwoCompanies(personaRecord())
      expect(capturedCtx?.handoff).toBe(false)
      await typeIntoDraft()
      await act(async () => {
        capturedCtx!.openCreate()
      })
      expect(capturedCtx!.draft.number).toBe('INV-2026-00482')
      expect(capturedCtx!.draft.date).toBe('2026-06-16')
      expect(capturedCtx!.draft.buyer).not.toBe('')
      expect(capturedCtx!.draft.items).toHaveLength(2)
    })
  })

  it('a redeemed hand-off boot carries handoff:true', async () => {
    configure()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(capturedCtx?.handoff).toBe(true)
    expect(capturedCtx?.connectors).toEqual(NO_CONNECTORS)
    expect(capturedCtx?.customRules).toEqual([])
  })

  it('no render of a hand-off boot shows demo state, stored or redeemed', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(T, ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    await settle()
    expect(seenDemoState.length).toBeGreaterThan(0)
    expect(seenDemoState.filter((r) => r.handoff !== true || r.connected !== 0 || r.rules !== 0)).toEqual([])

    cleanup()
    seenDemoState = []
    localStorage.clear()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    await bootApp()
    await waitForVerifiedWorkspace()
    await settle()
    expect(seenDemoState.length).toBeGreaterThan(0)
    expect(seenDemoState.filter((r) => r.handoff !== true || r.connected !== 0 || r.rules !== 0)).toEqual([])
  })

  // Control: the same capture reads the demo state on a persona session.
  it('every render of a stored persona session shows the demo state', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, JSON.stringify({ v: 1, personaId: 'firm', token: T, me: ME, verified: true }))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    await settle()
    expect(seenDemoState.length).toBeGreaterThan(0)
    expect(seenDemoState.filter((r) => r.handoff !== false || r.connected !== 2 || r.rules !== 5)).toEqual([])
  })

  it("a hand-off workspace's stored custom-rules list is its own, never the seed plus a write", async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(T, ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.customRules).toEqual([])
    const adopted = SUGGESTED_RULES[0]
    await act(async () => {
      capturedCtx!.addSuggestedRule(adopted)
    })
    expect(capturedCtx?.customRules.map((r) => r.key)).toEqual([adopted.key])
    await act(async () => {
      capturedCtx!.removeCustomRule(adopted.key)
    })
    expect(capturedCtx?.customRules, 'the stored empty list wins; no seed returns').toEqual([])
  })

  // Control: a persona session writes onto the seed.
  it("a persona workspace's first write lands on top of the five seeded rules", async () => {
    configure()
    localStorage.setItem(SESSION_KEY, JSON.stringify({ v: 1, personaId: 'firm', token: T, me: ME, verified: true }))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
    expect(capturedCtx?.customRules).toHaveLength(5)
    await act(async () => {
      capturedCtx!.addSuggestedRule(SUGGESTED_RULES[0])
    })
    expect(capturedCtx?.customRules).toHaveLength(6)
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

  // The refresh token is stored in the same record as the access token.
  it('a hand-off boot stores the refresh token with its receipt time', async () => {
    configure()
    ensureSignInState()
    // Readable iat/exp, so the renewable session is not due at once.
    const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
    const live = `${b64({ alg: 'RS256' })}.${b64({ sub: ME.user.id, iat: nowSec(), exp: nowSec() + 3600 })}.sig`
    exchangeReply = ok({ access_token: live, refresh_token: 'R0' })
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    const before = Date.now()
    await bootApp()
    await waitForVerifiedWorkspace()
    const rec = storedRecord()
    expect(rec?.token).toBe(live)
    expect(rec?.refresh_token).toBe('R0')
    // Backdated by the gateway's HandoffTTL (60 s): the code may have waited that long.
    expect(rec?.received_at).toBeGreaterThanOrEqual(before - 60_000)
    expect(rec?.received_at).toBeLessThanOrEqual(Date.now() - 60_000)
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
    mineReply = ok({ invitations: [] })
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=no-workspace`]))
    expect(exchangeBodies).toHaveLength(1)
    expect(meAuth).toEqual([`Bearer ${T}`])
    expect(workspacesCalls, 'a token without answers provisions nothing').toEqual([])
    expect(refreshBodies).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('a join offer shows the Join screen, not the no-workspace path', async () => {
    configure()
    ensureSignInState()
    meReply = fail(403, 'forbidden')
    mineReply = ok({ invitations: [{ id: 'a', workspace: 'WS', role: 'admin', inviter: null, expires_at: '2026-10-20T00:00:00Z' }] })
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(screen.getByTestId('join-screen')).toBeTruthy())
    await settle()
    expect(hrefWrites).toEqual([])
    expect(workspacesCalls).toEqual([])
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
  it('a hand-off carrying persona= redeems and strips', async () => {
    configure()
    const S = ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}&persona=firm`)
    const replace = vi.spyOn(window.history, 'replaceState')
    const push = vi.spyOn(window.history, 'pushState')
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    await settle()

    expect(exchangeBodies, 'the code is redeemed once').toEqual([{ code: CODE, state: S }])
    const urls = historyUrls([replace, push])
    expect(urls.length, 'the strip wrote at least one history entry').toBeGreaterThan(0)
    expect(urls.filter((u) => /handoff=|persona=/.test(u))).toEqual([])
    expect(window.location.search).toBe('')
    expect(loginCalls, 'no mint').toBe(0)
    expect(hrefWrites).toEqual([])
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
      // Accepted limit: no notice is shown.
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

describe('a confirm code over a live session (LOGFIX-04-05, D10, D18)', () => {
  const A_ME: Me = { ...OLD_ME, user: { ...OLD_ME.user, email: 'a@corp.example' } }
  const A_TOKEN = jwt(A_ME.user.id, nowSec() + 3600)
  const NOTICE = (who: string) => `Your email is confirmed. You are signed in ${who}. To use the confirmed account, sign out and sign in with it.`
  const toast = () => screen.queryByTestId('verify-confirmed-toast')

  async function bootOverLive(me: Me, withMarker = true) {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(A_TOKEN, me))
    const S = ensureSignInState()
    if (withMarker) holdPendingVerify()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    return S
  }

  it('a verify code over a live session keeps A, posts nothing and shows the confirmed notice', async () => {
    await bootOverLive(A_ME)
    await settle()
    expect(exchangeBodies).toHaveLength(0)
    expect(meAuth).toHaveLength(0)
    expect(storedRecord()?.token).toBe(A_TOKEN)
    expect(toast()?.textContent).toContain(NOTICE('as a@corp.example'))
    expect(screen.getByTestId('verify-confirmed-toast').firstChild?.textContent).toBe(NOTICE('as a@corp.example'))
    expect(sessionStorage.getItem('invoice-os.pendingVerify')).toBeNull()
    expect(window.location.search).toBe('')
  })

  it('a verify code with no session signs in as B', async () => {
    configure()
    const S = ensureSignInState()
    holdPendingVerify()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitForVerifiedWorkspace()
    await settle()
    expect(exchangeBodies).toEqual([{ code: CODE, state: S }])
    expect(toast()).toBeNull()
    expect(sessionStorage.getItem('invoice-os.pendingVerify')).toBeNull()
  })

  it('a plain handoff over a live session shows no notice', async () => {
    await bootOverLive(A_ME, false)
    await settle()
    expect(exchangeBodies).toHaveLength(0)
    expect(capturedCtx?.user.tenantName).toBe(OLD_ME.tenant.name)
    expect(toast()).toBeNull()
  })

  it('the confirmed notice falls back to the display name, then to another account', async () => {
    await bootOverLive({ ...A_ME, user: { ...A_ME.user, email: null } })
    expect(toast()?.textContent).toContain(NOTICE('as Adaeze Nwankwo'))
    cleanup()
    capturedCtx = undefined
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    await bootOverLive({ ...A_ME, user: { ...A_ME.user, email: null, display_name: null } })
    expect(toast()?.textContent).toContain(NOTICE('to another account'))
  })

  it('a stale verify marker over a live session shows no notice', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(A_TOKEN, A_ME))
    holdPendingVerify(Date.now() - 11 * 60 * 1000)
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    await settle()
    expect(exchangeBodies).toHaveLength(0)
    expect(toast()).toBeNull()
  })

  it('a handoff beside auth=verify over a live session neither bounces nor posts, and names A', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, handoffRecord(A_TOKEN, A_ME))
    ensureSignInState()
    holdPendingVerify()
    window.history.replaceState(null, '', `/?handoff=${CODE}&auth=verify#token=tok_1`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user).toBeDefined())
    await settle()
    expect(hrefWrites).toEqual([])
    expect(exchangeBodies).toHaveLength(0)
    expect(storedRecord()?.token).toBe(A_TOKEN)
    expect(toast()?.textContent).toContain(NOTICE('as a@corp.example'))
  })

  it('the confirmed notice renders the email as text', async () => {
    await bootOverLive({ ...A_ME, user: { ...A_ME.user, email: '<b id="x">a</b>@corp.example' } })
    expect(toast()?.querySelector('#x')).toBeNull()
    expect(toast()?.textContent).toContain('<b id="x">a</b>@corp.example')
  })

  it('the confirmed notice dismisses like its sibling', async () => {
    await bootOverLive(A_ME)
    fireEvent.click(screen.getByLabelText('Dismiss'))
    expect(toast()).toBeNull()
    cleanup()
    capturedCtx = undefined
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      await bootOverLive(A_ME)
      expect(toast()).not.toBeNull()
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5200)
      })
      expect(toast()).toBeNull()
    } finally {
      vi.useRealTimers()
    }
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
      ['me unknown kind', ok({ ...ME, tenant: { ...ME.tenant, kind: 'bogus' } })],
      ['me no kind', ok({ ...ME, tenant: { id: ME.tenant.id, name: ME.tenant.name } })],
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

  it('a /me 200 with a tenant but no tenant id reports failed', async () => {
    configure()
    ensureSignInState()
    meReply = ok({ tenant: { name: 'No Id', kind: 'firm' }, user: ME.user })
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    expect(capturedCtx).toBeUndefined()
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
    const ONE_INVITE = ok({ invitations: [{ id: 'a', workspace: 'WS', role: 'admin', inviter: null, expires_at: '2026-10-20T00:00:00Z' }] })
    const cases: [string, Reply, Reply | null][] = [
      ['success', ok(ME), null],
      ['me 403', fail(403, 'forbidden'), null],
      ['me 500', fail(500, 'boom'), null],
      ['me 403, mine one invite, Join screen', fail(403, 'forbidden'), ONE_INVITE],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [name, reply, mine] of cases) {
      configure()
      ensureSignInState()
      meReply = reply
      if (mine) mineReply = mine
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

  it('pinned: a corrupt record on a ?persona= boot warns once and leaves for landing without a mint', async () => {
    configure()
    localStorage.setItem(SESSION_KEY, '{not json')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', '/?persona=firm')
    const { hrefWrites } = interceptHref()
    await bootApp()
    await settle()
    expect(capturedCtx, 'no workspace opens').toBeUndefined()
    expect(loginCalls).toBe(0)
    expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}`])
    expect(warn.mock.calls.filter((c) => String(c[0]).startsWith('[session]'))).toHaveLength(1)
  })

  it('a stored hand-off record with no tenant kind is dropped and the boot signs in again', async () => {
    const OLD_T = jwt(OLD_ME.user.id, nowSec() + 3600)
    const noKind = { tenant: { id: OLD_ME.tenant.id, name: OLD_ME.tenant.name }, user: OLD_ME.user }
    configure()
    // Control: with a kind the same record mounts.
    localStorage.setItem(SESSION_KEY, handoffRecord(OLD_T, OLD_ME))
    interceptHref()
    await bootApp()
    await waitFor(() => expect(capturedCtx?.user, 'control: the record with a kind mounts').toBeDefined())
    cleanup()
    capturedCtx = undefined

    localStorage.setItem(SESSION_KEY, JSON.stringify({ v: 1, personaId: 'firm', token: OLD_T, me: noKind, verified: true, handoff: true }))
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { hrefWrites } = interceptHref()
    await bootApp()
    await settle()
    expect(capturedCtx, 'no workspace opens on a record without a kind').toBeUndefined()
    expect(warn.mock.calls.filter((c) => String(c[0]).startsWith('[session]'))).toHaveLength(1)
    expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}`])
    expect(exchangeBodies).toHaveLength(0)
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

describe('the first sign-in provisions the registered workspace', () => {
  const CHAIN = (base: string) => [
    `${base}/auth/exchange`,
    `${base}/api/tenancy/v1/me`,
    `${base}/api/tenancy/v1/invitations/mine`,
    `${base}/api/tenancy/v1/workspaces`,
    `${base}/auth/refresh`,
    `${base}/api/tenancy/v1/me`,
  ]

  function registered(opts: { exchange?: Reply; me?: Reply[] } = {}) {
    configure()
    ensureSignInState()
    exchangeReply = opts.exchange ?? ok({ access_token: T_ANSWERS, refresh_token: 'R0' })
    meQueue = opts.me ?? [fail(403, 'forbidden'), ok(ME)]
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    return interceptHref()
  }

  it('a registered account lands in its new workspace on first sign-in', async () => {
    const { hrefWrites } = registered()
    const before = Date.now()
    // Every DOM state from boot to the workspace: the splash alone, never a prompt.
    const frames: { text: string; prompts: number; usersSeen: number }[] = []
    const observer = new MutationObserver(() =>
      frames.push({
        text: document.body.textContent ?? '',
        prompts: document.querySelectorAll('button, input, form, a, [role="dialog"]').length,
        usersSeen: seenUsers.length,
      }),
    )
    observer.observe(document.body, { childList: true, subtree: true, characterData: true })
    onTestFinished(() => observer.disconnect())
    await bootApp()
    await waitForVerifiedWorkspace()
    const preWorkspace = frames.filter((f) => f.usersSeen === 0)
    expect(preWorkspace.length, 'frames before the workspace').toBeGreaterThan(0)
    const screens = [...new Set(preWorkspace.map((f) => f.text).filter((t) => t !== ''))]
    expect(screens, 'one screen before the workspace: the splash').toHaveLength(1)
    expect(screens[0]).toContain('Opening your workspace…')
    expect(Math.max(...preWorkspace.map((f) => f.prompts)), 'no button, field, link or dialog before the workspace').toBe(0)
    expect(fetchUrls.slice(0, 6)).toEqual(CHAIN(GATEWAY))
    expect(workspacesCalls).toEqual([{ auth: `Bearer ${T_ANSWERS}`, body: ANSWERS }])
    expect(refreshBodies).toEqual([{ refresh_token: 'R0' }])
    expect(meAuth).toEqual([`Bearer ${T_ANSWERS}`, `Bearer ${T2}`])
    expect(hrefWrites, 'no landing navigation').toEqual([])
    // No confirmation step: the first render with a user is the signed-in workspace.
    expect(seenUsers[0]).toMatchObject({ tenantName: ME.tenant.name, verified: true })
    expect(screen.queryByRole('dialog')).toBeNull()
    const rec = storedRecord()
    expect(rec?.token).toBe(T2)
    expect(rec?.refresh_token).toBe('R1')
    // Refreshed just now, so not backdated by the hand-off TTL.
    expect(rec?.received_at).toBeGreaterThanOrEqual(before)
    expect(rec?.me).toEqual(ME)
  })

  it('a registered account whose workspace already exists lands after a 409', async () => {
    workspacesReply = fail(409, 'already has a workspace')
    const { hrefWrites } = registered()
    await bootApp()
    await waitForVerifiedWorkspace()
    expect(fetchUrls.slice(0, 6)).toEqual(CHAIN(GATEWAY))
    expect(hrefWrites).toEqual([])
    expect(storedRecord()?.token).toBe(T2)
  })

  it('a registered account provisions and refreshes once under StrictMode', async () => {
    registered()
    await bootApp({ strict: true })
    await waitForVerifiedWorkspace()
    expect(fetchUrls.filter((u) => u.endsWith('/auth/exchange'))).toHaveLength(1)
    expect(workspacesCalls).toHaveLength(1)
    expect(refreshBodies).toEqual([{ refresh_token: 'R0' }])
  })

  it('a registered account still blocked after a 409 reports no-workspace', async () => {
    workspacesReply = fail(409, 'already has a workspace')
    const { hrefWrites } = registered({ me: [fail(403, 'forbidden'), fail(403, 'forbidden')] })
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=no-workspace`]))
    expect(fetchUrls.slice(0, 6)).toEqual(CHAIN(GATEWAY))
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  const failures: [string, () => void, Reply | undefined, number][] = [
    ['provisioning 400', () => (workspacesReply = fail(400, 'bad request')), undefined, 4],
    ['provisioning 500', () => (workspacesReply = fail(500, 'boom')), undefined, 4],
    ['refresh 401', () => (refreshReply = fail(401, 'invalid refresh token')), undefined, 5],
    ['an exchange without a refresh token', () => {}, ok({ access_token: T_ANSWERS }), 4],
  ]
  for (const [name, arrange, exchange, calls] of failures) {
    it(`a registered account with ${name} reports failed and stores nothing`, async () => {
      arrange()
      const { hrefWrites } = registered({ exchange })
      await bootApp()
      await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
      expect(fetchUrls.slice(0, calls)).toEqual(CHAIN(GATEWAY).slice(0, calls))
      expect(fetchUrls).toHaveLength(calls)
      expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    })
  }

  const badLists: [string, Reply][] = [
    ['a 500', fail(500, 'boom')],
    ['a malformed body', ok({ invitations: 'x' })],
  ]
  for (const [name, mine] of badLists) {
    it(`a registered account with ${name} on the invite lookup reports failed and does not provision`, async () => {
      mineReply = mine
      const { hrefWrites } = registered()
      await bootApp()
      await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
      expect(workspacesCalls).toEqual([])
      expect(localStorage.getItem(SESSION_KEY)).toBeNull()
    })
  }

  it('an account without answers and a 500 on the invite lookup reports failed, not no-workspace', async () => {
    configure()
    ensureSignInState()
    meReply = fail(403, 'forbidden')
    mineReply = fail(500, 'boom')
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    const { hrefWrites } = interceptHref()
    await bootApp()
    await waitFor(() => expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`]))
    expect(workspacesCalls).toEqual([])
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

  type Leg = 'exchange' | 'me' | 'workspaces' | 'refresh' | 'second /me'
  let urls: string[]

  // Every leg before `hangAt` answers at once, so a late leg proves the one signal spans the chain.
  function stubFetch(hangAt: Leg) {
    const registered = hangAt === 'workspaces' || hangAt === 'refresh' || hangAt === 'second /me'
    let meCalls = 0
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init?: RequestInit) => {
        urls.push(url)
        if (url === `${GATEWAY}/auth/exchange`) {
          signals.exchange.push(init?.signal ?? undefined)
          return hangAt === 'exchange'
            ? hang(init?.signal)
            : ok({ access_token: registered ? T_ANSWERS : T, refresh_token: 'R0' })()
        }
        if (url === `${GATEWAY}/api/tenancy/v1/me`) {
          signals.me.push(init?.signal ?? undefined)
          meCalls++
          return registered && meCalls === 1 ? fail(403, 'forbidden')() : hang(init?.signal)
        }
        if (url === `${GATEWAY}/api/tenancy/v1/invitations/mine`) {
          return ok({ invitations: [] })()
        }
        if (url === `${GATEWAY}/api/tenancy/v1/workspaces`) {
          return hangAt === 'workspaces' ? hang(init?.signal) : ok({ tenant: ME.tenant })()
        }
        if (url === `${GATEWAY}/auth/refresh`) {
          return hangAt === 'refresh' ? hang(init?.signal) : ok({ access_token: T2, refresh_token: 'R1' })()
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
    urls = []
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

  const LEGS: [Leg, string[], number][] = [
    ['exchange', ['/auth/exchange'], 0],
    ['me', ['/auth/exchange', '/api/tenancy/v1/me'], 1],
    ['workspaces', ['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine', '/api/tenancy/v1/workspaces'], 1],
    ['refresh', ['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine', '/api/tenancy/v1/workspaces', '/auth/refresh'], 1],
    ['second /me', ['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine', '/api/tenancy/v1/workspaces', '/auth/refresh', '/api/tenancy/v1/me'], 2],
  ]
  for (const [leg, path, meCalls] of LEGS) {
    it(`a hung ${leg} fails once at 15 s, not before`, async () => {
      configure()
      ensureSignInState()
      stubFetch(leg)
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      window.history.replaceState(null, '', `/?handoff=${CODE}`)
      const { hrefWrites } = interceptHref()
      await bootApp()
      await tick(0)
      expect(signals.exchange, 'one exchange call').toHaveLength(1)
      expect(signals.me, 'the /me calls').toHaveLength(meCalls)
      expect(urls, 'the chain stops at the hung leg').toEqual(path.map((p) => `${GATEWAY}${p}`))

      await tick(TIMEOUT_MS - 1)
      expect(hrefWrites, 'no navigation before 15 s').toEqual([])
      expect(warn).not.toHaveBeenCalled()
      expect(screen.getByText('Opening your workspace…')).toBeTruthy()

      await tick(1)
      expect(hrefWrites).toEqual([`${LANDING}/?state=${storedState()}&signin=failed`])
      expect(AbortSignal.timeout).toHaveBeenCalledTimes(1)
      expect(AbortSignal.timeout).toHaveBeenCalledWith(TIMEOUT_MS)
      expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=${STATE_RE}&signin=failed$`))
      expect(warn.mock.calls.filter((c) => String(c[0]).includes('hand-off redemption failed'))).toHaveLength(1)
      expect(warn).toHaveBeenCalledTimes(1)
      expect(localStorage.getItem(SESSION_KEY)).toBeNull()
      expect(urls, 'nothing runs after the abort').toHaveLength(path.length)

      await tick(TIMEOUT_MS)
      expect(hrefWrites, 'still one navigation').toHaveLength(1)
    })
  }

  it('both redemption calls carry an abort signal', async () => {
    configure()
    ensureSignInState()
    stubFetch('me')
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
