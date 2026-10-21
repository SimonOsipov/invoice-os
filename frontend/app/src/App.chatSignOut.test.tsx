// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://app.ascomply.com/" }
// Every sign-out ends the chat before it leaves.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION } from '../../landing/src/consent'
import { SHARED_COOKIE_DOMAIN } from '../../landing/src/hubspot'
import { COOKIES, container, installSdk, jarNames, resetJar, seed, w } from '../../landing/src/hubspotChat.test.util'
import type { Me } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { SESSION_KEY } from './lib/session'
import { ensureSignInState } from './lib/signInState'
import type { PlatformCtx } from './types'

const LANDING = 'https://landing.example'
const GATEWAY = 'https://gw.test'
const REVOKED = `${GATEWAY}/api/revoked`
const HANDOFF_EXIT = [LANDING, expect.stringMatching(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}$`))]
const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
const MIN = 60_000

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}
const b64url = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
function jwt(): string {
  const iat = Math.floor((Date.now() - MIN) / 1000)
  const claims = { sub: ME.user.id, iat, exp: iat + 3600, app_metadata: { tenant_id: ME.tenant.id }, session_id: 'sid1' }
  return `${b64url('{"alg":"RS256"}')}.${b64url(JSON.stringify(claims))}.sig`
}
const handoffRecord = () =>
  JSON.stringify({ v: 1, personaId: 'firm', token: jwt(), me: ME, verified: true, handoff: true, refresh_token: 'R1', received_at: Date.now() - MIN })

const hsq = () => (w._hsq ?? []) as unknown[][]
const answer = (status: number, body: unknown) =>
  Promise.resolve({ ok: status >= 200 && status < 300, status, statusText: String(status), json: () => Promise.resolve(body), text: () => Promise.resolve('') })

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

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((k: string) => (store.has(k) ? (store.get(k) as string) : null)),
    setItem: vi.fn((k: string, v: string) => void store.set(k, v)),
    removeItem: vi.fn((k: string) => void store.delete(k)),
    clear: vi.fn(() => store.clear()),
  }
}

// One order log shared by the href proxy and the widget stub.
let log: string[] = []
let hrefWrites: string[] = []
function interceptHref() {
  const real = window.location
  const proxy = new Proxy(real, {
    set(target, prop, value) {
      if (prop === 'href') {
        hrefWrites.push(value)
        log.push('href')
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
}

function grant() {
  const rec = { analytics: true, ts: new Date().toISOString(), v: CONSENT_VERSION }
  document.cookie = `${CONSENT_STORAGE_KEY}=${encodeURIComponent(JSON.stringify(rec))}; Domain=${SHARED_COOKIE_DOMAIN}; Path=/`
}

function armChat() {
  const sdk = installSdk()
  const remove = sdk.widget.remove
  sdk.widget.remove = () => {
    log.push('remove')
    remove()
  }
  seed(...COOKIES)
}

function expectChatGone() {
  for (const name of COOKIES) expect(jarNames(), name).not.toContain(name)
  expect(container()).toBeNull()
  expect(hsq().some((e) => e[0] === 'doNotTrack')).toBe(false)
}

async function mountWorkspace() {
  localStorage.setItem(SESSION_KEY, handoffRecord())
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(<App />)
  })
  await waitFor(() => expect(capturedCtx?.user, 'the workspace must mount').toBeDefined())
  await act(async () => {
    await new Promise((r) => setTimeout(r, 30))
  })
}

let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubEnv('VITE_LANDING_URL', LANDING)
  document.head.innerHTML = ''
  document.body.innerHTML = ''
  delete w._hsq
  delete w.HubSpotConversations
  delete w.hsConversationsSettings
  delete w.hsConversationsOnReady
  window.history.replaceState(null, '', '/')
  resetJar()
  capturedCtx = undefined
  log = []
  hrefWrites = []
  interceptHref()
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url === `${GATEWAY}/auth/sign-out`) return Promise.resolve(new Response(null, { status: 204 }))
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return answer(200, ME)
      if (url.startsWith(REVOKED)) return answer(401, { error: 'unauthorized' })
      return answer(200, {
        entities: [], policies: [], members: [], roles: [], invoices: [], pagination: { limit: 50, offset: 0, total: 0 },
        total: 0, clients: [], totals: EMPTY_BUCKET, rejection_reasons: [],
      })
    }),
  )
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  window.history.replaceState(null, '', '/')
})

describe('sign-out ends the chat (CHAT-03-05)', () => {
  it('signOut_endsTheChatBeforeLeaving', async () => {
    grant()
    await mountWorkspace()
    armChat()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    })
    await waitFor(() => expect(hrefWrites).toEqual(HANDOFF_EXIT))
    expectChatGone()
    expect(log.indexOf('remove')).toBeGreaterThanOrEqual(0)
    expect(log.indexOf('remove')).toBeLessThan(log.indexOf('href'))
  })

  it('endRevokedSession_endsTheChatBeforeLeaving', async () => {
    grant()
    await mountWorkspace()
    armChat()
    await act(async () => {
      await capturedCtx!.authedFetch(REVOKED).catch(() => {})
    })
    await waitFor(() => expect(hrefWrites.length).toBeGreaterThan(0))
    expect(hrefWrites[0]).toBe(LANDING)
    expectChatGone()
    expect(log.indexOf('remove')).toBeGreaterThanOrEqual(0)
    expect(log.indexOf('remove')).toBeLessThan(log.indexOf('href'))
  })

  it('joinSignOut_deletesTheHubSpotCookies', async () => {
    const T = `${b64url('{"alg":"RS256"}')}.${b64url(JSON.stringify({ sub: ME.user.id, exp: Math.floor(Date.now() / 1000) + 3600 }))}.sig`
    const ok = (body: unknown) => answer(200, body)
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url === `${GATEWAY}/auth/exchange`) return ok({ access_token: T, refresh_token: 'R0' })
        if (url === `${GATEWAY}/api/tenancy/v1/me`) return answer(403, { error: 'forbidden' })
        if (url === `${GATEWAY}/api/tenancy/v1/invitations/mine`) {
          return ok({ invitations: [{ id: 'inv-1', workspace: 'Obi Partners', role: 'reviewer', inviter: 'Ada Obi', expires_at: '2026-10-20T00:00:00Z' }] })
        }
        return ok({})
      }),
    )
    grant()
    ensureSignInState()
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    vi.resetModules()
    const { default: App } = await import('./App')
    await act(async () => {
      render(<App />)
    })
    await waitFor(() => expect(screen.getByTestId('join-screen')).toBeTruthy())
    // The join screen loads no chat, so only the cookies are seeded.
    seed(...COOKIES)
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    })
    await waitFor(() => expect(hrefWrites).toEqual([LANDING]))
    expectChatGone()
  })
})
