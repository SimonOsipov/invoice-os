import { StrictMode, isValidElement, type ReactElement } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const T = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'

const h = vi.hoisted(() => {
  const render = vi.fn()
  // What the address bar and sessionStorage held at the moment a module evaluated or a call ran.
  const seen: Record<string, { url: string; stored: string | null }> = {}
  const snapshot = () => {
    const g = globalThis as unknown as { location: Location; sessionStorage: Storage }
    return { url: g.location.pathname + g.location.search + g.location.hash, stored: g.sessionStorage.getItem('ascomply.inviteToken') }
  }
  return {
    render,
    seen,
    instrumentEvaluated: vi.fn(),
    appEvaluated: vi.fn(),
    inviteLinkEvaluated: vi.fn(),
    bootAnalytics: vi.fn(() => {
      seen.atAnalytics = snapshot()
    }),
    createRoot: vi.fn(() => ({ render })),
    snapshot,
  }
})

// doMock, not mock: a factory runs again only after resetModules plus a fresh registration.
function mockModules() {
  vi.doMock('./instrument', () => {
    h.instrumentEvaluated()
    h.seen.atInstrument = h.snapshot()
    return {}
  })
  vi.doMock('@invoice-os/monitoring', () => ({ CrashBoundary: () => null }))
  vi.doMock('react-dom/client', () => ({ createRoot: h.createRoot }))
  vi.doMock('./App', () => {
    h.appEvaluated()
    return { default: () => null }
  })
  vi.doMock('./components/InvitePage', () => ({ InvitePage: () => null }))
  // The real module, so its evaluation-time capture runs; the spy records that it has finished.
  vi.doMock('./inviteLink', async (importOriginal) => {
    const real = await importOriginal<typeof import('./inviteLink')>()
    h.inviteLinkEvaluated()
    return real
  })
  vi.doMock('./analytics', () => ({ bootAnalytics: h.bootAnalytics }))
}

// Browser globals for the node environment; replaceState moves the fake address bar as the real one does.
function browse(url: string) {
  const at = new URL(url, 'https://www.ascomply.com')
  const loc = { pathname: at.pathname, search: at.search, hash: at.hash }
  const history = {
    replaceState: vi.fn((_state: unknown, _title: string, next: string) => {
      const n = new URL(next, 'https://www.ascomply.com')
      Object.assign(loc, { pathname: n.pathname, search: n.search, hash: n.hash })
    }),
  }
  const map = new Map<string, string>()
  const sessionStorage = { getItem: (k: string) => map.get(k) ?? null, setItem: (k: string, v: string) => void map.set(k, v) }
  vi.stubGlobal('document', { getElementById: () => ({}) })
  vi.stubGlobal('location', loc)
  vi.stubGlobal('history', history)
  vi.stubGlobal('sessionStorage', sessionStorage)
  return { loc, history, map }
}

// The mocks and BrandMark are re-evaluated by resetModules, so every comparison uses this run's instances.
async function bootMain(url: string) {
  vi.clearAllMocks()
  for (const k of Object.keys(h.seen)) delete h.seen[k]
  vi.resetModules()
  mockModules()
  const browser = browse(url)
  await import('./main')
  return {
    ...browser,
    App: (await import('./App')).default,
    InvitePage: (await import('./components/InvitePage')).InvitePage,
    CrashBoundary: (await import('@invoice-os/monitoring')).CrashBoundary,
    BrandMark: (await import('./icons')).BrandMark,
  }
}

type Root = ReactElement<{ children: ReactElement<{ brand: unknown; children: ReactElement<{ token?: string | null }> }> }>

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('main', () => {
  it('main_startsMonitoringBeforeTheAppGraphWrapsTheAppAndKeepsAnalytics', async () => {
    const { App, CrashBoundary, BrandMark } = await bootMain('/')

    expect(h.instrumentEvaluated).toHaveBeenCalledTimes(1)
    expect(h.createRoot).toHaveBeenCalledTimes(1)
    expect(h.instrumentEvaluated.mock.invocationCallOrder[0]).toBeLessThan(h.createRoot.mock.invocationCallOrder[0])

    // Imports are hoisted, so only a side-effect module imported first evaluates before App's graph.
    expect(h.appEvaluated).toHaveBeenCalledTimes(1)
    expect(h.instrumentEvaluated.mock.invocationCallOrder[0]).toBeLessThan(h.appEvaluated.mock.invocationCallOrder[0])

    expect(h.render).toHaveBeenCalledTimes(1)
    const root = h.render.mock.calls[0][0] as Root
    expect(root.type).toBe(StrictMode)
    const boundary = root.props.children
    expect(boundary.type).toBe(CrashBoundary)
    expect(isValidElement(boundary.props.brand) && boundary.props.brand.type).toBe(BrandMark)
    expect(boundary.props.children.type).toBe(App)

    expect(h.bootAnalytics).toHaveBeenCalledTimes(1)
    expect(h.render.mock.invocationCallOrder[0]).toBeLessThan(h.bootAnalytics.mock.invocationCallOrder[0])
  })

  it('main_capturesTheInviteBeforeMonitoringAndAnalytics', async () => {
    await bootMain(`/invite?x=1#token=${T}`)

    expect(h.inviteLinkEvaluated).toHaveBeenCalledTimes(1)
    expect(h.instrumentEvaluated).toHaveBeenCalledTimes(1)
    expect(h.bootAnalytics).toHaveBeenCalledTimes(1)
    expect(h.inviteLinkEvaluated.mock.invocationCallOrder[0]).toBeLessThan(h.instrumentEvaluated.mock.invocationCallOrder[0])
    expect(h.inviteLinkEvaluated.mock.invocationCallOrder[0]).toBeLessThan(h.bootAnalytics.mock.invocationCallOrder[0])
  })

  // The oracle that replaces RESEND-05 D27's deployed check: the fork loads no GA4 or Sentry (P16).
  it('main_hasStrippedTheFragmentWhenMonitoringAndAnalyticsRun', async () => {
    const { history } = await bootMain(`/invite?x=1#token=${T}`)

    expect(h.seen.atInstrument, 'control: ./instrument evaluated').toBeDefined()
    expect(h.seen.atAnalytics, 'control: bootAnalytics ran').toBeDefined()
    expect(h.seen.atInstrument).toEqual({ url: '/invite?x=1', stored: T })
    expect(h.seen.atAnalytics).toEqual({ url: '/invite?x=1', stored: T })
    expect(history.replaceState).toHaveBeenCalledTimes(1)
  })

  it('main_rendersTheInvitePageOnlyOnInvite', async () => {
    for (const path of ['/invite', '/invite/', '/INVITE']) {
      const { InvitePage, CrashBoundary } = await bootMain(`${path}#token=${T}`)
      const root = h.render.mock.calls[0][0] as Root
      expect(root.type, path).toBe(StrictMode)
      expect(root.props.children.type, path).toBe(CrashBoundary)
      const child = root.props.children.props.children
      expect(child.type, path).toBe(InvitePage)
      expect(child.props.token, path).toBe(T)
      expect(h.render.mock.invocationCallOrder[0], path).toBeLessThan(h.bootAnalytics.mock.invocationCallOrder[0])
    }

    for (const path of ['/', '/privacy', '/invites', '/invite/x']) {
      const { App, InvitePage } = await bootMain(path)
      const child = (h.render.mock.calls[0][0] as Root).props.children.props.children
      expect(child.type, path).toBe(App)
      expect(child.type, path).not.toBe(InvitePage)
    }
  })

  it('main_givesInvitePageNoTokenWhenTheLinkHasNone', async () => {
    const { InvitePage } = await bootMain('/invite')
    const child = (h.render.mock.calls[0][0] as Root).props.children.props.children
    expect(child.type, 'control: the invite page renders').toBe(InvitePage)
    expect(child.props.token).toBeNull()
  })
})
