// @vitest-environment jsdom
// The Join screen: a tenant-less sign-in with invites waiting (LOGFIX-03).

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Me } from './auth'
import { SESSION_KEY } from './lib/session'
import { ensureSignInState } from './lib/signInState'
import type { PlatformCtx } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
const MINE = `${GATEWAY}/api/tenancy/v1/invitations/mine`
const ACCEPT = (id: string) => `${GATEWAY}/api/tenancy/v1/invitations/${id}/accept`

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Obi Partners', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Ada Obi', email: 'ada@example.com' },
}
const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
const jwt = (extra: object = {}) => `${b64({ alg: 'RS256' })}.${b64({ sub: ME.user.id, exp: Math.floor(Date.now() / 1000) + 3600, ...extra })}.sig`
const T = jwt()
const T2 = jwt({ iat: Math.floor(Date.now() / 1000), exp: Math.floor(Date.now() / 1000) + 7200, app_metadata: { tenant_id: ME.tenant.id } })
const ANSWERS = { workspace_name: 'Own Books', display_name: 'Ada Obi', kind: 'firm' }
const T_ANSWERS = jwt({ user_metadata: { registration: ANSWERS } })

const invite = (id: string, workspace: string) => ({ id, workspace, role: 'reviewer', inviter: 'Ada Obi', expires_at: '2026-10-20T00:00:00Z' })

type Reply = () => Promise<unknown>
const ok = (body: unknown): Reply => () => Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body), text: () => Promise.resolve('') })
const fail = (status: number, error: string): Reply => () =>
  Promise.resolve({ ok: false, status, statusText: String(status), json: () => Promise.resolve({ error }), text: () => Promise.resolve('') })

let ctx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    ctx = p.ctx
    return null
  },
}))

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((k: string) => (store.has(k) ? (store.get(k) as string) : null)),
    setItem: vi.fn((k: string, v: string) => void store.set(k, v)),
    removeItem: vi.fn((k: string) => void store.delete(k)),
    clear: vi.fn(() => store.clear()),
  }
}

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
  return hrefWrites
}

let calls: { url: string; method: string; body: unknown }[]
let replies: Record<string, Reply>
let meQueue: Reply[]
let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
  ctx = undefined
  calls = []
  meQueue = [fail(403, 'forbidden'), ok(ME)]
  replies = {
    [`${GATEWAY}/auth/exchange`]: ok({ access_token: T, refresh_token: 'R0' }),
    [MINE]: ok({ invitations: [invite('inv-1', 'Obi Partners')] }),
    [ACCEPT('inv-1')]: ok({}),
    [`${GATEWAY}/api/tenancy/v1/workspaces`]: ok({ tenant: ME.tenant }),
    [`${GATEWAY}/auth/refresh`]: ok({ access_token: T2, refresh_token: 'R1' }),
    [`${GATEWAY}/auth/sign-out`]: ok(''),
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string; body?: string }) => {
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body ? JSON.parse(init.body) : null })
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return (meQueue.shift() ?? ok(ME))()
      return (replies[url] ?? ok({ entities: [], policies: [], members: [], roles: [], invoices: [], clients: [], total: 0 }))()
    }),
  )
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubEnv('VITE_LANDING_URL', LANDING)
  vi.spyOn(console, 'warn').mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  window.history.replaceState(null, '', '/')
})

async function boot(): Promise<string[]> {
  ensureSignInState()
  window.history.replaceState(null, '', `/?handoff=${CODE}`)
  const hrefWrites = interceptHref()
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(<App />)
  })
  return hrefWrites
}

const joinScreen = () => waitFor(() => expect(screen.getByTestId('join-screen')).toBeTruthy())
const posts = (suffix: string) => calls.filter((c) => c.method === 'POST' && c.url.endsWith(suffix))

describe('the Join screen in the app', () => {
  it('app_joinOfferRendersTheJoinScreenAndNoBounce', async () => {
    const hrefWrites = await boot()
    await joinScreen()
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(screen.getByRole('heading').textContent).toBe('Join Obi Partners')
    expect(hrefWrites).toEqual([])
    expect(localStorage.getItem(SESSION_KEY)).toBeNull()
  })

  it('app_joinMountsTheJoinedWorkspace', async () => {
    await boot()
    await joinScreen()
    fireEvent.click(screen.getByRole('button', { name: 'Join Obi Partners' }))
    await waitFor(() => expect(ctx?.user).toBeDefined())
    expect(ctx?.user).toMatchObject({ tenantName: 'Obi Partners', verified: true })
    expect(posts('/invitations/inv-1/accept')).toHaveLength(1)
    const stored = JSON.parse(localStorage.getItem(SESSION_KEY) ?? 'null')
    expect(stored.token).toBe(T2)
    expect(stored.refresh_token).toBe('R1')
    expect(screen.queryByTestId('join-screen')).toBeNull()
  })

  it.each([
    ['not valid', 404, 'this invite is no longer valid', `${LANDING}/?invite=invalid`],
    ['already a member', 409, 'you already belong to a workspace', `${LANDING}/?invite=already-member`],
  ])('app_refusedJoinGoesToTheInviteNotice: %s', async (_name, status, error, dest) => {
    replies[ACCEPT('inv-1')] = fail(status, error)
    meQueue = [fail(403, 'forbidden'), fail(403, 'forbidden')]
    const hrefWrites = await boot()
    await joinScreen()
    fireEvent.click(screen.getByRole('button', { name: 'Join Obi Partners' }))
    await waitFor(() => expect(hrefWrites).toEqual([dest]))
    expect(ctx).toBeUndefined()
  })

  it('app_refusedJoinGoesToTheInviteNotice: a server error reports failed', async () => {
    replies[ACCEPT('inv-1')] = fail(500, 'boom')
    const hrefWrites = await boot()
    await joinScreen()
    fireEvent.click(screen.getByRole('button', { name: 'Join Obi Partners' }))
    await waitFor(() => expect(hrefWrites).toHaveLength(1))
    expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&signin=failed$`))
  })

  it('app_signOutOnTheJoinScreenRevokesThenLeaves', async () => {
    const hrefWrites = await boot()
    await joinScreen()
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(hrefWrites).toEqual([LANDING]))
    expect(posts('/auth/sign-out').map((c) => c.body)).toEqual([{ refresh_token: 'R0' }])
    const order = calls.map((c) => c.url)
    expect(order.indexOf(`${GATEWAY}/auth/sign-out`)).toBeGreaterThan(order.indexOf(MINE))
    expect(posts('/accept')).toEqual([])
  })

  it('app_doubleClickJoinSendsOneAccept', async () => {
    await boot()
    await joinScreen()
    const join = screen.getByRole('button', { name: 'Join Obi Partners' })
    await act(async () => {
      fireEvent.click(join)
      fireEvent.click(join)
    })
    await waitFor(() => expect(ctx?.user).toBeDefined())
    expect(posts('/accept')).toHaveLength(1)
  })

  it('app_createOwnProvisionsAndOpensTheNewWorkspace', async () => {
    replies[`${GATEWAY}/auth/exchange`] = ok({ access_token: T_ANSWERS, refresh_token: 'R0' })
    await boot()
    await joinScreen()
    fireEvent.click(screen.getByRole('button', { name: 'Create my own workspace' }))
    await waitFor(() => expect(ctx?.user).toBeDefined())
    expect(posts('/api/tenancy/v1/workspaces').map((c) => c.body)).toEqual([ANSWERS])
    expect(posts('/accept')).toEqual([])
  })

  it('app_noAnswersMeansNoCreateOwnButton', async () => {
    await boot()
    await joinScreen()
    expect(screen.queryByRole('button', { name: 'Create my own workspace' })).toBeNull()
  })
})
