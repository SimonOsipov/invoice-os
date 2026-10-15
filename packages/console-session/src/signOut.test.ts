import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { expireConsole, signOutConsole } from './signOut'
import {
  APP,
  borrowedRaw,
  BASE64URL_43_RE,
  GW,
  installFetch,
  installStorage,
  installWindow,
  LANDING,
  OPS_KEY,
  recordRaw,
  reply,
  spyTimeouts,
  staffToken,
  STATE_KEY,
  SUPPORT_KEY,
  throwingStorage,
  timeoutError,
} from './testkit'

const TIMEOUT_MS = 5000 // REVOKE_TIMEOUT_MS (frontend/app/src/lib/revoke.ts)
const SIGN_OUT = `${GW}/auth/sign-out`
const RECORD = recordRaw(staffToken('stored'), 'R-stored')

let warn: ReturnType<typeof vi.spyOn>
let timeouts: ReturnType<typeof spyTimeouts>

beforeEach(() => {
  warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
  timeouts = spyTimeouts()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// Logs the order of fetch, record removal and navigation; the handler also reads the record at request time.
function setup(local: string | null, answer: () => Response | Error) {
  const log: string[] = []
  const store = installStorage({ local: local === null ? {} : { [OPS_KEY]: local } })
  const removeItem = store.local.removeItem.getMockImplementation()
  store.local.removeItem.mockImplementation((k: string) => {
    log.push('remove')
    removeItem?.(k)
  })
  const seenAtFetch: (string | null)[] = []
  const net = installFetch(() => {
    log.push('fetch')
    seenAtFetch.push(store.local.getItem(OPS_KEY))
    return answer()
  })
  const win = installWindow(log)
  return { log, store, net, win, seenAtFetch }
}

describe('signOutConsole (AC-12)', () => {
  it('signOut_revokesThenClearsThenLeaves', async () => {
    const t = setup(RECORD, () => reply(204))
    await signOutConsole({ storageKey: OPS_KEY, gateway: GW, landing: LANDING })
    expect(t.net.calls).toHaveLength(1)
    expect(t.net.calls[0]).toMatchObject({ url: SIGN_OUT, method: 'POST' })
    expect(t.net.calls[0]?.body).toEqual({ refresh_token: 'R-stored' })
    expect(t.seenAtFetch).toEqual([RECORD])
    expect(t.log).toEqual(['fetch', 'remove', 'href'])
    expect(t.store.local.getItem(OPS_KEY)).toBeNull()
    expect(t.win.hrefWrites).toEqual([LANDING])
    expect(t.win.reload).not.toHaveBeenCalled()
    expect(timeouts).toHaveBeenCalledTimes(1)
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
    expect(t.net.calls[0]?.signal).toBe(timeouts.mock.results[0]?.value)
    expect(warn).not.toHaveBeenCalled()
  })

  it('signOut_failureWarnsAndStillLeaves', async () => {
    const failures: [string, Response | Error][] = [
      ['502', reply(502, { error: 'sign-out is unavailable' })],
      ['503', reply(503, { error: 'unavailable' })],
      ['timeout', timeoutError()],
      ['network', new TypeError('Failed to fetch')],
      ['401', reply(401, { error: 'invalid or expired refresh token' })],
      ['500', reply(500)],
    ]
    for (const [name, answer] of failures) {
      warn.mockClear()
      const t = setup(RECORD, () => answer)
      await expect(signOutConsole({ storageKey: OPS_KEY, gateway: GW, landing: LANDING }), name).resolves.toBeUndefined()
      expect(t.net.calls, name).toHaveLength(1)
      expect(warn, name).toHaveBeenCalledTimes(1)
      expect(t.store.local.getItem(OPS_KEY), name).toBeNull()
      expect(t.win.hrefWrites, name).toEqual([LANDING])
    }
  })

  it('signOut_withoutGatewayOrRecordSendsNothing', async () => {
    const noGateway = setup(RECORD, () => reply(204))
    await signOutConsole({ storageKey: OPS_KEY, gateway: null, landing: LANDING })
    expect(noGateway.net.calls).toHaveLength(0)
    expect(noGateway.store.local.getItem(OPS_KEY)).toBeNull()
    expect(noGateway.win.hrefWrites).toEqual([LANDING])
    expect(warn).not.toHaveBeenCalled()

    const noRecord = setup(null, () => reply(204))
    await signOutConsole({ storageKey: OPS_KEY, gateway: GW, landing: LANDING })
    expect(noRecord.net.calls).toHaveLength(0)
    expect(noRecord.store.local.removeItem).toHaveBeenCalledWith(OPS_KEY)
    expect(noRecord.win.hrefWrites).toEqual([LANDING])
    expect(warn).not.toHaveBeenCalled()
  })

  it('signOut_standaloneClearsAndReloads', async () => {
    const t = setup(RECORD, () => reply(204))
    await signOutConsole({ storageKey: OPS_KEY, gateway: null, landing: null })
    expect(t.store.local.getItem(OPS_KEY)).toBeNull()
    expect(t.win.reload).toHaveBeenCalledTimes(1)
    expect(t.win.hrefWrites).toEqual([])
    expect(t.net.calls).toHaveLength(0)
  })
})

describe('signOutConsole edges (AC-12)', () => {
  it('signOut_standaloneWithAGatewayAndRecordStillRevokes', async () => {
    const t = setup(RECORD, () => reply(204))
    await signOutConsole({ storageKey: OPS_KEY, gateway: GW, landing: null })
    expect(t.net.calls).toHaveLength(1)
    expect(t.net.calls[0]?.body).toEqual({ refresh_token: 'R-stored' })
    expect(t.log).toEqual(['fetch', 'remove', 'reload'])
    expect(t.win.hrefWrites).toEqual([])
  })

  it('signOut_supportConsoleRevokesAndClearsItsOwnRecord', async () => {
    const t = setup(RECORD, () => reply(204))
    t.store.local.setItem(SUPPORT_KEY, recordRaw(staffToken('support'), 'R-support'))
    await signOutConsole({ storageKey: SUPPORT_KEY, gateway: GW, landing: LANDING })
    expect(t.net.calls.map((c) => c.body)).toEqual([{ refresh_token: 'R-support' }])
    expect(t.store.local.getItem(SUPPORT_KEY)).toBeNull()
    expect(t.store.local.getItem(OPS_KEY)).toBe(RECORD)
  })

  it('signOut_unusableRecordSendsNothingAndStillLeaves', async () => {
    for (const raw of [JSON.stringify({ v: 1, operator: 'developer' }), '{', JSON.stringify({ v: 2, token: 'T', refresh_token: '' })]) {
      warn.mockClear()
      const t = setup(raw, () => reply(204))
      await signOutConsole({ storageKey: OPS_KEY, gateway: GW, landing: LANDING })
      expect(t.net.calls, raw).toHaveLength(0)
      expect(warn, raw).toHaveBeenCalledTimes(1)
      expect(t.store.local.getItem(OPS_KEY), raw).toBeNull()
      expect(t.win.hrefWrites, raw).toEqual([LANDING])
    }
  })

  it('signOut_unreadableStorageNeverBlocksTheLeave', async () => {
    const t = setup(null, () => reply(204))
    vi.stubGlobal('localStorage', throwingStorage())
    await expect(signOutConsole({ storageKey: OPS_KEY, gateway: GW, landing: LANDING })).resolves.toBeUndefined()
    expect(t.net.calls).toHaveLength(0)
    expect(t.win.hrefWrites).toEqual([LANDING])
    expect(warn.mock.calls.length).toBeGreaterThan(0)
  })
})

describe('borrowed session (SUPP-11-02)', () => {
  const BORROWED = borrowedRaw(staffToken('borrowed'))
  const o = { storageKey: OPS_KEY, gateway: GW, landing: LANDING }

  it('signOut_borrowedSessionSendsNoRevoke', async () => {
    const t = setup(BORROWED, () => reply(204))
    await signOutConsole(o)
    expect(t.net.calls).toHaveLength(0)
    expect(t.store.local.getItem(OPS_KEY)).toBeNull()
    expect(t.win.hrefWrites).toEqual([LANDING])
  })

  it('expire_borrowedSessionLeavesThroughTheApp', async () => {
    const t = setup(BORROWED, () => reply(204))
    await expireConsole({ ...o, app: APP, target: 'support' })
    expect(t.net.calls).toHaveLength(0)
    expect(t.win.replaceWrites).toHaveLength(1)
    const m = new RegExp(`^${APP}/\\?auth=console&console=support&state=(.+)$`).exec(t.win.replaceWrites[0] ?? '')
    expect(m?.[1]).toMatch(BASE64URL_43_RE)
    expect(JSON.parse(t.store.session.getItem(STATE_KEY) ?? 'null')).toMatchObject({ s: m?.[1] })
    expect(t.store.local.getItem(OPS_KEY)).toBe(BORROWED)
    expect(t.win.hrefWrites).toEqual([])
  })

  it('expire_landingSessionSignsOutAsToday', async () => {
    const landing = setup(RECORD, () => reply(204))
    await expireConsole({ ...o, app: APP, target: 'support' })
    expect(landing.net.calls.map((c) => c.body)).toEqual([{ refresh_token: 'R-stored' }])
    expect(landing.store.local.getItem(OPS_KEY)).toBeNull()
    expect(landing.win.hrefWrites).toEqual([LANDING])
    expect(landing.win.replaceWrites).toEqual([])

    const noApp = setup(BORROWED, () => reply(204))
    await expireConsole({ ...o, app: null, target: 'support' })
    expect(noApp.net.calls).toHaveLength(0)
    expect(noApp.store.local.getItem(OPS_KEY)).toBeNull()
    expect(noApp.win.hrefWrites).toEqual([LANDING])
  })
})
