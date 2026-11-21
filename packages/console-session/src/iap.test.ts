import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { resolveConsoleBoot, type ConsoleBoot } from './boot'
import { iapExpiredFreshKey, iapMintedAtKey, isIapHost, mintFromIap, renewStaffSession, signedOutKey, switchGoogleAccount } from './iap'
import { expireConsole } from './signOut'
import {
  APP,
  CODE,
  customerToken,
  GW,
  installFetch,
  installStorage,
  installWindow,
  LANDING,
  NOW,
  OPS_KEY,
  reply,
  spyTimeouts,
  staffToken,
  STATE_A,
  STATE_KEY,
  stateRaw,
  SUPPORT_KEY,
  timeoutError,
  v3BorrowedRaw,
  v3Raw,
} from './testkit'

const MINT_URL = '/auth/iap-session'
const EXCHANGE = `${GW}/auth/exchange`
const REFRESH = `${GW}/auth/refresh`
const SIGN_OUT = `${GW}/auth/sign-out`
const CODE2 = 'ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvutsrqponmlk_'
const MARKER = signedOutKey(OPS_KEY)
const PENDING = `${OPS_KEY}:sign-out-pending`
const NO_STAFF = { error: 'no staff account', email: 'x@y' }

type Answer = Response | Error
interface Routes {
  mint?: Answer
  exchange?: Answer
  refresh?: Answer
  signOut?: Answer
}
const okMint = () => reply(200, { code: CODE2 })
const okExchange = () => reply(200, { access_token: staffToken('minted'), renewable: true })

function setup(o: { local?: Record<string, string>; session?: Record<string, string> } = {}, r: Routes = {}) {
  const store = installStorage(o)
  const net = installFetch((c) => {
    if (c.url === MINT_URL) return r.mint ?? okMint()
    if (c.url === EXCHANGE) return r.exchange ?? okExchange()
    if (c.url === REFRESH) return r.refresh ?? new Error('unexpected refresh')
    if (c.url === SIGN_OUT) return r.signOut ?? reply(204)
    return new Error('unexpected request ' + c.url)
  })
  return { store, net }
}
const urls = (net: { calls: { url: string }[] }) => net.calls.map((c) => c.url)

const base = {
  search: '',
  storageKey: OPS_KEY,
  target: 'ops' as const,
  gateway: GW,
  landing: LANDING,
  app: null,
  iap: true,
  now: NOW,
}
const boot = (o: Partial<Parameters<typeof resolveConsoleBoot>[0]> = {}) => resolveConsoleBoot({ ...base, ...o })

beforeEach(() => {
  vi.spyOn(console, 'warn').mockImplementation(() => undefined)
  spyTimeouts()
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('IAP boot', () => {
  it('boot_iap_nothingStoredMintsAndOpens', async () => {
    const { store, net } = setup()
    const b = await boot()
    expect(b).toMatchObject({ kind: 'open', session: { borrowed: false } })
    expect(JSON.parse(store.local.getItem(OPS_KEY)!)).toEqual({
      v: 3,
      token: staffToken('minted'),
    })
    const mint = net.calls[0]!
    expect(mint).toMatchObject({
      url: MINT_URL,
      method: 'POST',
      credentials: 'same-origin',
    })
    const posted = (mint.body as { state: string }).state
    expect(Object.keys(mint.body as object)).toEqual(['state'])
    expect(net.calls[1]).toMatchObject({
      url: EXCHANGE,
      body: { code: CODE2, state: posted },
    })
  })

  it('boot_iap_redirectManual', async () => {
    const spy = vi.fn(async (_u: string, _i?: RequestInit) => okMint())
    setup()
    vi.stubGlobal('fetch', spy)
    await boot()
    expect(spy.mock.calls[0]?.[1]).toMatchObject({ redirect: 'manual' })
  })

  it('boot_iap_neverLeavesToLanding', async () => {
    const rows: [string, () => Promise<ConsoleBoot>][] = [
      ['nothing stored', async () => (setup(), boot())],
      ['refused renewal', async () => (setup({ local: { [OPS_KEY]: v3Raw(staffToken('s')) } }, { refresh: reply(401) }), boot())],
      ['expired borrowed', async () => (setup({ local: { [OPS_KEY]: v3BorrowedRaw(staffToken('b', 1)) } }), boot({ app: APP }))],
      ['borrowed without app', async () => (setup({ local: { [OPS_KEY]: v3BorrowedRaw(staffToken('b')) } }), boot())],
      [
        'failed hand-off',
        async () => (setup({ session: { [STATE_KEY]: stateRaw(STATE_A, NOW) } }, { exchange: reply(400) }), boot({ search: `?handoff=${CODE}` })),
      ],
      [
        'non-staff hand-off',
        async () => (
          setup(
            { session: { [STATE_KEY]: stateRaw(STATE_A, NOW) } },
            {
              exchange: reply(200, {
                access_token: customerToken('c'),
                renewable: true,
              }),
            },
          ),
          boot({ search: `?handoff=${CODE}` })
        ),
      ],
      ['auth=start', async () => (setup(), boot({ search: '?auth=start' }))],
      ['auth=app without app base', async () => (setup(), boot({ search: '?auth=app' }))],
      ['mint failure', async () => (setup({}, { mint: reply(502) }), boot())],
    ]
    for (const [name, run] of rows) {
      const b = await run()
      expect(b.kind === 'leave' && b.url.startsWith(LANDING), name).toBe(false)
      expect(b.kind, name).not.toBe('leave')
    }
  })

  it('boot_iap_spentHandoffReloadMints', async () => {
    const { net } = setup({ session: { [STATE_KEY]: stateRaw(STATE_A, NOW) } })
    const search = `?handoff=${CODE}`
    expect(await boot({ search })).toMatchObject({ kind: 'open' })
    expect(urls(net)).toEqual([EXCHANGE])
    localStorage.removeItem(OPS_KEY)
    net.calls.length = 0
    expect(await boot({ search })).toMatchObject({ kind: 'open' })
    expect(urls(net)).toEqual([MINT_URL, EXCHANGE])
  })

  it('boot_iap_handoffWithoutStateMints', async () => {
    const { net } = setup()
    expect(await boot({ search: `?handoff=${CODE}` })).toMatchObject({
      kind: 'open',
    })
    expect(urls(net)).toEqual([MINT_URL, EXCHANGE])
  })

  it('boot_iap_handoffStillRedeemsFirst', async () => {
    const { net, store } = setup({
      local: { [MARKER]: '1' },
      session: { [STATE_KEY]: stateRaw(STATE_A, NOW) },
    })
    expect(await boot({ search: `?handoff=${CODE}` })).toMatchObject({
      kind: 'open',
    })
    expect(net.calls).toHaveLength(1)
    expect(net.calls[0]).toMatchObject({
      url: EXCHANGE,
      body: { code: CODE, state: STATE_A },
    })
    expect(store.local.getItem(MARKER)).toBeNull()
  })

  it('boot_iap_failedHandoffFallsThroughToIap', async () => {
    const { net } = setup({ session: { [STATE_KEY]: stateRaw(STATE_A, NOW) } }, { exchange: reply(400) })
    expect(await boot({ search: `?handoff=${CODE}` })).toEqual({
      kind: 'failed',
    })
    expect(urls(net)).toEqual([EXCHANGE, MINT_URL, EXCHANGE])
  })

  it('boot_iap_authAppStillBouncesToTheApp', async () => {
    const { net } = setup()
    const b = await boot({
      target: 'support',
      storageKey: SUPPORT_KEY,
      app: APP,
      search: '?auth=app',
    })
    expect(b).toMatchObject({ kind: 'leave', replace: true })
    expect((b as { url: string }).url.startsWith(`${APP}/?auth=console&console=support&state=`)).toBe(true)
    expect(net.calls).toHaveLength(0)
  })

  it('boot_iap_authStartIsIgnored', async () => {
    const { net } = setup()
    expect(await boot({ search: '?auth=start' })).toMatchObject({
      kind: 'open',
    })
    expect(urls(net)[0]).toBe(MINT_URL)
  })

  it('boot_iap_signedOutShowsAndPostsNothing', async () => {
    const { net } = setup({ local: { [MARKER]: '1' } })
    expect(await boot()).toEqual({ kind: 'signed-out' })
    expect(net.calls).toHaveLength(0)
  })

  it('boot_iap_noStaffShowsTheEmail', async () => {
    setup({}, { mint: reply(403, NO_STAFF) })
    expect(await boot()).toEqual({ kind: 'no-staff', email: 'x@y' })
  })
})

describe('IAP renewal', () => {
  const stored = { local: { [OPS_KEY]: v3Raw(staffToken('old')) } }

  it('boot_iap_storedRecordRenewsBeforeMinting', async () => {
    const { net } = setup(stored, {
      refresh: reply(200, { access_token: staffToken('renewed') }),
    })
    expect(await boot()).toMatchObject({
      kind: 'open',
      session: { token: staffToken('renewed') },
    })
    expect(urls(net)).toEqual([REFRESH])
  })

  it('boot_iap_refusedRenewalMintsFromIap', async () => {
    for (const refresh of [reply(401), reply(400), reply(200, { access_token: customerToken('c') })]) {
      const { net, store } = setup(stored, { refresh })
      expect(await boot()).toMatchObject({
        kind: 'open',
        session: { token: staffToken('minted') },
      })
      expect(urls(net)).toEqual([REFRESH, MINT_URL, EXCHANGE])
      expect(JSON.parse(store.local.getItem(OPS_KEY)!).token).toBe(staffToken('minted'))
    }
  })

  it('boot_iap_transientRenewalFailsAndKeepsTheRecord', async () => {
    const { net, store } = setup(stored, { refresh: reply(502) })
    expect(await boot()).toEqual({ kind: 'failed' })
    expect(store.local.getItem(OPS_KEY)).toBe(v3Raw(staffToken('old')))
    expect(urls(net)).toEqual([REFRESH])
  })

  it('renewStaffSession_fallsBackOnlyInIapMode', async () => {
    const o = { gateway: GW, storageKey: OPS_KEY }
    const on = setup({}, { refresh: reply(401) })
    await expect(renewStaffSession({ ...o, iap: true })).resolves.toEqual({
      token: staffToken('minted'),
      borrowed: false,
    })
    expect(urls(on.net)).toEqual([REFRESH, MINT_URL, EXCHANGE])
    const off = setup({}, { refresh: reply(401) })
    await expect(renewStaffSession({ ...o, iap: false })).rejects.toMatchObject({ status: 401 })
    expect(urls(off.net)).toEqual([REFRESH])
  })
})

describe('mintFromIap', () => {
  const o = { gateway: GW, storageKey: OPS_KEY, now: NOW }

  it('mintFromIap_noStaffCarriesTheEmail', async () => {
    setup({}, { mint: reply(403, NO_STAFF) })
    expect(await mintFromIap(o)).toEqual({ kind: 'no-staff', email: 'x@y' })
  })

  it('mintFromIap_everyOtherOutcomeFails', async () => {
    const rows: [string, Routes][] = [
      [
        'opaqueredirect',
        {
          mint: Object.defineProperty(new Response(null), 'type', {
            value: 'opaqueredirect',
          }),
        },
      ],
      ['network', { mint: new TypeError('Failed to fetch') }],
      ['timeout', { mint: timeoutError() }],
      ['401', { mint: reply(401, { error: 'no verified Google identity' }) }],
      ['502', { mint: reply(502, { error: 'console sign-in is unavailable' }) }],
      ['200 empty', { mint: reply(200, {}) }],
      ['200 short code', { mint: reply(200, { code: 'short' }) }],
      ['403 without email', { mint: reply(403, { error: 'no staff account' }) }],
      [
        'non-staff exchange',
        {
          exchange: reply(200, {
            access_token: customerToken('c'),
            renewable: true,
          }),
        },
      ],
    ]
    for (const [name, r] of rows) {
      const { store } = setup({}, r)
      expect(await mintFromIap(o), name).toEqual({ kind: 'failed' })
      expect(store.local.getItem(OPS_KEY), name).toBeNull()
    }
  })

  it('mintFromIap_refusesWhileSignedOut', async () => {
    const { net } = setup({ local: { [MARKER]: '1' } })
    expect(await mintFromIap(o)).toEqual({ kind: 'signed-out' })
    expect(net.calls).toHaveLength(0)
  })

  it('mintFromIap_recordsTheMintTime', async () => {
    const ok = setup()
    await mintFromIap(o)
    expect(ok.store.session.getItem(iapMintedAtKey(OPS_KEY))).toBe(String(NOW))
    const bad = setup({}, { mint: reply(502) })
    await mintFromIap(o)
    expect(bad.store.session.getItem(iapMintedAtKey(OPS_KEY))).toBeNull()
  })
})

describe('IAP loop guard', () => {
  it('expire_iap_freshMintExpiryShowsFailed', async () => {
    const win = installWindow()
    const { net, store } = setup({
      local: { [OPS_KEY]: v3Raw(staffToken('s')) },
      session: { [iapMintedAtKey(OPS_KEY)]: String(Date.now() - 10_000) },
    })
    await expireConsole({
      storageKey: OPS_KEY,
      gateway: GW,
      landing: LANDING,
      app: null,
      target: 'ops',
      iap: true,
    })
    expect(win.reload).toHaveBeenCalledTimes(1)
    expect(store.session.getItem(iapExpiredFreshKey(OPS_KEY))).toBe('1')
    expect(await boot()).toEqual({ kind: 'failed' })
    expect(net.calls).toHaveLength(0)
    expect(store.session.getItem(iapExpiredFreshKey(OPS_KEY))).toBeNull()
    expect(await boot()).toMatchObject({ kind: 'open' })
    expect(urls(net)[0]).toBe(MINT_URL)
  })

  it('expire_iap_oldMintExpiryMints', async () => {
    installWindow()
    const { net, store } = setup({
      local: { [OPS_KEY]: v3Raw(staffToken('s')) },
      session: { [iapMintedAtKey(OPS_KEY)]: String(Date.now() - 61_000) },
    })
    await expireConsole({
      storageKey: OPS_KEY,
      gateway: GW,
      landing: LANDING,
      app: null,
      target: 'ops',
      iap: true,
    })
    expect(store.session.getItem(iapExpiredFreshKey(OPS_KEY))).toBeNull()
    expect(await boot()).toMatchObject({ kind: 'open' })
    expect(urls(net)[0]).toBe(MINT_URL)
  })
})

describe('isIapHost', () => {
  it('isIapHost_onlyTheTwoProductionHosts', () => {
    for (const h of ['ops.ascomply.com', 'sup.ascomply.com']) expect(isIapHost(h), h).toBe(true)
    for (const h of ['www.ascomply.com', 'app.ascomply.com', 'ops-console-pr-1-abc.a.run.app', 'localhost', 'OPS.ascomply.com.evil.com'])
      expect(isIapHost(h), h).toBe(false)
  })
})

describe('switchGoogleAccount', () => {
  const CHOOSER = '/_gcp_iap/clear_login_cookie'

  it('switchGoogleAccount_revokesThenLeavesForTheChooser', async () => {
    const log: string[] = []
    const win = installWindow(log)
    const { net, store } = setup({
      local: { [OPS_KEY]: v3Raw(staffToken('s')) },
    })
    await switchGoogleAccount({ storageKey: OPS_KEY, gateway: GW })
    expect(net.calls).toHaveLength(1)
    expect(net.calls[0]).toMatchObject({
      url: SIGN_OUT,
      method: 'POST',
      credentials: 'include',
      headers: { 'x-auth-session': 'cookie' },
    })
    expect(win.assignWrites).toEqual([CHOOSER])
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(store.local.getItem(MARKER)).toBeNull()
    expect(log).toEqual(['assign'])
  })

  it('switchGoogleAccount_revokesWithoutARecordAndSkipsABorrowedOne', async () => {
    const none = setup()
    const w1 = installWindow()
    await switchGoogleAccount({ storageKey: OPS_KEY, gateway: GW })
    expect(urls(none.net)).toEqual([SIGN_OUT])
    expect(w1.assignWrites).toEqual([CHOOSER])

    const borrowed = setup({
      local: { [OPS_KEY]: v3BorrowedRaw(staffToken('b')) },
    })
    const w2 = installWindow()
    await switchGoogleAccount({ storageKey: OPS_KEY, gateway: GW })
    expect(borrowed.net.calls).toHaveLength(0)
    expect(borrowed.store.local.getItem(OPS_KEY)).toBeNull()
    expect(w2.assignWrites).toEqual([CHOOSER])
  })

  it('switchGoogleAccount_unreachedLeavesPendingNotSignedOut', async () => {
    const win = installWindow()
    const { net, store } = setup({ local: { [OPS_KEY]: v3Raw(staffToken('s')) } }, { signOut: new TypeError('Failed to fetch') })
    await switchGoogleAccount({ storageKey: OPS_KEY, gateway: GW })
    expect(store.local.getItem(PENDING)).toBe('1')
    expect(store.local.getItem(MARKER)).toBeNull()
    expect(win.assignWrites).toHaveLength(1)

    net.calls.length = 0
    vi.stubGlobal('fetch', async (u: string) => (u === SIGN_OUT ? reply(204) : u === MINT_URL ? okMint() : okExchange()))
    expect(await boot()).toMatchObject({ kind: 'open' })
    expect(store.local.getItem(PENDING)).toBeNull()
  })
})
