import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { resolveConsoleBoot, type ConsoleBoot } from './boot'
import {
  b64url,
  BASE64URL_43_RE,
  type Call,
  CODE,
  customerToken,
  GW,
  installFetch,
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
} from './testkit'

const TIMEOUT_MS = 15000 // AbortSignal.timeout in redeemHandoff and RENEW_TIMEOUT_MS (frontend/app/src/lib)
const EXCHANGE = `${GW}/auth/exchange`
const REFRESH = `${GW}/auth/refresh`
const FRONT_DOOR = new RegExp(`^${LANDING}/\\?state=([A-Za-z0-9_-]{43})&console=ops$`)
const withOutcome = (o: string) => new RegExp(`^${LANDING}/\\?state=([A-Za-z0-9_-]{43})&console=ops&signin=${o}$`)

const OLD_TOKEN = staffToken('old', NOW / 1000 + 10 * 365 * 24 * 3600)
const OLD_RECORD = recordRaw(OLD_TOKEN, 'R-old')
const pair = (token: string, refresh: string) => reply(200, { access_token: token, refresh_token: refresh })

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
}

async function run(o: Setup = {}) {
  store = installStorage({
    local: o.local === undefined ? {} : { [OPS_KEY]: o.local },
    session: o.state === undefined ? {} : { [STATE_KEY]: o.state },
  })
  const net = installFetch((c) => (o.answer ?? (() => new Error('unexpected request')))(c))
  const boot = await resolveConsoleBoot({
    search: o.search ?? '',
    storageKey: OPS_KEY,
    target: o.target ?? 'ops',
    gateway: o.gateway === undefined ? GW : o.gateway,
    landing: o.landing === undefined ? LANDING : o.landing,
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

  it('boot_otherLeaves_haveNoReplace', async () => {
    const scenarios: Record<string, Setup> = {
      'no session': {},
      'failed hand-off': { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => reply(503) },
      'not staff': { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => pair(customerToken('x'), 'R-x') },
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
      answer: () => pair(access, 'R-exchanged'),
    })
    expect(boot).toEqual({ kind: 'open', session: { token: access, refreshToken: 'R-exchanged' } })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: EXCHANGE, method: 'POST' })
    expect(calls[0]?.body).toEqual({ code: CODE, state: STATE_A })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: access, refresh_token: 'R-exchanged' })
    expect(store.session.getItem(STATE_KEY)).toBeNull()
    expect(timeouts).toHaveBeenCalledTimes(1)
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
    expect(calls[0]?.signal).toBe(timeouts.mock.results[0]?.value)
  })

  it('boot_handoffNonStaffStoresNothing', async () => {
    const { boot, calls } = await run({
      search: `?handoff=${CODE}`,
      state: stateRaw(STATE_A, NOW - 1000),
      answer: () => pair(customerToken('exchanged'), 'R-exchanged'),
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
    const stored = recordRaw(OLD_TOKEN, 'R-keep')
    const failures: [string, Response | Error][] = [
      ['exchange 400', reply(400, { error: 'invalid or expired code' })],
      ['network', new TypeError('Failed to fetch')],
      ['timeout', timeoutError()],
      ['429', reply(429, { error: 'too many requests' })],
      ['500', reply(500, { error: 'internal' })],
      ['200 with only an access token', reply(200, { access_token: staffToken('x') })],
      ['200 with an empty refresh token', reply(200, { access_token: staffToken('x'), refresh_token: '' })],
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
      const { boot, calls } = await run({ ...setup, local: OLD_RECORD, answer: () => pair(renewed, 'R-renewed') })
      expect(calls.filter((c) => c.url === EXCHANGE), name).toHaveLength(0)
      expect(calls.filter((c) => c.url === REFRESH), name).toHaveLength(1)
      expect(calls[0]?.body, name).toEqual({ refresh_token: 'R-old' })
      expect(boot, name).toEqual({ kind: 'open', session: { token: renewed, refreshToken: 'R-renewed' } })
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
    const { boot, calls } = await run({ local: OLD_RECORD, answer: () => pair(renewed, 'R-renewed') })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: REFRESH, method: 'POST' })
    expect(calls[0]?.body).toEqual({ refresh_token: 'R-old' })
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, refreshToken: 'R-renewed' } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: renewed, refresh_token: 'R-renewed' })
    expect(timeouts).toHaveBeenCalledTimes(1)
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
    expect(calls[0]?.signal).toBe(timeouts.mock.results[0]?.value)
  })

  it('boot_expiredStoredTokenRenews', async () => {
    const expired = staffToken('expired', NOW / 1000 - 3600)
    const renewed = staffToken('renewed')
    const { boot, calls } = await run({ local: recordRaw(expired, 'R-old'), answer: () => pair(renewed, 'R-renewed') })
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, refreshToken: 'R-renewed' } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: renewed, refresh_token: 'R-renewed' })
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
    const { boot, calls } = await run({ local: OLD_RECORD, answer: () => pair(customerToken('renewed'), 'R-renewed') })
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
      ['200 with only an access token', reply(200, { access_token: staffToken('renewed') })],
      ['200 with only a refresh token', reply(200, { refresh_token: 'R-new' })],
      ['200 with an empty access token', reply(200, { access_token: '', refresh_token: 'R-new' })],
      ['200 with a non-string access token', reply(200, { access_token: 1, refresh_token: 'R-new' })],
      ['200 with a non-string refresh token', reply(200, { access_token: staffToken('renewed'), refresh_token: 5 })],
      ['200 JSON null', reply(200, null)],
      ['204 with no body', reply(204)],
      ['200 with an empty refresh token', reply(200, { access_token: staffToken('renewed'), refresh_token: '' })],
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
      { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => pair(customerToken('x'), refresh) },
      { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => reply(400, { error: access }) },
      { search: `?handoff=${CODE}`, gateway: null, state: stateRaw(STATE_A, NOW - 1000) },
      { search: '?auth=start', local: OLD_RECORD },
      { local: OLD_RECORD, answer: () => reply(401, { error: 'invalid or expired refresh token' }) },
      { local: OLD_RECORD, answer: () => pair(customerToken('x'), refresh) },
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
      answer: () => pair(staffToken('x'), 'R-x'),
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
      const { boot, calls } = await run({ search, local: OLD_RECORD, answer: () => pair(staffToken('renewed'), 'R-renewed') })
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
      answer: () => pair(access, 'R-exchanged'),
    })
    expect(calls.map((c) => c.url)).toEqual([EXCHANGE])
    expect(boot).toEqual({ kind: 'open', session: { token: access, refreshToken: 'R-exchanged' } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: access, refresh_token: 'R-exchanged' })
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
    const local = { [OPS_KEY]: OLD_RECORD, [SUPPORT_KEY]: recordRaw(supportToken, 'R-support') }
    const base = { search: '', target: 'support' as const, gateway: GW, landing: LANDING, now: NOW }

    store = installStorage({ local })
    let net = installFetch(() => pair(renewed, 'R-support-new'))
    const opened = await resolveConsoleBoot({ ...base, storageKey: SUPPORT_KEY })
    expect(opened).toEqual({ kind: 'open', session: { token: renewed, refreshToken: 'R-support-new' } })
    expect(net.calls[0]?.body).toEqual({ refresh_token: 'R-support' })
    expect(JSON.parse(store.local.getItem(SUPPORT_KEY) ?? 'null')).toEqual({ v: 2, token: renewed, refresh_token: 'R-support-new' })
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
    installFetch(() => pair(access, 'R-exchanged'))
    await expect(resolveConsoleBoot({ ...base, search: `?handoff=${CODE}` })).resolves.toEqual({
      kind: 'open',
      session: { token: access, refreshToken: 'R-exchanged' },
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
