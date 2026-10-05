// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { resolveConsoleBoot } from './boot'
import { StaffGate } from './StaffGate'
import { CODE, GW, installFetch, installStorage, LANDING, OPS_KEY, recordRaw, reply, spyTimeouts, staffToken, STATE_A, STATE_KEY, stateRaw } from './testkit'

vi.mock('./boot', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./boot')>()
  return { ...actual, resolveConsoleBoot: vi.fn(actual.resolveConsoleBoot) }
})

const resolve = vi.mocked(resolveConsoleBoot)
const never = () => new Promise<never>(() => undefined)
const realLocation = window.location
let hrefWrites: string[]
let store: ReturnType<typeof installStorage>

function gate(o: { landing?: string | null; gateway?: string | null } = {}) {
  return (
    <StaffGate storageKey={OPS_KEY} target="ops" gateway={o.gateway === undefined ? GW : o.gateway} landing={o.landing === undefined ? LANDING : o.landing}>
      <div>console</div>
    </StaffGate>
  )
}

beforeEach(() => {
  hrefWrites = []
  vi.spyOn(console, 'warn').mockImplementation(() => undefined)
  spyTimeouts()
  // jsdom cannot navigate: capture href writes, and read search and pathname from the real location.
  vi.stubGlobal('location', {
    get search() {
      return realLocation.search
    },
    get pathname() {
      return realLocation.pathname
    },
    get href() {
      return realLocation.href
    },
    set href(v: string) {
      hrefWrites.push(v)
    },
  })
  window.history.replaceState(null, '', '/')
  // Node 26 exposes an unusable localStorage global that shadows jsdom's.
  store = installStorage()
})

afterEach(() => {
  cleanup()
  resolve.mockReset()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('StaffGate (AC-11)', () => {
  it('StaffGate_rendersChildrenOnlyWhenOpen', async () => {
    resolve.mockImplementationOnce(never)
    const pending = render(gate())
    expect(resolve).toHaveBeenCalledTimes(1)
    expect(pending.container.innerHTML).toBe('')
    expect(hrefWrites).toEqual([])
    pending.unmount()

    resolve.mockImplementationOnce(async () => ({ kind: 'open', session: null }))
    const open = render(gate())
    expect(await screen.findByText('console')).toBeTruthy()
    expect(hrefWrites).toEqual([])
    open.unmount()

    const url = `${LANDING}/?state=abc&console=ops`
    resolve.mockImplementationOnce(async () => ({ kind: 'leave', url }))
    const leave = render(gate())
    await waitFor(() => expect(hrefWrites).toEqual([url]))
    expect(screen.queryByText('console')).toBeNull()
    expect(leave.container.innerHTML).toBe('')
  })

  it('StaffGate_stripsHandoffAndAuthAtMount', () => {
    window.history.replaceState(null, '', `/?handoff=${CODE}&auth=start&x=1#frag`)
    resolve.mockImplementationOnce(never)
    const replace = vi.spyOn(window.history, 'replaceState')
    render(gate())
    expect(replace).toHaveBeenCalled()
    expect(realLocation.search).toBe('?x=1')
    expect(realLocation.pathname).toBe('/')
    expect(realLocation.hash).toBe('#frag')
    // The boot still gets the code: the strip must not run before the search is read.
    expect(resolve).toHaveBeenCalledTimes(1)
    expect(resolve.mock.calls[0]?.[0].search).toBe(`?handoff=${CODE}&auth=start&x=1`)
  })

  it('StaffGate_strictModePostsOnce', async () => {
    const renewed = staffToken('renewed')
    store.local.setItem(OPS_KEY, recordRaw(staffToken('old'), 'R-old'))
    const net = installFetch(() => reply(200, { access_token: renewed, refresh_token: 'R-renewed' }))
    render(<StrictMode>{gate()}</StrictMode>)
    expect(await screen.findByText('console')).toBeTruthy()
    expect(net.calls.map((c) => c.url)).toEqual([`${GW}/auth/refresh`])
    expect(hrefWrites).toEqual([])
  })

  it('StaffGate_standaloneRendersWithNoRequest', async () => {
    const net = installFetch(() => new Error('unexpected request'))
    store.local.setItem(OPS_KEY, recordRaw(staffToken('old'), 'R-old'))
    render(gate({ landing: null }))
    expect(await screen.findByText('console')).toBeTruthy()
    expect(net.calls).toHaveLength(0)
    expect(hrefWrites).toEqual([])
  })

  it('StaffGate_strictModeLeavesOnce', async () => {
    const url = `${LANDING}/?state=abc&console=ops`
    resolve.mockImplementation(async () => ({ kind: 'leave', url }))
    render(<StrictMode>{gate()}</StrictMode>)
    await waitFor(() => expect(hrefWrites).toEqual([url]))
    await new Promise((r) => setTimeout(r, 20))
    expect(hrefWrites).toEqual([url])
    expect(resolve).toHaveBeenCalledTimes(1)
  })

  it('StaffGate_strictModeRedeemsAHandoffOnce', async () => {
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    store.session.setItem(STATE_KEY, stateRaw(STATE_A, Date.now() - 1000))
    const access = staffToken('exchanged')
    const net = installFetch(() => reply(200, { access_token: access, refresh_token: 'R-exchanged' }))
    render(<StrictMode>{gate()}</StrictMode>)
    expect(await screen.findByText('console')).toBeTruthy()
    expect(net.calls.map((c) => c.url)).toEqual([`${GW}/auth/exchange`])
    expect(net.calls[0]?.body).toEqual({ code: CODE, state: STATE_A })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: access, refresh_token: 'R-exchanged' })
    expect(realLocation.search).toBe('')
    expect(hrefWrites).toEqual([])
  })

  it('StaffGate_authStartLeavesForTheLandingReady', async () => {
    window.history.replaceState(null, '', '/?auth=start')
    const net = installFetch(() => new Error('unexpected request'))
    render(gate())
    await waitFor(() => expect(hrefWrites).toHaveLength(1))
    expect(hrefWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=ops&signin=ready$`))
    expect(realLocation.search).toBe('')
    expect(net.calls).toHaveLength(0)
    expect(screen.queryByText('console')).toBeNull()
  })
})
