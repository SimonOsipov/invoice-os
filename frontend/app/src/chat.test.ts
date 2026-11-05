// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://app.ascomply.com/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION, readConsent } from '../../landing/src/consent'
import { chatScriptUrl } from '../../landing/src/hubspotChat'
import { SHARED_COOKIE_DOMAIN } from '../../landing/src/hubspot'
import { COOKIES, container, installSdk, jarNames, resetJar, seed, w } from '../../landing/src/hubspotChat.test.util'

type Chat = typeof import('./chat')

type GaWindow = Window & { dataLayer?: IArguments[]; gtag?: unknown }

const EMAIL = 'ada@example.com'
const UUID = '1b4e28ba-2fa1-11d2-883f-0016d3cca427'
const UUID2 = '6ba7b810-9dad-11d1-80b4-00c04fd430c8'
const ORIGIN = 'https://app.ascomply.com'
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
  delete (window as GaWindow).dataLayer
  delete (window as GaWindow).gtag
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
    expect((loaders()[0] as HTMLScriptElement).src).toBe(chatScriptUrl())
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

  it('with no baked id the seam loads no Google tag', async () => {
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

describe('Google tag', () => {
  const layer = () => [...((window as GaWindow).dataLayer ?? [])].map((a) => [...a] as unknown[])
  const pageViews = () => layer().filter((e) => e[0] === 'event' && e[1] === 'page_view')
  const tags = () => document.querySelectorAll('script[src*="googletagmanager"]')
  const stubId = () => vi.stubEnv('VITE_GA_MEASUREMENT_ID', 'G-TEST1234')

  it('bootAppAnalytics_storedAcceptSendsTheBoundedLocation', async () => {
    stubId()
    const { bootAppAnalytics } = await fresh()
    setConsent(true)
    window.history.replaceState(null, '', `/invoices/${UUID}?q=12345678-0001`)
    bootAppAnalytics()
    expect(tags().length).toBe(1)
    expect(layer().filter((e) => e[0] === 'config')).toEqual([['config', 'G-TEST1234', { page_location: `${ORIGIN}/invoices/:id` }]])
    const text = JSON.stringify(layer())
    for (const leak of ['1b4e28ba', '12345678', '?']) expect(text).not.toContain(leak)
  })

  it('bootAppAnalytics_noRecordRejectOrNoIdLoadsNothing', async () => {
    stubId()
    let chat = await fresh()
    chat.bootAppAnalytics()
    setConsent(false)
    chat = await fresh()
    chat.bootAppAnalytics()
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', '')
    setConsent(true)
    chat = await fresh()
    chat.bootAppAnalytics()
    expect(tags().length).toBe(0)
    expect((window as GaWindow).dataLayer).toBeUndefined()
  })

  it('chooseAppConsent_acceptLoadsOnceAndRejectRevokes', async () => {
    stubId()
    const { chooseAppConsent, reportAppPage } = await fresh()
    chooseAppConsent('accept', EMAIL)
    chooseAppConsent('accept', EMAIL)
    expect(tags().length).toBe(1)
    reportAppPage()
    expect(pageViews().length).toBe(1)
    chooseAppConsent('reject', EMAIL)
    reportAppPage()
    expect(pageViews().length).toBe(1)
  })

  it('syncAppConsent_followsARejectAndAReGrant', async () => {
    stubId()
    const { chooseAppConsent, syncAppConsent, reportAppPage } = await fresh()
    chooseAppConsent('accept', EMAIL)
    seed('_ga')
    setConsent(false)
    syncAppConsent(EMAIL)
    expect(jarNames()).not.toContain('_ga')
    reportAppPage()
    expect(pageViews().length).toBe(0)
    setConsent(true)
    syncAppConsent(EMAIL)
    reportAppPage()
    expect(pageViews().length).toBe(1)
  })

  it('endAppChat_aWorkspaceThatMountsAgainInThePageSendsPageViewsAgain', async () => {
    stubId()
    const { bootAppAnalytics, endAppChat, reportAppPage } = await fresh()
    setConsent(true)
    bootAppAnalytics()
    endAppChat()
    reportAppPage()
    expect(pageViews().length, 'control: endAppChat stops the sending').toBe(0)
    bootAppAnalytics()
    reportAppPage()
    expect(pageViews().length).toBe(1)
  })

  it('endAppChat_leavesTheConsentRecordAndDropsAHeldEvent', async () => {
    stubId()
    const { endAppChat, readConsent, reportValidatedInvoice, syncAppConsent } = await fresh()
    setConsent(true)
    reportValidatedInvoice({ status: 'validated' })
    const before = JSON.stringify(readConsent())
    endAppChat()
    expect(JSON.stringify(readConsent())).toBe(before)
    expect(document.cookie).toContain(CONSENT_STORAGE_KEY)
    reportValidatedInvoice({ status: 'validated' })
    syncAppConsent(EMAIL)
    expect(tags().length, 'control: the tag loaded').toBe(1)
    expect(layer().filter((e) => e[1] === 'invoice_validated')).toEqual([])
  })

  it('syncAppConsent_anotherTabsAcceptLoadsTheTagOnce', async () => {
    stubId()
    const { syncAppConsent } = await fresh()
    syncAppConsent(EMAIL)
    expect(tags().length, 'no record, no tag').toBe(0)
    setConsent(true)
    syncAppConsent(EMAIL)
    syncAppConsent(EMAIL)
    expect(tags().length).toBe(1)
    expect(layer().filter((e) => e[0] === 'config')).toHaveLength(1)
  })

  it('reportAppPage_sendsTheBoundedPatternPerCall', async () => {
    stubId()
    const { bootAppAnalytics, reportAppPage } = await fresh()
    const { reviewPath } = await import('./lib/route')
    setConsent(true)
    bootAppAnalytics()
    for (const path of ['/rules', '/invoices?q=12345678-0001', `/invoices/${UUID}`, reviewPath([UUID, UUID2]), '/no-such/12345678-0001']) {
      window.history.replaceState(null, '', path)
      reportAppPage()
    }
    expect(pageViews().map((e) => (e[2] as { page_location: string }).page_location)).toEqual(
      ['/rules', '/invoices', '/invoices/:id', '/imports/:ids/review', '/<unmatched>'].map((p) => ORIGIN + p),
    )
    const text = JSON.stringify(layer())
    for (const leak of ['1b4e28ba', '6ba7b810', '12345678', 'no-such', '?']) expect(text).not.toContain(leak)
    expect(w._hsq).toBeUndefined()
  })

  it('reportValidatedInvoice_sendsOnlyForValidated', async () => {
    stubId()
    const { chooseAppConsent, reportValidatedInvoice } = await fresh()
    chooseAppConsent('accept', EMAIL)
    for (const status of ['validated', 'draft', 'rejected']) {
      const inv = { status }
      expect(reportValidatedInvoice(inv)).toBe(inv)
    }
    expect(layer().filter((e) => e[1] === 'invoice_validated')).toEqual([['event', 'invoice_validated', {}]])
  })

  it('reportValidatedInvoice_heldBeforeAccept', async () => {
    stubId()
    const { chooseAppConsent, reportValidatedInvoice } = await fresh()
    reportValidatedInvoice({ status: 'validated' })
    expect((window as GaWindow).dataLayer).toBeUndefined()
    chooseAppConsent('accept', EMAIL)
    const names = layer().map((e) => e[0] === 'event' ? e[1] : e[0])
    expect(names.indexOf('invoice_validated')).toBeGreaterThan(names.indexOf('config'))
  })

  it('reportValidatedInvoice_aRejectEmptiesTheHoldAndAnAcceptSendsNone', async () => {
    stubId()
    const { chooseAppConsent, reportValidatedInvoice } = await fresh()
    reportValidatedInvoice({ status: 'validated' })
    chooseAppConsent('reject', EMAIL)
    chooseAppConsent('accept', EMAIL)
    expect(tags().length, 'control: the later Accept loads the tag').toBe(1)
    expect(layer().filter((e) => e[0] === 'config')).toHaveLength(1)
    expect(layer().filter((e) => e[1] === 'invoice_validated')).toEqual([])
  })

  it('reportValidatedInvoice_anotherTabsRejectEmptiesTheHold', async () => {
    stubId()
    const { syncAppConsent, reportValidatedInvoice } = await fresh()
    reportValidatedInvoice({ status: 'validated' })
    setConsent(false)
    syncAppConsent(EMAIL)
    setConsent(true)
    syncAppConsent(EMAIL)
    expect(tags().length, 'control: the later Accept loads the tag').toBe(1)
    expect(layer().filter((e) => e[1] === 'invoice_validated')).toEqual([])
  })

  it('reportValidatedInvoice_aHeldEventIsSentOnceAfterAStoredAcceptBoots', async () => {
    stubId()
    const { bootAppAnalytics, chooseAppConsent, reportValidatedInvoice } = await fresh()
    setConsent(true)
    reportValidatedInvoice({ status: 'validated' })
    expect((window as GaWindow).dataLayer, 'nothing before the tag').toBeUndefined()
    bootAppAnalytics()
    chooseAppConsent('accept', EMAIL)
    bootAppAnalytics()
    expect(layer().map((e) => e[0])).toEqual(['js', 'config', 'event'])
    expect(layer()[2]).toEqual(['event', 'invoice_validated', {}])
  })

  it('reportValidatedInvoice_aStoredRejectDropsTheEvent', async () => {
    stubId()
    const { chooseAppConsent, reportValidatedInvoice } = await fresh()
    setConsent(false)
    reportValidatedInvoice({ status: 'validated' })
    chooseAppConsent('accept', EMAIL)
    expect(tags().length, 'control: the Accept loads the tag').toBe(1)
    expect(layer().filter((e) => e[1] === 'invoice_validated')).toEqual([])
  })

  it('everyEventCarriesNoUserIdNoSetCallAndNoFragmentOrQuery', async () => {
    stubId()
    const { chooseAppConsent, reportAppPage, reportValidatedInvoice } = await fresh()
    window.history.replaceState(null, '', `/invoices/${UUID}?q=secret-term#token=abc123`)
    reportValidatedInvoice({ status: 'validated' })
    chooseAppConsent('accept', EMAIL)
    reportValidatedInvoice({ status: 'validated' })
    reportAppPage()
    expect(layer().length, 'control: the flows pushed entries').toBeGreaterThanOrEqual(5)
    expect(layer().some((e) => e[0] === 'set')).toBe(false)
    const text = JSON.stringify(layer())
    for (const leak of ['user_id', 'secret-term', 'abc123', '#', '?', UUID, EMAIL, 'ada']) expect(text, leak).not.toContain(leak)
    for (const e of layer().filter((x) => x[0] === 'event' && x[1] !== 'page_view')) expect(e[2]).toEqual({})
  })
})
