// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://app.ascomply.com/" }
// The mounted Workspace: cookie notice, Cookie choices, the chat after Accept, the suspended teardown.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION, parseConsent } from '../../landing/src/consent'
import { SHARED_COOKIE_DOMAIN } from '../../landing/src/hubspot'
import { COOKIES, container, installSdk, jarNames, resetJar, seed, w } from '../../landing/src/hubspotChat.test.util'
import { sliceCookieNoticeCss } from '../../library/src/cookieNoticeCss'
import type { Me } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { captureDestination } from './lib/deepLink'
import { NOT_ACTIVE_MEMBER_MESSAGE } from './lib/authedFetch'
import { SESSION_KEY } from './lib/session'
import type { PlatformCtx } from './types'

const SRC_DIR = dirname(fileURLToPath(import.meta.url))
const LANDING_CSS = readFileSync(join(SRC_DIR, '../../landing/src/styles/landing.css'), 'utf8')
const GATEWAY = 'https://gw.test'
const SUSPEND = `${GATEWAY}/api/suspend`
const EMAIL = 'adaeze.nwankwo@example.com'
const MIN = 60_000

const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: EMAIL },
}

const b64url = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
function jwt(extra: Record<string, unknown> = {}): string {
  const iat = Math.floor((Date.now() - MIN) / 1000)
  const claims = { sub: ME.user.id, iat, exp: iat + 3600, app_metadata: { tenant_id: ME.tenant.id }, session_id: 'sid1', ...extra }
  return `${b64url('{"alg":"RS256"}')}.${b64url(JSON.stringify(claims))}.sig`
}
function handoffRecord(token = jwt()): string {
  return JSON.stringify({ v: 1, personaId: 'firm', token, me: ME, verified: true, handoff: true, refresh_token: 'R1', received_at: Date.now() - MIN })
}
const UNVERIFIED = JSON.stringify({ v: 1, personaId: 'firm', token: null, me: null, verified: false })
const UNVERIFIED_WITH_ME = JSON.stringify({ v: 1, personaId: 'firm', token: null, me: ME, verified: false })

function setConsent(analytics: boolean) {
  const rec = { analytics, ts: new Date().toISOString(), v: CONSENT_VERSION }
  document.cookie = `${CONSENT_STORAGE_KEY}=${encodeURIComponent(JSON.stringify(rec))}; Domain=${SHARED_COOKIE_DOMAIN}; Path=/`
}
function storedConsent() {
  const pair = document.cookie.split(';').map((p) => p.trim()).find((p) => p.startsWith(`${CONSENT_STORAGE_KEY}=`))
  return pair ? parseConsent(decodeURIComponent(pair.slice(CONSENT_STORAGE_KEY.length + 1))) : null
}

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

const answer = (status: number, body: unknown) =>
  Promise.resolve({ ok: status >= 200 && status < 300, status, statusText: String(status), json: () => Promise.resolve(body) })

function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url === SUSPEND) return answer(403, { error: NOT_ACTIVE_MEMBER_MESSAGE })
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return answer(200, ME)
      return answer(200, {
        entities: [], policies: [], members: [], roles: [], invoices: [], pagination: { limit: 50, offset: 0, total: 0 },
        total: 0, clients: [], totals: EMPTY_BUCKET, rejection_reasons: [],
      })
    }),
  )
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

let hrefWrites: string[] = []
async function mount(raw: string) {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  localStorage.setItem(SESSION_KEY, raw)
  hrefWrites = interceptHref()
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

const notice = () => screen.queryByRole('region', { name: 'Cookie notice' })
const loaders = () => document.head.querySelectorAll('script#hs-script-loader')
const hsq = () => (w._hsq ?? []) as unknown[][]
const click = async (el: HTMLElement) => {
  await act(async () => {
    fireEvent.click(el)
  })
}
const cookieChoices = () => click(screen.getByRole('button', { name: 'Cookie choices' }))
const choose = (name: 'Accept' | 'Reject') => click(within(notice()!).getByRole('button', { name }))

async function suspend() {
  await act(async () => {
    await capturedCtx!.authedFetch(SUSPEND).catch(() => {})
  })
}

let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  document.head.innerHTML = ''
  document.body.innerHTML = ''
  delete w._hsq
  delete w.HubSpotConversations
  delete w.hsConversationsSettings
  delete w.hsConversationsOnReady
  window.history.replaceState(null, '', '/')
  resetJar()
  capturedCtx = undefined
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

describe('App cookie notice and chat (CHAT-03-04)', () => {
  it('app_noStoredChoiceShowsTheNoticeInTheScroller', async () => {
    await mount(handoffRecord())
    const regions = screen.getAllByRole('region', { name: 'Cookie notice' })
    expect(regions).toHaveLength(1)
    const scroller = regions[0].closest('.pf-scroll')
    expect(scroller, 'the notice sits inside .pf-scroll').not.toBeNull()
    expect(scroller!.lastElementChild!.classList.contains('cn-spacer')).toBe(true)
    const styles = document.querySelectorAll('style')
    expect(styles).toHaveLength(1)
    expect(styles[0].textContent).toBe(sliceCookieNoticeCss(LANDING_CSS))
  })

  it('app_aStoredChoiceShowsNoNotice', async () => {
    setConsent(true)
    await mount(handoffRecord())
    expect(notice()).toBeNull()
  })

  it('app_cookieChoicesReopensTheNoticeWithTheSetting', async () => {
    setConsent(true)
    await mount(handoffRecord())
    await cookieChoices()
    expect(within(notice()!).getByText('Analytics cookies are on.').classList.contains('cn-setting')).toBe(true)
  })

  it('app_acceptLoadsTheChatWithTheAccountEmail', async () => {
    await mount(handoffRecord())
    await choose('Accept')
    expect(notice()).toBeNull()
    expect(storedConsent()?.analytics).toBe(true)
    expect(hsq()[0]).toEqual(['identify', { email: EMAIL }])
    expect(loaders()).toHaveLength(1)
  })

  it('app_anUnverifiedSessionPushesNoIdentify', async () => {
    setConsent(true)
    await mount(UNVERIFIED)
    expect(loaders()).toHaveLength(1)
    expect(hsq().some((e) => e[0] === 'identify')).toBe(false)
  })

  it('app_theIdentifyEmailIsTheMeEmailNeverTokenMetadata', async () => {
    setConsent(true)
    await mount(handoffRecord(jwt({ user_metadata: { email: 'other@example.com' } })))
    const identify = hsq().filter((e) => e[0] === 'identify')
    expect(identify).toEqual([['identify', { email: EMAIL }]])
  })

  it('app_aReturningUserGetsTheChatAtBoot', async () => {
    setConsent(true)
    await mount(handoffRecord())
    expect(loaders()).toHaveLength(1)
  })

  it('app_rejectRemovesTheChatInPlace', async () => {
    setConsent(true)
    await mount(handoffRecord())
    installSdk()
    seed(...COOKIES)
    await cookieChoices()
    await choose('Reject')
    expect(container()).toBeNull()
    expect(hsq().at(-1)).toEqual(['doNotTrack'])
    for (const name of COOKIES) expect(jarNames(), name).not.toContain(name)
    expect(hrefWrites).toEqual([])
  })

  it('app_aChoiceFromAnotherTabIsFollowedOnVisibility', async () => {
    setConsent(true)
    await mount(handoffRecord())
    installSdk()
    setConsent(false)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(container()).toBeNull()
  })

  it('app_acceptAfterRejectReidentifiesInPlace', async () => {
    setConsent(true)
    await mount(handoffRecord())
    installSdk()
    await cookieChoices()
    await choose('Reject')
    const n = hsq().length
    await cookieChoices()
    await choose('Accept')
    expect(hsq().slice(n)).toEqual([['doNotTrack', { track: true }], ['identify', { email: EMAIL }], ['setPath', '/'], ['trackPageView']])
    expect(loaders()).toHaveLength(1)
    expect(hrefWrites).toEqual([])
  })

  it('app_theNoticeCarriesTheAppBody', async () => {
    await mount(handoffRecord())
    const { APP_NOTICE_BODY } = await import('./chat')
    const body = notice()!.querySelector('.cn-body')!.textContent
    expect(body).toBe(APP_NOTICE_BODY)
    expect(body).not.toContain('We use Google Analytics to see how people use this page')
  })

  it('app_aSuspendedMemberLosesTheChatAndSeesNoNotice', async () => {
    setConsent(true)
    await mount(handoffRecord())
    installSdk()
    seed(...COOKIES)
    await suspend()
    expect(screen.getByTestId('suspended-notice')).toBeTruthy()
    expect(container()).toBeNull()
    for (const name of COOKIES) expect(jarNames(), name).not.toContain(name)
    expect(hsq().some((e) => e[0] === 'doNotTrack')).toBe(false)
    expect(notice()).toBeNull()
  })

  it('app_aSuspendedMemberWithNoRecordSeesNoNotice', async () => {
    await mount(handoffRecord())
    await suspend()
    expect(screen.getByTestId('suspended-notice')).toBeTruthy()
    expect(notice()).toBeNull()
    expect(loaders()).toHaveLength(0)
  })

  it('app_anUnverifiedSessionCarryingAMeEmailPushesNoIdentify', async () => {
    setConsent(true)
    await mount(UNVERIFIED_WITH_ME)
    expect(loaders()).toHaveLength(1)
    expect(hsq().some((e) => e[0] === 'identify')).toBe(false)
  })

  it('app_aHiddenTabIgnoresAChoiceFromAnotherTab', async () => {
    setConsent(true)
    await mount(handoffRecord())
    installSdk()
    setConsent(false)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(container()).not.toBeNull()
  })

  it('app_cookieChoicesFollowsAChoiceMadeInAnotherTab', async () => {
    setConsent(true)
    await mount(handoffRecord())
    installSdk()
    setConsent(false)
    await cookieChoices()
    expect(container()).toBeNull()
    expect(within(notice()!).getByText('Analytics cookies are off.').classList.contains('cn-setting')).toBe(true)
  })

  it('app_visibilityWithNoRecordKeepsTheNotice', async () => {
    await mount(handoffRecord())
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(notice()).not.toBeNull()
    expect(loaders()).toHaveLength(0)
  })

  it('app_aSuspendedMemberIgnoresALaterGrantOnVisibility', async () => {
    setConsent(true)
    await mount(handoffRecord())
    installSdk()
    await suspend()
    expect(container()).toBeNull()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(screen.getByTestId('suspended-notice')).toBeTruthy()
    expect(notice()).toBeNull()
    expect(hsq().filter((e) => e[0] === 'trackPageView')).toHaveLength(0)
  })

  it('app_answeringTheReopenedNoticeClosesIt', async () => {
    setConsent(true)
    await mount(handoffRecord())
    await cookieChoices()
    await choose('Reject')
    expect(notice()).toBeNull()
    expect(storedConsent()?.analytics).toBe(false)
  })
  describe('route changes (CHAT-03-05)', () => {
    const goInvoices = () => click(screen.getByRole('button', { name: 'Invoices' }))

    it('app_aNavigationReportsOnePageViewWhileTheChatIsLoaded', async () => {
      setConsent(true)
      await mount(handoffRecord())
      const n = hsq().length
      await goInvoices()
      expect(hsq().slice(n)).toEqual([['setPath', '/invoices'], ['trackPageView']])
    })

    it('app_theFirstReportNamesTheRestoredPathNotTheBootUrl', async () => {
      setConsent(true)
      captureDestination('/audit')
      await mount(handoffRecord())
      expect(window.location.pathname).toBe('/audit')
      const paths = hsq().filter((e) => e[0] === 'setPath')
      expect(paths).toEqual([['setPath', '/audit']])
    })

    it('app_aBackReportsOnePageViewAndWritesNoHistory', async () => {
      setConsent(true)
      await mount(handoffRecord())
      await goInvoices()
      const n = hsq().length
      const push = vi.spyOn(window.history, 'pushState')
      const replace = vi.spyOn(window.history, 'replaceState')
      const popped = new Promise((r) => window.addEventListener('popstate', r, { once: true }))
      await act(async () => {
        window.history.back()
        await popped
      })
      expect(hsq().slice(n)).toEqual([['setPath', '/'], ['trackPageView']])
      expect(push).not.toHaveBeenCalled()
      expect(replace).not.toHaveBeenCalled()
    })

    it('app_noPageViewWithoutTheChat', async () => {
      setConsent(false)
      await mount(handoffRecord())
      await goInvoices()
      expect(w._hsq).toBeUndefined()
    })

    it('app_noPageViewAfterReject', async () => {
      setConsent(true)
      await mount(handoffRecord())
      installSdk()
      await cookieChoices()
      await choose('Reject')
      const n = hsq().length
      await goInvoices()
      expect(hsq().length).toBe(n)
    })
  })
})
