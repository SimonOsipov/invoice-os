import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { signOutConsole } from './signOut'
import { GW, installFetch, installStorage, installWindow, LANDING, OPS_KEY, recordRaw, reply, spyTimeouts, staffToken, timeoutError } from './testkit'

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
    expect(t.net.calls[0]).toMatchObject({ url: SIGN_OUT, method: 'POST', body: { refresh_token: 'R-stored' } })
    expect(t.seenAtFetch).toEqual([RECORD])
    expect(t.log).toEqual(['fetch', 'remove', 'href'])
    expect(t.store.local.getItem(OPS_KEY)).toBeNull()
    expect(t.win.hrefWrites).toEqual([LANDING])
    expect(t.win.reload).not.toHaveBeenCalled()
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
    // The 204 has no body: read as JSON it would be malformed and warn.
    expect(warn).not.toHaveBeenCalled()
  })

  it('signOut_failureWarnsAndStillLeaves', async () => {
    const failures: [string, Response | Error][] = [
      ['502', reply(502, { error: 'sign-out is unavailable' })],
      ['503', reply(503, { error: 'unavailable' })],
      ['timeout', timeoutError()],
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

    const noRecord = setup(null, () => reply(204))
    await signOutConsole({ storageKey: OPS_KEY, gateway: GW, landing: LANDING })
    expect(noRecord.net.calls).toHaveLength(0)
    expect(noRecord.store.local.removeItem).toHaveBeenCalledWith(OPS_KEY)
    expect(noRecord.win.hrefWrites).toEqual([LANDING])
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
