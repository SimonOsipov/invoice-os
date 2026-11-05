// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }
const w = window as TestWindow
const ID = 'G-E409H76XYY'

const entries = () => (w.dataLayer ?? []).map((a) => Array.from(a))
const names = (n: string) => entries().filter((e) => e[0] === 'event' && e[1] === n)

async function load() {
  const analytics = await import('./analytics')
  const actions = await import('./consentActions')
  return { ...analytics, ...actions }
}

beforeEach(() => {
  document.head.innerHTML = ''
  delete w.dataLayer
  delete w.gtag
  const mem = new Map<string, string>()
  Object.defineProperty(globalThis, 'localStorage', {
    value: {
      getItem: (k: string) => mem.get(k) ?? null,
      setItem: (k: string, v: string) => void mem.set(k, v),
      removeItem: (k: string) => void mem.delete(k),
    },
    configurable: true,
    writable: true,
  })
  document.cookie.split(';').forEach((c) => {
    const k = c.split('=')[0].trim()
    if (k) document.cookie = `${k}=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/`
  })
  vi.resetModules()
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('held events', () => {
  it('hold_anEventBeforeAcceptIsSentAfterConfig', async () => {
    const m = await load()
    m.trackSignUp()
    m.trackInvoiceValidated()
    m.applyChoice('accept')
    expect(entries().map((e) => e[0])).toEqual(['js', 'config', 'event', 'event'])
    expect(entries().slice(2).map((e) => e[1])).toEqual(['sign_up', 'invoice_validated'])
    expect(names('sign_up')).toHaveLength(1)
    expect(names('invoice_validated')).toHaveLength(1)
  })

  it('hold_beforeAcceptInjectsNothing', async () => {
    const m = await load()
    m.trackSignUp()
    expect(w.dataLayer).toBeUndefined()
    expect(document.head.querySelector('script[src*="googletagmanager"]')).toBeNull()
  })

  it('hold_rejectEmptiesTheQueue', async () => {
    const m = await load()
    m.trackSignUp()
    m.applyChoice('reject')
    m.applyChoice('accept')
    expect(names('sign_up')).toHaveLength(0)
  })

  it('hold_aStoredRejectDropsTheEvent', async () => {
    const m = await load()
    m.applyChoice('reject')
    m.setAnalyticsRevoked(false)
    m.trackInvoiceValidated()
    m.applyChoice('accept')
    expect(names('invoice_validated')).toHaveLength(0)
  })

  it('hold_aLoadedTagSendsAtOnce', async () => {
    const m = await load()
    m.applyChoice('accept')
    m.trackSignUp()
    expect(entries().at(-1)).toEqual(['event', 'sign_up', {}])
    expect(names('sign_up')).toHaveLength(1)
  })

  it('hold_revokedDropsAndNeverReplays', async () => {
    const m = await load()
    m.applyChoice('accept')
    m.applyChoice('reject')
    const before = entries().length
    m.trackSignUp()
    expect(entries()).toHaveLength(before)
    m.applyChoice('accept')
    expect(names('sign_up')).toHaveLength(0)
  })

  it('hold_aNonListedHostLoadsNoTagAndSendsNothing', async () => {
    const m = await load()
    m.trackSignUp()
    m.applyChoice('accept', { hostname: 'app-pr-1-x.a.run.app' })
    expect(document.head.querySelector('script[src*="googletagmanager"]')).toBeNull()
    expect(names('sign_up')).toHaveLength(0)
  })
})

describe('ensureTag config and app page view', () => {
  const GRANTED = { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: 1 }

  it('ensureTag_passesTheConfigObjectOnlyWhenGiven', async () => {
    const { CONSENT_VERSION } = await import('./consent')
    const rec = { ...GRANTED, v: CONSENT_VERSION }
    let m = await import('./analytics')
    m.ensureTag('www.ascomply.com', rec, undefined, { page_location: 'https://www.ascomply.com/x' })
    const cfg = entries().find((e) => e[0] === 'config')!
    expect(cfg).toHaveLength(3)
    expect(cfg[2]).toEqual({ page_location: 'https://www.ascomply.com/x' })

    delete w.dataLayer
    vi.resetModules()
    m = await import('./analytics')
    m.ensureTag('www.ascomply.com', rec)
    expect(entries().find((e) => e[0] === 'config')).toHaveLength(2)
  })

  it('appPageView_sendsTheGivenLocation', async () => {
    const m = await load()
    m.applyChoice('accept')
    m.trackAppPageView('https://app.ascomply.com/invoices/:id')
    expect(entries().at(-1)).toEqual(['event', 'page_view', { page_location: 'https://app.ascomply.com/invoices/:id' }])
  })

  it('appPageView_sendsNothingBeforeTheTag', async () => {
    const m = await load()
    m.trackAppPageView('https://app.ascomply.com/invoices')
    expect(w.dataLayer).toBeUndefined()
  })

  it('noUserIdAndNoSetCall', async () => {
    const m = await load()
    m.trackSignUp()
    m.applyChoice('accept')
    m.trackInvoiceValidated()
    m.trackAppPageView('https://app.ascomply.com/invoices')
    expect(entries().some((e) => e[0] === 'set')).toBe(false)
    expect(JSON.stringify(entries())).not.toContain('user_id')
  })
})
