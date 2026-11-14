import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { resolveConsoleBoot, type ConsoleBoot } from './boot'
import {
  APP,
  b64url,
  BASE64URL_43_RE,
  type Call,
  CODE,
  customerToken,
  GW,
  installFetch,
  jwt,
  installStorage,
  LANDING,
  NOW,
  OPS_KEY,
  recordRaw,
  reply,
  type MemoryStorage,
  spyTimeouts,
  staffToken,
  STATE_A,
  STATE_KEY,
  stateRaw,
  SUPPORT_KEY,
  throwingStorage,
  timeoutError,
  v3BorrowedRaw,
  v3Raw,
} from './testkit'

const TIMEOUT_MS = 15000 // AbortSignal.timeout in redeemHandoff and RENEW_TIMEOUT_MS (frontend/app/src/lib)
const SIGN_OUT = `${GW}/auth/sign-out`
const PENDING = `${OPS_KEY}:sign-out-pending`
const EXCHANGE = `${GW}/auth/exchange`
const REFRESH = `${GW}/auth/refresh`
const FRONT_DOOR = new RegExp(`^${LANDING}/\\?state=([A-Za-z0-9_-]{43})&console=ops$`)
const withOutcome = (o: string) => new RegExp(`^${LANDING}/\\?state=([A-Za-z0-9_-]{43})&console=ops&signin=${o}$`)

const OLD_TOKEN = staffToken('old', NOW / 1000 + 10 * 365 * 24 * 3600)
const OLD_RECORD = v3Raw(OLD_TOKEN)
const granted = (token: string) => reply(200, { access_token: token, renewable: true })

let store: { local: MemoryStorage; session: MemoryStorage }
let timeouts: ReturnType<typeof spyTimeouts>

beforeEach(() => {
  vi.spyOn(console, 'warn').mockImplementation(() => undefined)
  timeouts = spyTimeouts()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

interface Setup {
  local?: string
  state?: string
  answer?: (c: Call) => Response | Error
  search?: string
  target?: 'ops' | 'support'
  gateway?: string | null
  landing?: string | null
  app?: string | null
  pending?: boolean
}

async function run(o: Setup = {}) {
  store = installStorage({
    local: { ...(o.local === undefined ? {} : { [OPS_KEY]: o.local }), ...(o.pending ? { [PENDING]: '1' } : {}) },
    session: o.state === undefined ? {} : { [STATE_KEY]: o.state },
  })
  const net = installFetch((c) => (o.answer ?? (() => new Error('unexpected request')))(c))
  const boot = await resolveConsoleBoot({
    search: o.search ?? '',
    storageKey: OPS_KEY,
    target: o.target ?? 'ops',
    gateway: o.gateway === undefined ? GW : o.gateway,
    landing: o.landing === undefined ? LANDING : o.landing,
    app: o.app,
    now: NOW,
  })
  return { boot, calls: net.calls }
}

function leaveUrl(boot: ConsoleBoot): string {
  expect(boot.kind).toBe('leave')
  return boot.kind === 'leave' ? boot.url : ''
}

// The state landing must hold is the one the tab stored.
const heldBlob = (): { v: number; s: string; at: number } | null => {
  const raw = store.session.getItem(STATE_KEY)
  return raw === null ? null : (JSON.parse(raw) as { v: number; s: string; at: number })
}
const heldState = (): string | null => heldBlob()?.s ?? null

describe('standalone (AC-4)', () => {
  it('boot_standaloneOpensWithNoRequest', async () => {
    const searches = ['', '?auth=start', `?handoff=${CODE}`]
    for (const search of searches) {
      const { boot, calls } = await run({ landing: null, search, local: OLD_RECORD, state: stateRaw(STATE_A, NOW) })
      expect(boot, search).toEqual({ kind: 'open', session: null })
      expect(calls, search).toHaveLength(0)
      for (const s of [store.local, store.session]) {
        expect(s.getItem, search).not.toHaveBeenCalled()
        expect(s.setItem, search).not.toHaveBeenCalled()
        expect(s.removeItem, search).not.toHaveBeenCalled()
      }
    }
  })
})

describe('?auth=start (AC-5)', () => {
  it('boot_authStartMintsAFreshStateAndLeavesReady', async () => {
    const { boot, calls } = await run({ search: '?auth=start', state: stateRaw(STATE_A, NOW - 10_000), local: OLD_RECORD })
    const url = leaveUrl(boot)
    const m = withOutcome('ready').exec(url)
    expect(m, url).not.toBeNull()
    const state = m?.[1]
    expect(state).not.toBe(STATE_A)
    expect(heldState()).toBe(state)
    expect(heldBlob()).toEqual({ v: 1, s: state, at: NOW })
    expect(calls).toHaveLength(0)
  })

  it('boot_authStartLeaves_withReplace', async () => {
    const { boot } = await run({ search: '?auth=start' })
    expect(boot).toMatchObject({ kind: 'leave', replace: true })
    expect(withOutcome('ready').test(leaveUrl(boot))).toBe(true)
  })

  it('boot_authStartLeaves_withReplace_supportTarget', async () => {
    const { boot } = await run({ search: '?auth=start', target: 'support', local: OLD_RECORD })
    expect(boot).toMatchObject({ kind: 'leave', replace: true })
    expect(leaveUrl(boot)).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=support&signin=ready$`))
  })

  it('boot_otherLeaves_haveNoReplace', async () => {
    const scenarios: Record<string, Setup> = {
      'no session': {},
      'failed hand-off': { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => reply(503) },
      'not staff': { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => granted(customerToken('x')) },
    }
    for (const [name, setup] of Object.entries(scenarios)) {
      const { boot } = await run(setup)
      expect(boot.kind, name).toBe('leave')
      expect(boot.kind === 'leave' ? boot.replace : 'not-leave', name).toBeUndefined()
    }
  })
})

describe('hand-off redemption (AC-6, AC-7)', () => {
  it('boot_handoffStaffIsStoredAndOpens', async () => {
    const access = staffToken('exchanged')
    const { boot, calls } = await run({
      search: `?handoff=${CODE}`,
      state: stateRaw(STATE_A, NOW - 1000),
      answer: () => granted(access),
    })
    expect(boot).toEqual({ kind: 'open', session: { token: access, borrowed: false } })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: EXCHANGE, method: 'POST' })
    expect(calls[0]?.body).toEqual({ code: CODE, state: STATE_A })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: access })
    expect(store.session.getItem(STATE_KEY)).toBeNull()
    expect(timeouts).toHaveBeenCalledTimes(1)
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
    expect(calls[0]?.signal).toBe(timeouts.mock.results[0]?.value)
  })

  it('boot_handoffNonStaffStoresNothing', async () => {
    const { boot, calls } = await run({
      search: `?handoff=${CODE}`,
      state: stateRaw(STATE_A, NOW - 1000),
      answer: () => granted(customerToken('exchanged')),
    })
    const url = leaveUrl(boot)
    expect(withOutcome('not-staff').test(url), url).toBe(true)
    expect(calls).toHaveLength(1)
    expect(store.local.setItem).not.toHaveBeenCalled()
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(heldState()).toBe(withOutcome('not-staff').exec(url)?.[1])
    // The redeemed state is spent: landing gets a new one.
    expect(heldState()).not.toBe(STATE_A)
  })

  it('boot_handoffFailureKeepsAStoredRecord', async () => {
    const stored = v3Raw(OLD_TOKEN)
    const failures: [string, Response | Error][] = [
      ['exchange 400', reply(400, { error: 'invalid or expired code' })],
      ['network', new TypeError('Failed to fetch')],
      ['timeout', timeoutError()],
      ['429', reply(429, { error: 'too many requests' })],
      ['500', reply(500, { error: 'internal' })],
      ['200 with only a refresh token', reply(200, { refresh_token: 'R-x' })],
      ['200 with renewable false', reply(200, { access_token: staffToken('x'), renewable: false })],
      ['200 JSON null', reply(200, null)],
      ['200 non-JSON', new Response('<html>', { status: 200 })],
    ]
    expect(failures.length).toBeGreaterThan(0)
    for (const [name, answer] of failures) {
      const { boot, calls } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), local: stored, answer: () => answer })
      const url = leaveUrl(boot)
      expect(withOutcome('failed').test(url), `${name}: ${url}`).toBe(true)
      // The attempt spent the state: landing gets a new one, and the key holds it.
      expect(withOutcome('failed').exec(url)?.[1], name).not.toBe(STATE_A)
      expect(heldState(), name).toBe(withOutcome('failed').exec(url)?.[1])
      expect(calls.map((c) => c.url), name).toEqual([EXCHANGE])
      expect(store.local.getItem(OPS_KEY), name).toBe(stored)
      expect(store.local.setItem, name).not.toHaveBeenCalled()
      expect(store.local.removeItem, name).not.toHaveBeenCalled()
    }
  })

  it('boot_handoffWithoutStateRedeemsNothing', async () => {
    const renewed = staffToken('renewed')
    const scenarios: [string, Setup][] = [
      ['no live state', { search: `?handoff=${CODE}` }],
      ['state at the TTL', { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 600_000) }],
      ['code of 42 chars', { search: `?handoff=${CODE.slice(1)}`, state: stateRaw(STATE_A, NOW - 1000) }],
      ['code with +', { search: `?handoff=${CODE.slice(1)}%2B`, state: stateRaw(STATE_A, NOW - 1000) }],
      ['two codes', { search: `?handoff=${CODE}&handoff=${STATE_A}`, state: stateRaw(STATE_A, NOW - 1000) }],
    ]
    for (const [name, setup] of scenarios) {
      const { boot, calls } = await run({ ...setup, local: OLD_RECORD, answer: () => granted(renewed) })
      expect(calls.filter((c) => c.url === EXCHANGE), name).toHaveLength(0)
      expect(calls.filter((c) => c.url === REFRESH), name).toHaveLength(1)
      expect(calls[0]?.body, name).toBeUndefined()
      expect(boot, name).toEqual({ kind: 'open', session: { token: renewed, borrowed: false } })
    }
    // No record either: the front door, with no outcome.
    const { boot, calls } = await run({ search: `?handoff=${CODE}` })
    expect(calls).toHaveLength(0)
    expect(FRONT_DOOR.test(leaveUrl(boot))).toBe(true)
  })
})

describe('renewal of a stored record (AC-8, AC-9)', () => {
  it('boot_storedRecordAlwaysRenewsFirst', async () => {
    const renewed = staffToken('renewed')
    const { boot, calls } = await run({ local: OLD_RECORD, answer: () => granted(renewed) })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: REFRESH, method: 'POST' })
    expect(calls[0]?.body).toBeUndefined()
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, borrowed: false } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: renewed })
    expect(timeouts).toHaveBeenCalledTimes(1)
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
    expect(calls[0]?.signal).toBe(timeouts.mock.results[0]?.value)
  })

  it('boot_expiredStoredTokenRenews', async () => {
    const expired = staffToken('expired', NOW / 1000 - 3600)
    const renewed = staffToken('renewed')
    const { boot, calls } = await run({ local: v3Raw(expired), answer: () => granted(renewed) })
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, borrowed: false } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: renewed })
  })

  it('boot_refusedRenewalClearsAndLeaves', async () => {
    const refusals: [number, () => Response][] = [
      [400, () => reply(400, { error: 'invalid or expired refresh token' })],
      [401, () => reply(401, { error: 'invalid or expired refresh token' })],
      [400, () => reply(400)],
      [401, () => new Response('<html>', { status: 401 })],
    ]
    for (const [status, answer] of refusals) {
      const { boot, calls } = await run({ local: OLD_RECORD, answer })
      expect(calls, String(status)).toHaveLength(1)
      expect(store.local.getItem(OPS_KEY), String(status)).toBeNull()
      const url = leaveUrl(boot)
      expect(FRONT_DOOR.test(url), `${status}: ${url}`).toBe(true)
      expect(url).not.toContain('signin=')
      expect(heldState()).toBe(FRONT_DOOR.exec(url)?.[1])
    }
  })

  it('boot_destaffedRenewalLeavesNotStaff', async () => {
    const { boot, calls } = await run({ local: OLD_RECORD, answer: () => granted(customerToken('renewed')) })
    expect(calls).toHaveLength(1)
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(store.local.setItem).not.toHaveBeenCalled()
    const url = leaveUrl(boot)
    expect(withOutcome('not-staff').test(url), url).toBe(true)
  })

  it('boot_transientRenewalKeepsTheRecord', async () => {
    const answers: [string, Response | Error][] = [
      ['network error', new TypeError('Failed to fetch')],
      ['timeout', timeoutError()],
      ['429', reply(429, { error: 'too many requests' })],
      ['403', reply(403, { error: 'forbidden' })],
      ['404', reply(404)],
      ['500', reply(500, { error: 'internal' })],
      ['502', reply(502, { error: 'renewal is unavailable' })],
      ['503', reply(503, { error: 'unavailable' })],
      ['200 with no access token', reply(200, {})],
      ['200 with only a refresh token', reply(200, { refresh_token: 'R-new' })],
      ['200 with an empty access token', reply(200, { access_token: '', refresh_token: 'R-new' })],
      ['200 with a non-string access token', reply(200, { access_token: 1, refresh_token: 'R-new' })],
      ['200 with an array access token', reply(200, { access_token: [staffToken('renewed')] })],
      ['200 JSON null', reply(200, null)],
      ['204 with no body', reply(204)],
      ['200 with an object access token', reply(200, { access_token: {} })],
      ['200 with a non-JSON body', new Response('<html>', { status: 200 })],
    ]
    expect(answers.length).toBeGreaterThan(0)
    for (const [name, answer] of answers) {
      const { boot, calls } = await run({ local: OLD_RECORD, answer: () => answer })
      expect(calls, name).toHaveLength(1)
      expect(store.local.getItem(OPS_KEY), name).toBe(OLD_RECORD)
      expect(store.local.removeItem, name).not.toHaveBeenCalled()
      expect(store.local.setItem, name).not.toHaveBeenCalled()
      const url = leaveUrl(boot)
      expect(withOutcome('failed').test(url), `${name}: ${url}`).toBe(true)
    }
  })

  it('boot_forgedV2RecordIsRefused', async () => {
    // Unsigned JWT with the staff claim; only the gateway's refresh answer can open the console.
    const forged = `${b64url({ alg: 'none' })}.${b64url({ sub: 'x', exp: NOW / 1000 + 3600, app_metadata: { staff: true } })}.`
    const { boot, calls } = await run({ local: recordRaw(forged, 'R-forged'), answer: () => reply(401, { error: 'invalid or expired refresh token' }) })
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(boot.kind).toBe('leave')
    expect(store.local.getItem(OPS_KEY)).toBeNull()
  })
})

describe('front door and missing gateway (AC-10)', () => {
  it('boot_nothingStoredLeavesByTheFrontDoor', async () => {
    const { boot, calls } = await run({})
    const url = leaveUrl(boot)
    const m = FRONT_DOOR.exec(url)
    expect(m, url).not.toBeNull()
    expect(calls).toHaveLength(0)
    expect(heldBlob()).toEqual({ v: 1, s: m?.[1], at: NOW })

    // A state minted under a minute ago is reused (ensure, not mint).
    const held = await run({ state: stateRaw(STATE_A, NOW - 30_000) })
    expect(leaveUrl(held.boot)).toBe(`${LANDING}/?state=${STATE_A}&console=ops`)

    const support = await run({ target: 'support' })
    expect(leaveUrl(support.boot)).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=support$`))

    // The retired persona door is not a credential, for either console.
    for (const persona of ['developer', 'support', 'firm', 'inhouse']) {
      const viaParam = await run({ search: `?persona=${persona}` })
      expect(FRONT_DOOR.test(leaveUrl(viaParam.boot)), `?persona=${persona} at ops`).toBe(true)
      expect(viaParam.calls, persona).toHaveLength(0)
      expect(store.local.getItem(OPS_KEY), persona).toBeNull()
    }
    const supportViaParam = await run({ target: 'support', search: '?persona=support' })
    expect(leaveUrl(supportViaParam.boot)).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=support$`))
  })

  it('boot_noGatewayNeverOpens', async () => {
    const withCode = await run({ gateway: null, search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000) })
    expect(withCode.calls).toHaveLength(0)
    expect(withOutcome('failed').test(leaveUrl(withCode.boot))).toBe(true)

    const withRecord = await run({ gateway: null, local: OLD_RECORD })
    expect(withRecord.calls).toHaveLength(0)
    expect(FRONT_DOOR.test(leaveUrl(withRecord.boot))).toBe(true)
  })
})

describe('URLs (AC-13)', () => {
  it('urls_carryNoTokenOrCode', async () => {
    const access = staffToken('exchanged')
    const refresh = 'R-secret-refresh'
    const secrets = [access, refresh, 'R-old', OLD_TOKEN, CODE, 'eyJ']
    const scenarios: Setup[] = [
      { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => granted(customerToken('x')) },
      { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => reply(400, { error: access }) },
      { search: `?handoff=${CODE}`, gateway: null, state: stateRaw(STATE_A, NOW - 1000) },
      { search: '?auth=start', local: OLD_RECORD },
      { local: OLD_RECORD, answer: () => reply(401, { error: 'invalid or expired refresh token' }) },
      { local: OLD_RECORD, answer: () => granted(customerToken('x')) },
      { local: OLD_RECORD, answer: () => reply(503) },
      { search: `?handoff=${CODE}` },
      {},
    ]
    const urls: string[] = []
    for (const s of scenarios) {
      const { boot } = await run(s)
      if (boot.kind === 'leave') urls.push(boot.url)
    }
    expect(urls).toHaveLength(scenarios.length)
    for (const url of urls) {
      expect(url.startsWith(`${LANDING}/?state=`), url).toBe(true)
      for (const secret of secrets) expect(url, secret).not.toContain(secret)
      expect(url.match(/state=([^&]+)/)?.[1]).toMatch(BASE64URL_43_RE)
    }
  })
})

describe('precedence and isolation', () => {
  it('boot_authStartBeatsAHandoffCode', async () => {
    const { boot, calls } = await run({
      search: `?handoff=${CODE}&auth=start`,
      state: stateRaw(STATE_A, NOW - 1000),
      local: OLD_RECORD,
      answer: () => granted(staffToken('x')),
    })
    const url = leaveUrl(boot)
    const m = withOutcome('ready').exec(url)
    expect(m, url).not.toBeNull()
    expect(calls).toHaveLength(0)
    expect(m?.[1]).not.toBe(STATE_A)
    expect(heldBlob()).toEqual({ v: 1, s: m?.[1], at: NOW })
    expect(store.local.getItem(OPS_KEY)).toBe(OLD_RECORD)
  })

  it('boot_authParamOtherThanStartIsIgnored', async () => {
    for (const search of ['?auth=other', '?auth=', '?auth=START']) {
      const { boot, calls } = await run({ search, local: OLD_RECORD, answer: () => granted(staffToken('renewed')) })
      expect(boot.kind, search).toBe('open')
      expect(calls.map((c) => c.url), search).toEqual([REFRESH])
    }
  })

  it('boot_authStartLeavesReadyWithoutAGateway', async () => {
    const { boot, calls } = await run({ search: '?auth=start', gateway: null })
    expect(withOutcome('ready').test(leaveUrl(boot))).toBe(true)
    expect(calls).toHaveLength(0)
  })

  it('boot_handoffWithALiveStateBeatsAStoredRecord', async () => {
    const access = staffToken('exchanged')
    const { boot, calls } = await run({
      search: `?handoff=${CODE}`,
      state: stateRaw(STATE_A, NOW - 1000),
      local: OLD_RECORD,
      answer: () => granted(access),
    })
    expect(calls.map((c) => c.url)).toEqual([EXCHANGE])
    expect(boot).toEqual({ kind: 'open', session: { token: access, borrowed: false } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: access })
  })

  it('boot_v1RecordIsNoRecord', async () => {
    const v1 = JSON.stringify({ v: 1, operator: 'developer' })
    const { boot, calls } = await run({ local: v1 })
    expect(FRONT_DOOR.test(leaveUrl(boot))).toBe(true)
    expect(calls).toHaveLength(0)
    expect(console.warn).toHaveBeenCalledTimes(1)
  })

  it('boot_supportConsoleUsesItsOwnKeyAndTarget', async () => {
    const supportToken = staffToken('support-old')
    const renewed = staffToken('support-renewed')
    const local = { [OPS_KEY]: OLD_RECORD, [SUPPORT_KEY]: v3Raw(supportToken) }
    const base = { search: '', target: 'support' as const, gateway: GW, landing: LANDING, now: NOW }

    store = installStorage({ local })
    let net = installFetch(() => granted(renewed))
    const opened = await resolveConsoleBoot({ ...base, storageKey: SUPPORT_KEY })
    expect(opened).toEqual({ kind: 'open', session: { token: renewed, borrowed: false } })
    expect(net.calls[0]?.body).toBeUndefined()
    expect(JSON.parse(store.local.getItem(SUPPORT_KEY) ?? 'null')).toEqual({ v: 3, token: renewed })
    expect(store.local.getItem(OPS_KEY)).toBe(OLD_RECORD)

    store = installStorage({ local })
    net = installFetch(() => reply(401, { error: 'invalid or expired refresh token' }))
    const refused = await resolveConsoleBoot({ ...base, storageKey: SUPPORT_KEY })
    expect(refused.kind === 'leave' && refused.url).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=support$`))
    expect(store.local.getItem(SUPPORT_KEY)).toBeNull()
    expect(store.local.getItem(OPS_KEY)).toBe(OLD_RECORD)
  })
})

describe('storage that throws (AC-15)', () => {
  it('boot_storageThrowsNeverEscapes', async () => {
    const access = staffToken('exchanged')
    const base = { storageKey: OPS_KEY, target: 'ops' as const, gateway: GW, landing: LANDING, now: NOW }

    // A failed record write still opens this load.
    installStorage({ session: { [STATE_KEY]: stateRaw(STATE_A, NOW - 1000) } })
    vi.stubGlobal('localStorage', throwingStorage())
    installFetch(() => granted(access))
    await expect(resolveConsoleBoot({ ...base, search: `?handoff=${CODE}` })).resolves.toEqual({
      kind: 'open',
      session: { token: access, borrowed: false },
    })
    expect(console.warn).toHaveBeenCalledTimes(1)

    // An unreadable record reads as none: the front door, no request.
    vi.mocked(console.warn).mockClear()
    installStorage()
    vi.stubGlobal('localStorage', throwingStorage())
    let net = installFetch(() => new Error('unexpected request'))
    let boot = await resolveConsoleBoot({ ...base, search: '' })
    expect(FRONT_DOOR.test(leaveUrl(boot))).toBe(true)
    expect(net.calls).toHaveLength(0)
    expect(console.warn).toHaveBeenCalledTimes(1)

    // An unreadable state redeems nothing and leaves with an unstored state.
    vi.mocked(console.warn).mockClear()
    installStorage()
    vi.stubGlobal('sessionStorage', throwingStorage())
    net = installFetch(() => new Error('unexpected request'))
    boot = await resolveConsoleBoot({ ...base, search: `?handoff=${CODE}` })
    expect(FRONT_DOOR.test(leaveUrl(boot))).toBe(true)
    expect(net.calls).toHaveLength(0)
    expect(vi.mocked(console.warn).mock.calls.length).toBeGreaterThan(0)

    // ?auth=start with unwritable state storage still leaves.
    boot = await resolveConsoleBoot({ ...base, search: '?auth=start' })
    expect(withOutcome('ready').test(leaveUrl(boot))).toBe(true)
  })
})

const appStart = (target: string) => new RegExp(`^${APP}/\\?auth=console&console=${target}&state=([A-Za-z0-9_-]{43})$`)
const BORROWED = v3BorrowedRaw(OLD_TOKEN)
const FRONT_DOOR_SUPPORT = /^https:\/\/www\.test\/\?state=[A-Za-z0-9_-]{43}&console=support$/
const access = (token: string) => reply(200, { access_token: token })

describe('app-borrowed session (SUPP-11-02)', () => {
  it('boot_authAppLeavesToTheAppWithAFreshState', async () => {
    const { boot, calls } = await run({ search: '?auth=app', target: 'support', app: APP, state: stateRaw(STATE_A, NOW - 10_000) })
    expect(boot).toMatchObject({ kind: 'leave', replace: true })
    const url = leaveUrl(boot)
    const m = appStart('support').exec(url)
    expect(m, url).not.toBeNull()
    expect(m?.[1]).not.toBe(STATE_A)
    expect(heldBlob()).toEqual({ v: 1, s: m?.[1], at: NOW })
    expect(calls).toHaveLength(0)
  })

  it('boot_authAppWithoutAnAppBaseLeavesReady', async () => {
    for (const app of [null, undefined]) {
      const { boot } = await run({ search: '?auth=app', app })
      expect(boot).toMatchObject({ kind: 'leave', replace: true })
      expect(withOutcome('ready').test(leaveUrl(boot))).toBe(true)
    }
  })

  it('boot_appHandoffOpensABorrowedStaffSession', async () => {
    const token = staffToken('borrowed')
    const { boot, calls } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), target: 'support', app: APP, answer: () => access(token) })
    expect(boot).toEqual({ kind: 'open', session: { token, borrowed: true } })
    expect(calls.map((c) => c.url)).toEqual([EXCHANGE])
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token, from: 'app' })
  })

  it('boot_appHandoffOverwritesAStoredLandingRecord', async () => {
    const token = staffToken('borrowed')
    await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), local: OLD_RECORD, target: 'support', app: APP, answer: () => access(token) })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token, from: 'app' })
  })

  it('boot_opsHandoffAnswerWithoutARefreshTokenFails', async () => {
    for (const o of [{ target: 'ops' as const, app: APP }, { target: 'support' as const, app: null }]) {
      const { boot } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), ...o, answer: () => access(staffToken('o')) })
      expect(/&signin=failed$/.test(leaveUrl(boot)), JSON.stringify(o)).toBe(true)
      expect(store.local.getItem(OPS_KEY)).toBeNull()
    }
  })

  it('boot_authAppKeepsAStoredLandingRecord', async () => {
    const renewed = staffToken('renewed')
    const { boot, calls } = await run({
      search: '?auth=app',
      target: 'support',
      app: APP,
      local: OLD_RECORD,
      answer: () => reply(200, { access_token: renewed }),
    })
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, borrowed: false } })
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(calls[0]!.body).toBeUndefined()
  })

  it('boot_authAppWithABorrowedRecordStillBounces', async () => {
    const { boot } = await run({ search: '?auth=app', target: 'support', app: APP, local: BORROWED, state: stateRaw(STATE_A, NOW - 1000) })
    expect(appStart('support').test(leaveUrl(boot))).toBe(true)
    expect(heldState()).not.toBe(STATE_A)
    expect(store.local.getItem(OPS_KEY)).toBe(BORROWED)
  })

  it('boot_expiredBorrowedRecordIsClearedAndLeavesToLanding', async () => {
    const expired = v3BorrowedRaw(staffToken('exp', NOW / 1000 - 10))
    const { boot, calls } = await run({ local: expired, app: APP, target: 'support' })
    expect(/^https:\/\/www\.test\/\?state=[A-Za-z0-9_-]{43}&console=support$/.test(leaveUrl(boot))).toBe(true)
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(calls).toHaveLength(0)
  })

  it('boot_borrowedRecordAfterOneBounceIsClearedAndLeavesToLanding', async () => {
    const first = await run({ local: BORROWED, app: APP, target: 'support' })
    expect(appStart('support').test(leaveUrl(first.boot))).toBe(true)
    const state = store.session.getItem(STATE_KEY) ?? undefined
    const { boot } = await run({ local: BORROWED, state, app: APP, target: 'support' })
    expect(/^https:\/\/www\.test\/\?state=[A-Za-z0-9_-]{43}&console=support$/.test(leaveUrl(boot))).toBe(true)
    expect(store.local.getItem(OPS_KEY)).toBeNull()
  })

  it('boot_borrowedRecordWithoutAValidExpIsClearedAndLeavesToLanding', async () => {
    const tokens = [
      staffToken('exp-now', NOW / 1000),
      jwt({ sub: 'x', app_metadata: { staff: true } }),
      jwt({ sub: 'x', exp: String(NOW / 1000 + 3600), app_metadata: { staff: true } }),
      'not-a-jwt',
    ]
    expect(tokens).toHaveLength(4)
    for (const t of tokens) {
      const { boot, calls } = await run({ local: v3BorrowedRaw(t), app: APP, target: 'support' })
      expect(FRONT_DOOR_SUPPORT.test(leaveUrl(boot)), t).toBe(true)
      expect(store.local.getItem(OPS_KEY), t).toBeNull()
      expect(calls).toHaveLength(0)
    }
  })

  it('boot_borrowedRecordWithAStaleHeldStateStillBouncesAndIsKept', async () => {
    const { boot } = await run({ local: BORROWED, app: APP, target: 'support', state: stateRaw(STATE_A, NOW - 11 * 60_000) })
    expect(appStart('support').test(leaveUrl(boot))).toBe(true)
    expect(store.local.getItem(OPS_KEY)).toBe(BORROWED)
  })

  it('boot_borrowedRecordWithAHeldStateAndNoAppIsKept', async () => {
    const { boot } = await run({ local: BORROWED, app: null, target: 'support', state: stateRaw(STATE_A, NOW - 1000) })
    expect(FRONT_DOOR_SUPPORT.test(leaveUrl(boot))).toBe(true)
    expect(store.local.getItem(OPS_KEY)).toBe(BORROWED)
  })

  it('boot_authAppKeepsAnExpiredLandingRecordAndRenewsIt', async () => {
    const expiredLanding = v3Raw(staffToken('old', NOW / 1000 - 10))
    const renewed = staffToken('renewed')
    const { boot, calls } = await run({ search: '?auth=app', target: 'support', app: APP, local: expiredLanding, answer: () => granted(renewed) })
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, borrowed: false } })
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
  })

  it('boot_authAppOnOpsBouncesEvenWithALandingRecord', async () => {
    const { boot, calls } = await run({ search: '?auth=app', target: 'ops', app: APP, local: OLD_RECORD })
    expect(appStart('ops').test(leaveUrl(boot))).toBe(true)
    expect(calls).toHaveLength(0)
    expect(store.local.getItem(OPS_KEY)).toBe(OLD_RECORD)
  })

  it('boot_authAppKeptLandingRecordRejectedLeavesToLandingOnce', async () => {
    const { boot, calls } = await run({ search: '?auth=app', target: 'support', app: APP, local: OLD_RECORD, answer: () => reply(401, { error: 'invalid' }) })
    expect(FRONT_DOOR_SUPPORT.test(leaveUrl(boot))).toBe(true)
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(store.local.getItem(OPS_KEY)).toBeNull()
  })

  it('boot_supportLandingHandoffStoresALandingRecordAndABadRenewableFails', async () => {
    const token = staffToken('both')
    const ok = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), target: 'support', app: null, answer: () => granted(token) })
    expect(ok.boot).toEqual({ kind: 'open', session: { token, borrowed: false } })
    // With an app base the borrowed path is allowed, but renewable true still stores a landing record.
    const withApp = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), target: 'support', app: APP, answer: () => granted(token) })
    expect(withApp.boot).toEqual({ kind: 'open', session: { token, borrowed: false } })
    expect(store.local.getItem(OPS_KEY)).toBe(v3Raw(token))
    for (const renewable of [null, '', false]) {
      const { boot } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), target: 'support', app: null, answer: () => reply(200, { access_token: token, renewable }) })
      expect(/&console=support&signin=failed$/.test(leaveUrl(boot)), String(renewable)).toBe(true)
      expect(store.local.getItem(OPS_KEY)).toBeNull()
    }
  })

  it('boot_supportAppHandoffAnswerWithoutAnAccessTokenFails', async () => {
    for (const body of [{ refresh_token: 'R' }, {}, { access_token: '' }]) {
      const { boot } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), target: 'support', app: APP, answer: () => reply(200, body) })
      expect(/&console=support&signin=failed$/.test(leaveUrl(boot)), JSON.stringify(body)).toBe(true)
      expect(store.local.getItem(OPS_KEY)).toBeNull()
    }
  })

  it('boot_appHandoffNonStaffStoresNothing', async () => {
    const { boot } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), target: 'support', app: APP, answer: () => access(customerToken('c')) })
    expect(/&console=support&signin=not-staff$/.test(leaveUrl(boot))).toBe(true)
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(store.local.setItem).not.toHaveBeenCalled()
  })

  it('boot_handoffAnswerWithoutAnAccessTokenFails', async () => {
    for (const body of [{ refresh_token: 'R' }, {}]) {
      const { boot } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => reply(200, body) })
      expect(withOutcome('failed').test(leaveUrl(boot)), JSON.stringify(body)).toBe(true)
      expect(store.local.getItem(OPS_KEY)).toBeNull()
    }
  })

  it('boot_handoffAnswerWithANullRenewableFails', async () => {
    const { boot } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => reply(200, { access_token: staffToken('n'), renewable: null }) })
    expect(withOutcome('failed').test(leaveUrl(boot))).toBe(true)
    expect(store.local.getItem(OPS_KEY)).toBeNull()
  })

  it('boot_refusedAppHandoffKeepsAStoredBorrowedRecord', async () => {
    const { boot } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), local: BORROWED, app: APP, answer: () => reply(400, { error: 'invalid or expired code' }) })
    expect(withOutcome('failed').test(leaveUrl(boot))).toBe(true)
    expect(store.local.getItem(OPS_KEY)).toBe(BORROWED)
  })

  it('boot_refreshAnswerWithoutAnAccessTokenStillFails', async () => {
    const { boot, calls } = await run({ local: OLD_RECORD, answer: () => reply(200, {}) })
    expect(withOutcome('failed').test(leaveUrl(boot))).toBe(true)
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(store.local.getItem(OPS_KEY)).toBe(OLD_RECORD)
  })

  it('boot_storedBorrowedRecordBouncesToTheApp', async () => {
    const { boot, calls } = await run({ local: BORROWED, app: APP, target: 'support' })
    expect(boot).toMatchObject({ kind: 'leave', replace: true })
    const m = appStart('support').exec(leaveUrl(boot))
    expect(m).not.toBeNull()
    expect(heldState()).toBe(m?.[1])
    expect(store.local.getItem(OPS_KEY)).toBe(BORROWED)
    expect(calls).toHaveLength(0)
  })

  it('boot_secondTabKeepsTheFirstTabsBorrowedRecord', async () => {
    const t1 = v3BorrowedRaw(staffToken('T1'))
    await run({ local: t1, app: APP })
    const tabB = installStorage({ local: { [OPS_KEY]: t1 } })
    const net = installFetch(() => new Error('unexpected request'))
    const boot = await resolveConsoleBoot({ search: '', storageKey: OPS_KEY, target: 'ops', gateway: GW, landing: LANDING, app: APP, now: NOW })
    expect(boot.kind).toBe('leave')
    expect(tabB.local.getItem(OPS_KEY)).toBe(t1)
    expect(net.calls).toHaveLength(0)
  })

  it('boot_storedBorrowedRecordWithoutAnAppLeavesToLanding', async () => {
    for (const o of [{ app: null }, { app: APP, gateway: null }]) {
      const { boot, calls } = await run({ local: BORROWED, ...o })
      expect(FRONT_DOOR.test(leaveUrl(boot)), JSON.stringify(o)).toBe(true)
      expect(store.local.getItem(OPS_KEY)).toBe(BORROWED)
      expect(calls).toHaveLength(0)
    }
  })

  it('boot_foreignHandoffWithABorrowedRecordBouncesToTheApp', async () => {
    const { boot, calls } = await run({ search: `?handoff=${CODE}`, local: BORROWED, app: APP })
    expect(boot).toMatchObject({ kind: 'leave', replace: true })
    expect(appStart('ops').test(leaveUrl(boot))).toBe(true)
    expect(calls).toHaveLength(0)
  })

  it('boot_usedAppHandoffCodeLeavesFailed', async () => {
    const { boot, calls } = await run({
      search: `?handoff=${CODE}`,
      state: stateRaw(STATE_A, NOW - 1000),
      app: APP,
      answer: () => reply(400, { error: 'invalid or expired code' }),
    })
    expect(withOutcome('failed').test(leaveUrl(boot))).toBe(true)
    expect(calls).toHaveLength(1)
    expect(store.local.getItem(OPS_KEY)).toBeNull()
  })
})

describe('cookie-held refresh token (SECAUD-17-10)', () => {
  const T = staffToken('v3')
  const T2 = staffToken('v3-renewed')
  const cookieMode = { credentials: 'include', headers: { 'x-auth-session': 'cookie' } }
  const held = { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000) }
  const sessionKeysHold = (needle: string) => [store.local, store.session].some((m) => [...Array(m.length).keys()].some((i) => (m.getItem(m.key(i) ?? '') ?? '').includes(needle)))

  it('handoff_landingRedeemsInCookieMode', async () => {
    const { boot, calls } = await run({ ...held, answer: () => reply(200, { access_token: T, renewable: true }) })
    expect(boot).toEqual({ kind: 'open', session: { token: T, borrowed: false } })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: EXCHANGE, method: 'POST', ...cookieMode })
    expect(calls[0]?.body).toEqual({ code: CODE, state: STATE_A })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: T })
    expect(sessionKeysHold('refresh_token')).toBe(false)
  })

  it('handoff_landingWithoutRenewableLeavesFailed', async () => {
    const control = await run({ ...held, answer: () => reply(200, { access_token: T, renewable: true }) })
    expect(control.boot.kind).toBe('open')
    const rows: [string, 'ops' | 'support', unknown][] = [
      ['absent, ops', 'ops', { access_token: T }],
      ['false, ops', 'ops', { access_token: T, renewable: false }],
      ['"true", ops', 'ops', { access_token: T, renewable: 'true' }],
      ['absent, support with no app', 'support', { access_token: T }],
      ['false, support with no app', 'support', { access_token: T, renewable: false }],
    ]
    for (const [name, target, body] of rows) {
      const { boot, calls } = await run({ ...held, target, answer: () => reply(200, body) })
      expect(calls, name).toHaveLength(1)
      expect(leaveUrl(boot), name).toMatch(new RegExp(`&console=${target}&signin=failed$`))
      expect(store.local.getItem(OPS_KEY), name).toBeNull()
    }
  })

  it('handoff_borrowedSupportOpensBorrowed', async () => {
    const { boot, calls } = await run({ ...held, target: 'support', app: APP, answer: () => reply(200, { access_token: T, renewable: false }) })
    expect(boot).toEqual({ kind: 'open', session: { token: T, borrowed: true } })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: EXCHANGE, ...cookieMode })
    expect(store.local.getItem(OPS_KEY)).toBe(v3BorrowedRaw(T))
  })

  it('reload_landingRecordRenewsThroughTheCookie', async () => {
    for (const target of ['ops', 'support'] as const) {
      const { boot, calls } = await run({ local: v3Raw(OLD_TOKEN), target, answer: () => reply(200, { access_token: T2 }) })
      expect(boot, target).toEqual({ kind: 'open', session: { token: T2, borrowed: false } })
      expect(calls, target).toHaveLength(1)
      expect(calls[0], target).toMatchObject({ url: REFRESH, method: 'POST', ...cookieMode })
      expect(calls[0]?.body, target).toBeUndefined()
      expect(store.local.getItem(OPS_KEY), target).toBe(v3Raw(T2))
    }
  })

  it('reload_refusedCookieClearsAndLeaves', async () => {
    for (const refuse of [() => reply(401, { error: 'invalid or expired refresh token' }), () => reply(400, { error: 'bad request' })]) {
      const { boot, calls } = await run({ local: v3Raw(OLD_TOKEN), answer: refuse })
      expect(calls).toHaveLength(1)
      expect(calls[0]).toMatchObject({ url: REFRESH, ...cookieMode })
      expect(store.local.getItem(OPS_KEY)).toBeNull()
      const url = leaveUrl(boot)
      expect(FRONT_DOOR.test(url), url).toBe(true)
      expect(heldState()).toBe(FRONT_DOOR.exec(url)?.[1])
    }
  })

  it('reload_unavailableKeepsTheRecord', async () => {
    const answers: [string, Response | Error][] = [
      ['502', reply(502, { error: 'renewal is unavailable' })],
      ['network error', new TypeError('Failed to fetch')],
      ['200 without an access token', reply(200, {})],
    ]
    for (const [name, answer] of answers) {
      const { boot, calls } = await run({ local: v3Raw(OLD_TOKEN), answer: () => answer })
      expect(calls, name).toHaveLength(1)
      expect(withOutcome('failed').test(leaveUrl(boot)), name).toBe(true)
      expect(store.local.getItem(OPS_KEY), name).toBe(v3Raw(OLD_TOKEN))
    }
  })

  it('legacyRecord_adoptsIntoTheCookieOnce', async () => {
    let midFlight: string | null = null
    const { boot, calls } = await run({
      local: recordRaw(OLD_TOKEN, 'R-old'),
      answer: () => {
        midFlight = store.local.getItem(OPS_KEY)
        return reply(200, { access_token: T2 })
      },
    })
    expect(boot).toEqual({ kind: 'open', session: { token: T2, borrowed: false } })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: REFRESH, method: 'POST', ...cookieMode })
    expect(calls[0]?.body).toEqual({ refresh_token: 'R-old' })
    // [legacy-adopt-order]: the legacy record stays until the adoption answers 200.
    expect(midFlight).toBe(recordRaw(OLD_TOKEN, 'R-old'))
    expect(store.local.getItem(OPS_KEY)).toBe(v3Raw(T2))
    expect(sessionKeysHold('R-old')).toBe(false)
  })

  it('legacyAdoption_transientFailureKeepsTheRecordAndTheNextBootRetries', async () => {
    const legacy = recordRaw(OLD_TOKEN, 'R-old')
    const answers: [string, () => Response | Error][] = [
      ['502', () => reply(502, { error: 'renewal is unavailable' })],
      ['network error', () => new TypeError('Failed to fetch')],
    ]
    for (const [name, fail] of answers) {
      const first = await run({ local: legacy, answer: fail })
      expect(first.calls, name).toHaveLength(1)
      expect(first.calls[0], name).toMatchObject({ url: REFRESH, method: 'POST', ...cookieMode })
      expect(first.calls[0]?.body, name).toEqual({ refresh_token: 'R-old' })
      expect(withOutcome('failed').test(leaveUrl(first.boot)), name).toBe(true)
      const kept = store.local.getItem(OPS_KEY)
      expect(kept, name).toBe(legacy)

      // A reload: the same storage, the gateway back.
      const second = await run({ local: kept ?? undefined, answer: () => reply(200, { access_token: T2 }) })
      expect(second.calls, name).toHaveLength(1)
      expect(second.calls[0], name).toMatchObject({ url: REFRESH, method: 'POST', ...cookieMode })
      expect(second.calls[0]?.body, name).toEqual({ refresh_token: 'R-old' })
      expect(second.boot, name).toEqual({ kind: 'open', session: { token: T2, borrowed: false } })
      expect(store.local.getItem(OPS_KEY), name).toBe(v3Raw(T2))
    }
  })

  it('legacyAdoption_refusedTokenRemovesTheRecordAndLeaves', async () => {
    const refusals = [() => reply(401, { error: 'invalid or expired refresh token' }), () => reply(400, { error: 'bad request' })]
    for (const refuse of refusals) {
      const { boot, calls } = await run({ local: recordRaw(OLD_TOKEN, 'R-old'), answer: refuse })
      expect(calls).toHaveLength(1)
      expect(calls[0]).toMatchObject({ url: REFRESH, method: 'POST', ...cookieMode })
      expect(calls[0]?.body).toEqual({ refresh_token: 'R-old' })
      expect(store.local.getItem(OPS_KEY)).toBeNull()
      const url = leaveUrl(boot)
      expect(FRONT_DOOR.test(url), url).toBe(true)
    }
  })

  it('boot_pendingSignOutIsSentFirst', async () => {
    const rows: [string, Response | Error, boolean][] = [
      ['204', reply(204), true],
      ['401', reply(401, { error: 'x' }), true],
      ['502', reply(502, { error: 'sign-out is unavailable' }), true],
      ['403', reply(403, { error: 'origin not allowed' }), false],
      ['network error', new TypeError('Failed to fetch'), false],
      ['timeout', timeoutError(), false],
    ]
    for (const [name, answer, removed] of rows) {
      const { boot, calls } = await run({ pending: true, answer: () => answer })
      expect(calls, name).toHaveLength(1)
      expect(calls[0], name).toMatchObject({ url: SIGN_OUT, method: 'POST', ...cookieMode })
      expect(calls[0]?.body, name).toBeUndefined()
      expect(FRONT_DOOR.test(leaveUrl(boot)), name).toBe(true)
      expect(store.local.getItem(PENDING), name).toBe(removed ? null : '1')
    }
  })

  it('boot_pendingSignOutWithAHandoffSendsNothing', async () => {
    const ok = await run({ ...held, pending: true, answer: () => reply(200, { access_token: T, renewable: true }) })
    expect(ok.boot.kind).toBe('open')
    expect(ok.calls.map((c) => c.url)).toEqual([EXCHANGE])
    expect(store.local.getItem(PENDING)).toBeNull()

    const refused = await run({ ...held, pending: true, answer: () => reply(400, { error: 'invalid or expired code' }) })
    expect(refused.calls.map((c) => c.url)).toEqual([EXCHANGE])
    expect(withOutcome('failed').test(leaveUrl(refused.boot))).toBe(true)
    expect(store.local.getItem(PENDING)).toBe('1')
  })

  it('boot_usedHandoffCodeWithAStoredV3RecordLeavesFailed', async () => {
    const { boot, calls } = await run({ ...held, local: v3Raw(T), answer: () => reply(400, { error: 'invalid or expired code' }) })
    expect(withOutcome('failed').test(leaveUrl(boot))).toBe(true)
    expect(calls.map((c) => c.url)).toEqual([EXCHANGE])
    expect(store.local.getItem(OPS_KEY)).toBe(v3Raw(T))
    // Kept means usable: the next plain load renews it through the cookie.
    const again = installFetch(() => reply(200, { access_token: T2 }))
    const next = await resolveConsoleBoot({ search: '', storageKey: OPS_KEY, target: 'ops', gateway: GW, landing: LANDING, now: NOW })
    expect(next).toEqual({ kind: 'open', session: { token: T2, borrowed: false } })
    expect(again.calls.map((c) => c.url)).toEqual([REFRESH])
  })
})
