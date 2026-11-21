// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { resolveConsoleBoot } from './boot'
import { StaffGate } from './StaffGate'
import {
  CODE,
  GW,
  installFetch,
  installStorage,
  LANDING,
  OPS_KEY,
  reply,
  spyTimeouts,
  SUPPORT_KEY,
  staffToken,
  STATE_A,
  STATE_KEY,
  stateRaw,
  v3Raw,
} from './testkit'

vi.mock('./boot', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./boot')>()
  return { ...actual, resolveConsoleBoot: vi.fn(actual.resolveConsoleBoot) }
})

const resolve = vi.mocked(resolveConsoleBoot)
const never = () => new Promise<never>(() => undefined)
const realLocation = window.location
let hrefWrites: string[]
let replaceWrites: string[]
let assignWrites: string[]
let reload: ReturnType<typeof vi.fn>
let store: ReturnType<typeof installStorage>

function gate(
  o: {
    landing?: string | null
    gateway?: string | null
    app?: string | null
    iap?: boolean
  } = {},
) {
  return (
    <StaffGate
      storageKey={OPS_KEY}
      target="ops"
      gateway={o.gateway === undefined ? GW : o.gateway}
      landing={o.landing === undefined ? LANDING : o.landing}
      app={o.app}
      iap={o.iap}
    >
      <div>console</div>
    </StaffGate>
  )
}

beforeEach(() => {
  hrefWrites = []
  replaceWrites = []
  assignWrites = []
  reload = vi.fn()
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
    replace(v: string) {
      replaceWrites.push(v)
    },
    assign(v: string) {
      assignWrites.push(v)
    },
    reload,
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

    resolve.mockImplementationOnce(async () => ({
      kind: 'open',
      session: null,
    }))
    const open = render(gate())
    expect(await screen.findByText('console')).toBeTruthy()
    expect(hrefWrites).toEqual([])
    open.unmount()

    const url = `${LANDING}/?state=abc&console=ops`
    resolve.mockImplementationOnce(async () => ({ kind: 'leave', url }))
    const leave = render(gate())
    await waitFor(() => expect(hrefWrites).toEqual([url]))
    expect(replaceWrites).toEqual([])
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
    store.local.setItem(OPS_KEY, v3Raw(staffToken('old')))
    const net = installFetch(() => reply(200, { access_token: renewed }))
    render(<StrictMode>{gate()}</StrictMode>)
    expect(await screen.findByText('console')).toBeTruthy()
    expect(net.calls.map((c) => c.url)).toEqual([`${GW}/auth/refresh`])
    expect(hrefWrites).toEqual([])
  })

  it('StaffGate_standaloneRendersWithNoRequest', async () => {
    const net = installFetch(() => new Error('unexpected request'))
    store.local.setItem(OPS_KEY, v3Raw(staffToken('old')))
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
    expect(replaceWrites).toEqual([])
    expect(resolve).toHaveBeenCalledTimes(1)
  })

  it('StaffGate_strictModeRedeemsAHandoffOnce', async () => {
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    store.session.setItem(STATE_KEY, stateRaw(STATE_A, Date.now() - 1000))
    const access = staffToken('exchanged')
    const net = installFetch(() => reply(200, { access_token: access, renewable: true }))
    render(<StrictMode>{gate()}</StrictMode>)
    expect(await screen.findByText('console')).toBeTruthy()
    expect(net.calls.map((c) => c.url)).toEqual([`${GW}/auth/exchange`])
    expect(net.calls[0]?.body).toEqual({ code: CODE, state: STATE_A })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({
      v: 3,
      token: access,
    })
    expect(realLocation.search).toBe('')
    expect(hrefWrites).toEqual([])
  })

  it('StaffGate_handoffLeavesNoRefreshTokenInStorage', async () => {
    window.history.replaceState(null, '', `/?handoff=${CODE}`)
    store.session.setItem(STATE_KEY, stateRaw(STATE_A, Date.now() - 1000))
    const access = staffToken('exchanged')
    installFetch(() => reply(200, { access_token: access, renewable: true }))
    render(gate())
    expect(await screen.findByText('console')).toBeTruthy()
    const values = [store.local, store.session].flatMap((m) => [...Array(m.length).keys()].map((i) => m.getItem(m.key(i) ?? '') ?? ''))
    expect(values.some((v) => v.includes(access))).toBe(true)
    expect(values.filter((v) => v.includes('refresh_token'))).toEqual([])
  })

  it('StaffGate_authStart_replacesTheEntry', async () => {
    window.history.replaceState(null, '', '/?auth=start')
    const net = installFetch(() => new Error('unexpected request'))
    render(gate())
    await waitFor(() => expect(replaceWrites).toHaveLength(1))
    expect(hrefWrites).toEqual([])
    expect(replaceWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=ops&signin=ready$`))
    expect(realLocation.search).toBe('')
    expect(net.calls).toHaveLength(0)
    expect(screen.queryByText('console')).toBeNull()
  })

  it('StaffGate_authStart_replacesTheEntry_supportTarget', async () => {
    window.history.replaceState(null, '', '/?auth=start')
    render(
      <StaffGate storageKey={SUPPORT_KEY} target="support" gateway={GW} landing={LANDING}>
        <div>console</div>
      </StaffGate>,
    )
    await waitFor(() => expect(replaceWrites).toHaveLength(1))
    expect(hrefWrites).toEqual([])
    expect(replaceWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=support&signin=ready$`))
  })

  it('staffGate_withoutAppTreatsAuthAppAsStart', async () => {
    window.history.replaceState(null, '', '/?auth=app')
    render(gate())
    await waitFor(() => expect(replaceWrites).toHaveLength(1))
    expect(replaceWrites[0]).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=ops&signin=ready$`))
    expect(hrefWrites).toEqual([])
  })
})

describe('StaffGate IAP mode (CSSO-01-06)', () => {
  const SIGN_OUT = `${GW}/auth/sign-out`
  const MARKER = `${OPS_KEY}:signed-out`

  it('StaffGate_iap_showsOpeningThenTheConsole', async () => {
    let done!: (b: { kind: 'open'; session: null }) => void
    resolve.mockImplementationOnce(() => new Promise((r) => (done = r)))
    const { container } = render(gate({ iap: true }))
    expect(screen.getByRole('status').textContent).toBe('Opening the console…')
    expect(screen.queryByText('console')).toBeNull()
    done({ kind: 'open', session: null })
    expect(await screen.findByText('console')).toBeTruthy()
    expect(container.textContent).toBe('console')
  })

  it('StaffGate_iap_failedOffersTryAgain', async () => {
    resolve.mockImplementationOnce(async () => ({ kind: 'failed' }))
    render(gate({ iap: true }))
    expect((await screen.findByRole('alert')).textContent).toBe('Could not open the console.')
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('StaffGate_iap_noStaffScreen', async () => {
    resolve.mockImplementationOnce(async () => ({
      kind: 'no-staff',
      email: 'x@y',
    }))
    const { container } = render(gate({ iap: true }))
    expect((await screen.findByRole('heading')).textContent).toBe('No staff account for this Google account')
    expect(screen.getByText('x@y is not an ASComply staff account. Ask an ASComply admin to give this email staff access.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Use a different Google account' }).getAttribute('href')).toBe('/_gcp_iap/clear_login_cookie')
    expect(container.querySelector('input, form')).toBeNull()
  })

  it('StaffGate_iap_signedOutReopensOnlyOnPress', async () => {
    store.local.setItem(MARKER, '1')
    const net = installFetch(() => new Error('unexpected request'))
    render(gate({ iap: true }))
    expect(await screen.findByText('You are signed out of the console.')).toBeTruthy()
    expect(net.calls).toHaveLength(0)
    expect(reload).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Open the console' }))
    expect(store.local.getItem(MARKER)).toBeNull()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('StaffGate_iap_differentAccountClickSwitches', async () => {
    resolve.mockImplementationOnce(async () => ({
      kind: 'no-staff',
      email: 'x@y',
    }))
    store.local.setItem(OPS_KEY, v3Raw(staffToken('s')))
    const seen: string[] = []
    const net = installFetch((c) => {
      seen.push(`fetch ${c.url}`)
      return reply(204)
    })
    render(gate({ iap: true }))
    const link = await screen.findByRole('link', {
      name: 'Use a different Google account',
    })
    const notPrevented = fireEvent.click(link)
    expect(notPrevented).toBe(false)
    await waitFor(() => expect(assignWrites).toEqual(['/_gcp_iap/clear_login_cookie']))
    expect(net.calls.map((c) => c.url)).toEqual([SIGN_OUT])
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(store.local.getItem(MARKER)).toBeNull()
  })
})
