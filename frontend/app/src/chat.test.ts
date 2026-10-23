// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://app.ascomply.com/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION, readConsent } from '../../landing/src/consent'
import { CHAT_SCRIPT_URL } from '../../landing/src/hubspotChat'
import { SHARED_COOKIE_DOMAIN } from '../../landing/src/hubspot'
import { COOKIES, container, installSdk, jarNames, resetJar, seed, w } from '../../landing/src/hubspotChat.test.util'

type Chat = typeof import('./chat')

const EMAIL = 'ada@example.com'
const IDENTIFY = ['identify', { email: EMAIL }]
const LIFT = ['doNotTrack', { track: true }]

function setConsent(analytics: boolean) {
  const rec = { analytics, ts: new Date().toISOString(), v: CONSENT_VERSION }
  document.cookie = `${CONSENT_STORAGE_KEY}=${encodeURIComponent(JSON.stringify(rec))}; Domain=${SHARED_COOKIE_DOMAIN}; Path=/`
}

function clearConsent() {
  document.cookie = `${CONSENT_STORAGE_KEY}=; Max-Age=0; Domain=${SHARED_COOKIE_DOMAIN}; Path=/`
}

const loaders = () => document.head.querySelectorAll('script#hs-script-loader')
const hsq = () => w._hsq as unknown[][]

async function fresh(): Promise<Chat> {
  vi.resetModules()
  return import('./chat')
}

beforeEach(() => {
  document.head.innerHTML = ''
  document.body.innerHTML = ''
  delete w._hsq
  delete w.HubSpotConversations
  delete w.hsConversationsSettings
  delete w.hsConversationsOnReady
  window.history.replaceState(null, '', '/')
  resetJar()
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('bootAppChat', () => {
  it('boot on the app host with Accept injects one loader', async () => {
    const { bootAppChat } = await fresh()
    setConsent(true)
    expect(bootAppChat(EMAIL)).toBe(true)
    expect(loaders().length).toBe(1)
    expect((loaders()[0] as HTMLScriptElement).src).toBe(CHAT_SCRIPT_URL)
  })

  it('identify, setPath and page view are queued before the loader', async () => {
    const { bootAppChat } = await fresh()
    setConsent(true)
    window.history.replaceState(null, '', '/invoices?q=Acme')
    let atAppend: unknown[] | undefined
    const real = document.head.appendChild.bind(document.head)
    vi.spyOn(document.head, 'appendChild').mockImplementation(((node: Node) => {
      atAppend = structuredClone(w._hsq)
      return real(node)
    }) as typeof document.head.appendChild)
    bootAppChat(EMAIL)
    expect(atAppend).toEqual([IDENTIFY, ['setPath', '/invoices'], ['trackPageView']])
  })

  it('a null or blank email pushes no identify', async () => {
    for (const email of [null, '  ']) {
      delete w._hsq
      document.head.innerHTML = ''
      const { bootAppChat } = await fresh()
      setConsent(true)
      bootAppChat(email)
      expect(w._hsq).toEqual([['setPath', '/'], ['trackPageView']])
      expect(loaders().length).toBe(1)
    }
  })

  it('a padded email is trimmed before identify', async () => {
    const { bootAppChat } = await fresh()
    setConsent(true)
    bootAppChat(`  ${EMAIL} `)
    expect(hsq()[0]).toEqual(IDENTIFY)
  })

  it('a second boot queues nothing twice', async () => {
    const { bootAppChat } = await fresh()
    setConsent(true)
    bootAppChat(EMAIL)
    bootAppChat(EMAIL)
    expect(loaders().length).toBe(1)
    expect(hsq().filter((e) => e[0] === 'identify').length).toBe(1)
  })

  it('a leftover opt-out cookie is expired before the first push', async () => {
    const { bootAppChat } = await fresh()
    setConsent(true)
    seed('__hs_do_not_track')
    bootAppChat(EMAIL)
    expect(jarNames()).not.toContain('__hs_do_not_track')
    expect(hsq()[0][0]).toBe('identify')
    expect(hsq()).not.toContainEqual(LIFT)
  })

  it('no record or a denied record loads nothing', async () => {
    let chat = await fresh()
    expect(chat.bootAppChat(EMAIL)).toBe(false)
    expect(loaders().length).toBe(0)
    expect(w._hsq).toBeUndefined()

    setConsent(false)
    seed('__hstc')
    chat = await fresh()
    expect(chat.bootAppChat(EMAIL)).toBe(false)
    expect(jarNames()).not.toContain('__hstc')
    expect(loaders().length).toBe(0)
    expect(w._hsq).toBeUndefined()
  })
})

describe('chooseAppConsent', () => {
  it('Accept, Reject, Accept in one page re-identifies after the opt-out lift', async () => {
    const { chooseAppConsent } = await fresh()
    const { chatIsLoaded } = await import('../../landing/src/hubspotChat')
    chooseAppConsent('accept', EMAIL)
    const sdk = installSdk()
    chooseAppConsent('reject', EMAIL)
    const n = hsq().length
    chooseAppConsent('accept', EMAIL)
    expect(hsq().slice(n)).toEqual([LIFT, IDENTIFY, ['setPath', '/'], ['trackPageView']])
    expect(loaders().length).toBe(1)
    expect(sdk.widget.load).toHaveBeenCalled()
    expect(chatIsLoaded()).toBe(true)
  })

  it('a re-Accept with a blank email pushes no identify', async () => {
    const { chooseAppConsent } = await fresh()
    chooseAppConsent('accept', null)
    installSdk()
    chooseAppConsent('reject', null)
    const n = hsq().length
    chooseAppConsent('accept', null)
    expect(hsq().slice(n)).toEqual([LIFT, ['setPath', '/'], ['trackPageView']])
  })

  it('the seam loads no Google tag', async () => {
    const { chooseAppConsent } = await fresh()
    chooseAppConsent('accept', EMAIL)
    expect(document.querySelectorAll('script[src*="googletagmanager"]').length).toBe(0)
    expect((window as Window & { gtag?: unknown }).gtag).toBeUndefined()
    expect((window as Window & { dataLayer?: unknown }).dataLayer).toBeUndefined()
  })

  it('a choice in the app writes the shared cookie', async () => {
    const { chooseAppConsent } = await fresh()
    const record = chooseAppConsent('accept', EMAIL)
    expect(readConsent(undefined, document, 'www.ascomply.com')).toEqual(record)
  })

  it('Reject after Accept removes the chat in place', async () => {
    const { chooseAppConsent } = await fresh()
    chooseAppConsent('accept', EMAIL)
    installSdk()
    seed(...COOKIES, '_ga')
    const record = chooseAppConsent('reject', EMAIL)
    expect(container()).toBeNull()
    expect(hsq()[hsq().length - 1]).toEqual(['doNotTrack'])
    for (const name of [...COOKIES, '_ga']) expect(jarNames()).not.toContain(name)
    expect(record.analytics).toBe(false)
  })

  it('a first Reject loads nothing and pushes nothing', async () => {
    const { chooseAppConsent } = await fresh()
    seed(...COOKIES, '_ga')
    chooseAppConsent('reject', EMAIL)
    expect(loaders().length).toBe(0)
    expect(w._hsq).toBeUndefined()
    for (const name of [...COOKIES, '_ga']) expect(jarNames()).not.toContain(name)
  })
})

describe('syncAppConsent', () => {
  it('a re-grant from another tab re-identifies', async () => {
    const { bootAppChat, syncAppConsent } = await fresh()
    setConsent(true)
    bootAppChat(EMAIL)
    installSdk()
    setConsent(false)
    syncAppConsent(EMAIL)
    const n = hsq().length
    setConsent(true)
    syncAppConsent(EMAIL)
    expect(hsq().slice(n)).toEqual([LIFT, IDENTIFY, ['setPath', '/'], ['trackPageView']])
    expect(loaders().length).toBe(1)
  })

  it('sync follows another tab', async () => {
    const { syncAppConsent } = await fresh()
    setConsent(true)
    syncAppConsent(EMAIL)
    expect(loaders().length).toBe(1)
    installSdk()
    setConsent(false)
    syncAppConsent(EMAIL)
    expect(container()).toBeNull()
    expect(hsq()).toContainEqual(['doNotTrack'])
    clearConsent()
    const n = hsq().length
    expect(syncAppConsent(EMAIL)).toBeNull()
    expect(hsq().length).toBe(n)
  })
})

describe('syncAppConsent with no record', () => {
  it('sync with no record leaves a loaded chat in place', async () => {
    const { bootAppChat, syncAppConsent } = await fresh()
    setConsent(true)
    bootAppChat(EMAIL)
    installSdk()
    clearConsent()
    const n = hsq().length
    expect(syncAppConsent(EMAIL)).toBeNull()
    expect(container()).not.toBeNull()
    expect(hsq().length).toBe(n)
  })
})

describe('reportAppPage and endAppChat', () => {
  it('reportAppPage reports only while the chat is loaded', async () => {
    const { bootAppChat, chooseAppConsent, reportAppPage } = await fresh()
    reportAppPage()
    expect(w._hsq).toBeUndefined()
    setConsent(true)
    bootAppChat(EMAIL)
    window.history.replaceState(null, '', `/audit?invoice=${EMAIL}`)
    reportAppPage()
    expect(hsq().slice(-2)).toEqual([['setPath', '/audit'], ['trackPageView']])
    chooseAppConsent('reject', EMAIL)
    const n = hsq().length
    reportAppPage()
    expect(hsq().length).toBe(n)
  })

  it('endAppChat removes the widget and the cookies and pushes no doNotTrack', async () => {
    const { bootAppChat, endAppChat } = await fresh()
    setConsent(true)
    bootAppChat(EMAIL)
    installSdk()
    seed(...COOKIES)
    endAppChat()
    expect(container()).toBeNull()
    for (const name of COOKIES) expect(jarNames()).not.toContain(name)
    expect(hsq().some((e) => e[0] === 'doNotTrack')).toBe(false)
    expect(readConsent()?.analytics).toBe(true)
  })
})

describe('privacy', () => {
  it('the email is logged nowhere', async () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m).mockImplementation(() => {}))
    const { bootAppChat, chooseAppConsent, syncAppConsent, endAppChat } = await fresh()
    setConsent(true)
    bootAppChat(EMAIL)
    installSdk()
    chooseAppConsent('reject', EMAIL)
    chooseAppConsent('accept', EMAIL)
    syncAppConsent(EMAIL)
    endAppChat()
    for (const spy of spies) for (const call of spy.mock.calls) expect(JSON.stringify(call)).not.toContain(EMAIL)
  })

  it("APP_NOTICE_BODY is the user's words, byte for byte", async () => {
    const { APP_NOTICE_BODY } = await fresh()
    expect(APP_NOTICE_BODY).toBe(
      'We use Google Analytics to see how people use the product. Accept also turns on our HubSpot live chat, which sets its own cookies and tells HubSpot the email address of your account. No advertising, no data sold to anyone. Your choice applies to ascomply.com, the Feature Library, the Blog and this product.',
    )
  })

  it('APP_NOTICE_BODY drops the old claim', async () => {
    const { APP_NOTICE_BODY } = await fresh()
    expect(APP_NOTICE_BODY).not.toContain('runs no Google Analytics')
    expect(APP_NOTICE_BODY).not.toContain('this page')
  })

  it('app_noticeBodyIsTheLandingConstant', async () => {
    const { APP_NOTICE_BODY } = await fresh()
    const landing = await import('../../landing/src/components/CookieNotice')
    expect(APP_NOTICE_BODY).toBe(landing.APP_NOTICE_BODY)
  })

  it('privacyHref points at the landing privacy page', async () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const { privacyHref } = await fresh()
    expect(privacyHref()).toBe('https://landing.example/privacy')
    vi.stubEnv('VITE_LANDING_URL', '')
    expect(privacyHref()).toBe('https://www.ascomply.com/privacy')
  })
})
