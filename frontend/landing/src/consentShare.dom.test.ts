// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
import { JSDOM, CookieJar } from 'jsdom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  CONSENT_STORAGE_KEY,
  CONSENT_VERSION,
  readConsent,
  readConsentCookie,
  sharedConsentDomain,
  writeConsent,
  type ConsentJar,
  type ConsentRecord,
  type ConsentStore,
} from './consent'
import { applyChoice } from './consentActions'
import { LIBRARY_HOSTNAMES, PRODUCTION_HOSTNAMES, SHARED_COOKIE_DOMAIN } from './hubspot'

const WWW_URL = 'https://www.ascomply.com/'
const LIB_URL = 'https://library.ascomply.com/'
const WWW = 'www.ascomply.com'
const LIB = 'library.ascomply.com'
const KEY = CONSENT_STORAGE_KEY

type MemStore = Required<ConsentStore> & { map: Map<string, string> }

function memStore(): MemStore {
  const map = new Map<string, string>()
  return {
    map,
    getItem: (k) => (map.has(k) ? map.get(k)! : null),
    setItem: (k, v) => void map.set(k, String(v)),
    removeItem: (k) => void map.delete(k),
  }
}

const rec = (analytics: boolean, ts: string): ConsentRecord => ({ analytics, ts, v: CONSENT_VERSION })
const seed = (store: MemStore, r: object) => store.map.set(KEY, JSON.stringify(r))
const enc = (r: object) => encodeURIComponent(JSON.stringify(r))

function win(url: string, cookieJar: CookieJar): Document {
  return new JSDOM('', { url, cookieJar }).window.document
}

function setup() {
  const cookieJar = new CookieJar()
  return {
    cookieJar,
    wwwDoc: win(WWW_URL, cookieJar),
    libDoc: win(LIB_URL, cookieJar),
    storeW: memStore(),
    storeL: memStore(),
  }
}

const jarCookies = (j: CookieJar, url: string) => j.getCookiesSync(url).filter((c) => c.key === KEY)

describe('shared consent cookie', () => {
  it('CS-01 Accept on www is read on the Library', () => {
    const { cookieJar, wwwDoc, libDoc, storeW, storeL } = setup()
    const now = new Date('2026-03-01T00:00:00.000Z')
    const written = writeConsent(true, storeW, now, wwwDoc, WWW)

    const cookies = jarCookies(cookieJar, LIB_URL)
    expect(cookies).toHaveLength(1)
    expect(cookies[0]).toMatchObject({
      domain: 'ascomply.com',
      path: '/',
      secure: true,
      sameSite: 'lax',
      maxAge: 34560000,
    })
    expect(readConsent(storeL, libDoc, LIB)).toEqual(written)
    expect(storeW.map.has(KEY)).toBe(false)
  })

  it('CS-02 Reject on the Library is read on www', () => {
    const { wwwDoc, libDoc, storeW, storeL } = setup()
    writeConsent(true, storeW, new Date('2026-03-01T00:00:00.000Z'), wwwDoc, WWW)
    const later = new Date('2026-03-02T00:00:00.000Z')
    writeConsent(false, storeL, later, libDoc, LIB)

    expect(readConsent(storeW, wwwDoc, WWW)).toEqual(rec(false, later.toISOString()))
  })

  it('CS-03 a fork and localhost keep their own store', () => {
    const { cookieJar, wwwDoc, storeW } = setup()
    writeConsent(true, storeW, new Date('2026-03-01T00:00:00.000Z'), wwwDoc, WWW)
    const wwwBefore = jarCookies(cookieJar, WWW_URL)[0].value

    const forkUrl = 'https://pr-12-library.up.railway.app/'
    const forkDoc = win(forkUrl, cookieJar)
    const localDoc = win('http://localhost/', cookieJar)
    const storeF = memStore()
    expect(readConsent(memStore(), forkDoc, 'pr-12-library.up.railway.app')).toBeNull()
    expect(readConsent(memStore(), localDoc, 'localhost')).toBeNull()

    writeConsent(true, storeF, new Date('2026-03-02T00:00:00.000Z'), forkDoc, 'pr-12-library.up.railway.app')
    expect(storeF.map.has(KEY)).toBe(true)
    expect(jarCookies(cookieJar, forkUrl)).toEqual([])
    expect(jarCookies(cookieJar, WWW_URL)[0].value).toBe(wwwBefore)
  })

  it('CS-04 only an exact production host shares', () => {
    for (const h of [WWW, ' WWW.ASCOMPLY.COM ', LIB]) expect(sharedConsentDomain(h)).toBe('ascomply.com')
    for (const h of ['ascomply.com', 'app.ascomply.com', 'www.ascomply.com.evil.example', 'library.ascomply.com.', 'localhost', '']) {
      expect(sharedConsentDomain(h)).toBeNull()
    }
  })

  it('CS-05 a local record moves into the cookie', () => {
    const { cookieJar, wwwDoc, storeW } = setup()
    const local = rec(true, '2026-01-01T00:00:00.000Z')
    seed(storeW, local)

    expect(readConsent(storeW, wwwDoc, WWW)).toEqual(local)
    expect(readConsentCookie(win(LIB_URL, cookieJar))).toEqual(local)
    expect(storeW.map.has(KEY)).toBe(false)
  })

  it('CS-06 the later ts wins either way', () => {
    const a = setup()
    seed(a.storeW, rec(true, '2026-01-01T00:00:00.000Z'))
    a.wwwDoc.cookie = `${KEY}=${enc(rec(false, '2026-02-01T00:00:00.000Z'))}; Domain=ascomply.com; Path=/; Secure`
    expect(readConsent(a.storeW, a.wwwDoc, WWW)).toEqual(rec(false, '2026-02-01T00:00:00.000Z'))
    expect(readConsentCookie(a.wwwDoc)).toEqual(rec(false, '2026-02-01T00:00:00.000Z'))
    expect(a.storeW.map.has(KEY)).toBe(false)

    const b = setup()
    seed(b.storeW, rec(false, '2026-02-01T00:00:00.000Z'))
    b.wwwDoc.cookie = `${KEY}=${enc(rec(true, '2026-01-01T00:00:00.000Z'))}; Domain=ascomply.com; Path=/; Secure`
    expect(readConsent(b.storeW, b.wwwDoc, WWW)).toEqual(rec(false, '2026-02-01T00:00:00.000Z'))
    expect(readConsentCookie(b.wwwDoc)).toEqual(rec(false, '2026-02-01T00:00:00.000Z'))
    expect(b.storeW.map.has(KEY)).toBe(false)
  })

  it('CS-07 a tie and a missing ts keep the cookie', () => {
    const tie = setup()
    const ts = '2026-01-01T00:00:00.000Z'
    seed(tie.storeW, rec(true, ts))
    tie.wwwDoc.cookie = `${KEY}=${enc(rec(false, ts))}; Domain=ascomply.com; Path=/; Secure`
    expect(readConsent(tie.storeW, tie.wwwDoc, WWW)!.analytics).toBe(false)

    const noTs = setup()
    seed(noTs.storeW, { analytics: true, v: CONSENT_VERSION })
    noTs.wwwDoc.cookie = `${KEY}=${enc(rec(true, ts))}; Domain=ascomply.com; Path=/; Secure`
    expect(readConsent(noTs.storeW, noTs.wwwDoc, WWW)).toEqual(rec(true, ts))

    const noCookie = setup()
    seed(noCookie.storeW, { analytics: false, v: CONSENT_VERSION })
    expect(readConsent(noCookie.storeW, noCookie.wwwDoc, WWW)).toEqual(rec(false, ''))
    expect(readConsentCookie(noCookie.wwwDoc)).toEqual(rec(false, ''))
    expect(noCookie.storeW.map.has(KEY)).toBe(false)
  })

  it('CS-08 two old origins settle on the most recent', () => {
    const { wwwDoc, libDoc, storeW, storeL } = setup()
    seed(storeW, rec(true, '2026-01-01T00:00:00.000Z'))
    seed(storeL, rec(false, '2026-02-01T00:00:00.000Z'))

    expect(readConsent(storeW, wwwDoc, WWW)!.analytics).toBe(true)
    expect(readConsent(storeL, libDoc, LIB)!.analytics).toBe(false)
    expect(readConsent(storeW, wwwDoc, WWW)!.analytics).toBe(false)
    expect(storeW.map.has(KEY)).toBe(false)
    expect(storeL.map.has(KEY)).toBe(false)
  })

  it('CS-09 a blocked or throwing cookie degrades to the fallback', () => {
    const blocked: ConsentJar = {
      get cookie() {
        return ''
      },
      set cookie(_v: string) {},
    }
    const store = memStore()
    const local = rec(true, '2026-01-01T00:00:00.000Z')
    seed(store, local)
    expect(readConsent(store, blocked, WWW)).toEqual(local)
    expect(store.map.has(KEY)).toBe(true)

    expect(writeConsent(false, store, new Date('2026-02-01T00:00:00.000Z'), blocked, WWW).analytics).toBe(false)
    expect(store.map.has(KEY)).toBe(false)
    expect(readConsent(store, blocked, WWW)).toBeNull()

    const throwing: ConsentJar = {
      get cookie(): string {
        throw new Error('blocked')
      },
      set cookie(_v: string) {
        throw new Error('blocked')
      },
    }
    const other = memStore()
    expect(() => writeConsent(true, other, undefined, throwing, WWW)).not.toThrow()
    expect(() => readConsent(other, throwing, WWW)).not.toThrow()
    seed(other, local)
    expect(readConsent(other, throwing, WWW)).toEqual(local)
    expect(readConsent(memStore(), throwing, WWW)).toBeNull()

    expect(readConsent(null, null, WWW)).toBeNull()
    expect(writeConsent(true, null, undefined, null, WWW).analytics).toBe(true)
  })

  it('CS-10 nothing is written before an answer', () => {
    const { wwwDoc } = setup()
    const set = vi.fn()
    const spied: ConsentJar = {
      get cookie() {
        return wwwDoc.cookie
      },
      set cookie(v: string) {
        set(v)
        wwwDoc.cookie = v
      },
    }
    const store = memStore()
    expect(readConsent(store, spied, WWW)).toBeNull()
    expect(set).not.toHaveBeenCalled()
    expect(store.map.size).toBe(0)
  })

  it('CS-11 a read that finds the cookie current writes nothing', () => {
    const { wwwDoc } = setup()
    const r = rec(true, '2026-01-01T00:00:00.000Z')
    wwwDoc.cookie = `${KEY}=${enc(r)}; Domain=ascomply.com; Path=/; Secure`
    const set = vi.fn()
    const spied: ConsentJar = {
      get cookie() {
        return wwwDoc.cookie
      },
      set cookie(v: string) {
        set(v)
        wwwDoc.cookie = v
      },
    }
    const store = memStore()
    expect(readConsent(store, spied, WWW)).toEqual(r)
    expect(readConsent(store, spied, WWW)).toEqual(r)
    expect(set.mock.calls.filter(([v]) => String(v).startsWith(`${KEY}=`))).toHaveLength(0)

    seed(store, rec(false, '2026-02-01T00:00:00.000Z'))
    readConsent(store, spied, WWW)
    const writes = set.mock.calls.map(([v]) => String(v)).filter((v) => v.startsWith(`${KEY}=`))
    expect(writes).toHaveLength(1)
    expect(writes[0]).toContain('Max-Age=34560000')
  })

  it('CS-12 a malformed cookie is no record', () => {
    for (const value of ['%E0%A4%A', '%7Bbad', enc({ analytics: true, v: 2 })]) {
      const { wwwDoc, storeW } = setup()
      wwwDoc.cookie = `${KEY}=${value}; Domain=ascomply.com; Path=/; Secure`
      expect(readConsentCookie(wwwDoc)).toBeNull()
      const local = rec(true, '2026-01-01T00:00:00.000Z')
      seed(storeW, local)
      expect(readConsent(storeW, wwwDoc, WWW)).toEqual(local)
    }
  })

  it('CS-13 both production hosts sit under the shared domain', () => {
    for (const h of [...PRODUCTION_HOSTNAMES, ...LIBRARY_HOSTNAMES]) expect(h.endsWith(`.${SHARED_COOKIE_DOMAIN}`)).toBe(true)
    expect(SHARED_COOKIE_DOMAIN.split('.')).toHaveLength(2)
  })

  it('CS-14 applyChoice on www writes the cookie, not the store', () => {
    const map = new Map<string, string>()
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      value: {
        getItem: (k: string) => map.get(k) ?? null,
        setItem: (k: string, v: string) => void map.set(k, v),
        removeItem: (k: string) => void map.delete(k),
      },
    })
    applyChoice('accept', { hostname: WWW })
    expect(readConsentCookie(document)!.analytics).toBe(true)
    expect(map.has(KEY)).toBe(false)
  })

  it('QA-01 a non-production host ignores a cookie it can see and never migrates into it', () => {
    const { cookieJar } = setup()
    const forkUrl = 'https://pr-12-library.up.railway.app/'
    const forkDoc = win(forkUrl, cookieJar)
    forkDoc.cookie = `${KEY}=${enc(rec(false, '2026-05-01T00:00:00.000Z'))}; Path=/; Secure`
    expect(jarCookies(cookieJar, forkUrl)).toHaveLength(1)
    const store = memStore()
    const local = rec(true, '2026-01-01T00:00:00.000Z')
    seed(store, local)
    const before = forkDoc.cookie

    expect(readConsent(store, forkDoc, 'pr-12-library.up.railway.app')).toEqual(local)
    expect(store.map.has(KEY)).toBe(true)
    expect(forkDoc.cookie).toBe(before)
  })

  it('QA-02 a wrong-shape or old-version cookie is no record', () => {
    const bad = ['{"analytics":"yes","v":1}', '{"analytics":true,"v":0}', '{"analytics":true}', 'null', '[]', '1', '""', '{}']
    for (const raw of bad) {
      const { wwwDoc, storeW } = setup()
      wwwDoc.cookie = `${KEY}=${encodeURIComponent(raw)}; Domain=ascomply.com; Path=/; Secure`
      expect(wwwDoc.cookie).toContain(KEY)
      expect(readConsentCookie(wwwDoc)).toBeNull()
      expect(readConsent(storeW, wwwDoc, WWW)).toBeNull()
    }
  })

  it('QA-03 only the exact asc_consent name is read', () => {
    const { wwwDoc } = setup()
    const r = rec(true, '2026-01-01T00:00:00.000Z')
    wwwDoc.cookie = `xasc_consent=${enc(rec(false, '2026-09-01T00:00:00.000Z'))}; Domain=ascomply.com; Path=/; Secure`
    wwwDoc.cookie = `asc_consent_x=${enc(rec(false, '2026-09-01T00:00:00.000Z'))}; Domain=ascomply.com; Path=/; Secure`
    expect(readConsentCookie(wwwDoc)).toBeNull()
    wwwDoc.cookie = `${KEY}=${enc(r)}; Domain=ascomply.com; Path=/; Secure`
    expect(readConsentCookie(wwwDoc)).toEqual(r)
  })

  it('QA-04 a cookie record with no ts loses to a dated local record', () => {
    const { wwwDoc, storeW } = setup()
    wwwDoc.cookie = `${KEY}=${enc({ analytics: false, v: CONSENT_VERSION })}; Domain=ascomply.com; Path=/; Secure`
    const local = rec(true, '2026-01-01T00:00:00.000Z')
    seed(storeW, local)
    expect(readConsent(storeW, wwwDoc, WWW)).toEqual(local)
    expect(readConsentCookie(wwwDoc)).toEqual(local)
    expect(storeW.map.has(KEY)).toBe(false)
  })

  it('QA-05 applyChoice with a non-production hostname keeps the record in the store', () => {
    const map = new Map<string, string>()
    const store = {
      getItem: (k: string) => map.get(k) ?? null,
      setItem: (k: string, v: string) => void map.set(k, v),
      removeItem: (k: string) => void map.delete(k),
    }
    applyChoice('accept', { hostname: 'localhost', store })
    expect(map.has(KEY)).toBe(true)
    expect(readConsentCookie(document)).toBeNull()
  })
})

describe('open-tab sync', () => {
  const SYNC_ID = 'G-E409H76XYY'
  const SYNC_TAG = 'script[src^="https://www.googletagmanager.com/"]'
  type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }
  const setCookie = (analytics: boolean) =>
    (document.cookie = `${KEY}=${enc(rec(analytics, '2026-03-01T00:00:00.000Z'))}; Domain=ascomply.com; Path=/; Secure`)

  beforeEach(() => {
    document.head.innerHTML = ''
    const w = window as TestWindow
    delete w.dataLayer
    delete w.gtag
    vi.resetModules()
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', SYNC_ID)
  })

  afterEach(() => {
    vi.unstubAllEnvs()
    vi.restoreAllMocks()
    delete (document as { cookie?: string }).cookie
  })

  function spyJar(): string[] {
    const desc = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie')!
    const writes: string[] = []
    Object.defineProperty(document, 'cookie', {
      configurable: true,
      get: () => desc.get!.call(document),
      set: (v: string) => {
        writes.push(v)
        desc.set!.call(document, v)
      },
    })
    return writes
  }
  const layer = () => ((window as TestWindow).dataLayer ?? []).map((a) => Array.from(a))

  it.each([
    [LIB, [LIB, `.${LIB}`, 'ascomply.com', '.ascomply.com']],
    [WWW, [WWW, `.${WWW}`, 'ascomply.com', '.ascomply.com']],
  ])('SY-04 a synced Reject on %s expires every _ga name on host-only and each domain form', async (host, domains) => {
    const { syncConsent } = await import('./consentActions')
    const { ensureTag } = await import('./analytics')
    setCookie(true)
    expect(ensureTag(host, readConsentCookie(document), [host])).toBe(true)
    document.cookie = '_ga=x'
    document.cookie = '_ga_ABC=y'
    const writes = spyJar()

    setCookie(false)
    syncConsent({ hostname: host, hosts: [host] })

    for (const name of ['_ga', '_ga_ABC']) {
      const exp = writes.filter((w) => w.startsWith(`${name}=; Max-Age=0`))
      expect(exp.filter((w) => !w.includes('domain=')), name).toHaveLength(1)
      expect(exp.filter((w) => w.includes('domain=')).map((w) => w.split('domain=')[1]).sort(), name).toEqual([...domains].sort())
    }
    expect(writes.filter((w) => w.startsWith(`${KEY}=;`))).toEqual([])
  })

  it('SY-05 a sync that finds the choice unchanged loads nothing twice and writes nothing', async () => {
    const { syncConsent } = await import('./consentActions')
    setCookie(true)
    syncConsent()
    expect(document.querySelectorAll(SYNC_TAG)).toHaveLength(1)
    const writes = spyJar()
    for (let i = 0; i < 3; i += 1) expect(syncConsent()?.analytics).toBe(true)
    expect(document.querySelectorAll(SYNC_TAG)).toHaveLength(1)
    expect(layer().filter((e) => e[0] === 'config')).toHaveLength(1)
    expect(writes).toEqual([])
  })

  it('SY-06 a synced Reject with no tag loaded expires nothing and stays revoked', async () => {
    const { syncConsent } = await import('./consentActions')
    const { trackDemoOpen } = await import('./analytics')
    document.cookie = '_ga=x'
    const writes = spyJar()
    setCookie(false)
    expect(syncConsent()?.analytics).toBe(false)
    expect(writes.filter((w) => w.startsWith('_ga'))).toEqual([])
    trackDemoOpen('hero')
    expect(layer().filter((e) => e[0] === 'event')).toEqual([])
  })

  it('SY-07 a sync that finds nothing stored keeps the page-load choice when the cookie write was dropped', async () => {
    const { applyChoice, syncConsent } = await import('./consentActions')
    const { trackDemoOpen } = await import('./analytics')
    Object.defineProperty(document, 'cookie', { configurable: true, get: () => '', set: () => {} })
    const opts = { hostname: WWW, hosts: [WWW] }

    expect(applyChoice('accept', opts).analytics).toBe(true)
    expect(syncConsent(opts)).toBeNull()

    trackDemoOpen('hero')
    expect(layer().filter((e) => e[0] === 'event').length).toBeGreaterThan(0)
  })

  it('SY-08 a sync reads the store of the host it was given, not of location', async () => {
    const { syncConsent } = await import('./consentActions')
    setCookie(false)
    localStorage.setItem(KEY, JSON.stringify(rec(true, '2026-03-01T00:00:00.000Z')))
    try {
      expect(syncConsent({ hostname: 'localhost' })?.analytics).toBe(true)
    } finally {
      localStorage.removeItem(KEY)
    }
  })

  it('SY-01 a Reject from another tab stops this tab', async () => {
    const { syncConsent } = await import('./consentActions')
    const { ensureTag, trackDemoOpen } = await import('./analytics')
    setCookie(true)
    expect(ensureTag(WWW, readConsentCookie(document))).toBe(true)
    document.cookie = '_ga=x'
    const desc = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie')!
    const writes: string[] = []
    Object.defineProperty(document, 'cookie', {
      configurable: true,
      get: () => desc.get!.call(document),
      set: (v: string) => {
        writes.push(v)
        desc.set!.call(document, v)
      },
    })

    setCookie(false)
    expect(syncConsent()?.analytics).toBe(false)

    const layer = () => ((window as TestWindow).dataLayer ?? []).map((a) => Array.from(a))
    trackDemoOpen('hero')
    expect(layer().filter((e) => e[0] === 'event')).toEqual([])
    expect(writes.some((w) => w.startsWith('_ga=; Max-Age=0') && w.endsWith('domain=.ascomply.com'))).toBe(true)
  })

  it("SY-02 an Accept from another tab loads this tab's tag", async () => {
    const { syncConsent } = await import('./consentActions')
    setCookie(false)
    expect(syncConsent()?.analytics).toBe(false)
    expect(document.querySelectorAll(SYNC_TAG)).toHaveLength(0)

    setCookie(true)
    syncConsent()
    expect(document.querySelectorAll(SYNC_TAG)).toHaveLength(1)
    syncConsent()
    expect(document.querySelectorAll(SYNC_TAG)).toHaveLength(1)
  })
})
