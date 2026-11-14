import { StrictMode, isValidElement, type ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { gatewayBase } from '@invoice-os/api-client'
import { CrashBoundary } from '@invoice-os/monitoring'

import { BrandMark } from './icons'
import { routeName } from './lib/route'

const h = vi.hoisted(() => {
  const render = vi.fn()
  return { render, appEvaluated: vi.fn(), initMonitoring: vi.fn(), createRoot: vi.fn(() => ({ render })) }
})

vi.mock('@invoice-os/monitoring', () => ({
  initMonitoring: h.initMonitoring,
  CrashBoundary: () => null,
}))
vi.mock('react-dom/client', () => ({ createRoot: h.createRoot }))
vi.mock('./App', () => {
  h.appEvaluated()
  return { default: () => null }
})

describe('main', () => {
  it('main_startsMonitoringBeforeRenderAndWrapsTheApp', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    vi.stubGlobal('document', { getElementById: () => ({}) })
    vi.stubGlobal('window', { location: { search: '' } })
    await import('./main')
    await vi.waitFor(() => expect(h.render).toHaveBeenCalledTimes(1))
    const { default: App } = await import('./App')

    expect(h.initMonitoring).toHaveBeenCalledTimes(1)
    const [service, opts] = h.initMonitoring.mock.calls[0] as unknown as [string, { gateway: string; routeName: unknown }]
    expect(service).toBe('app')
    expect(opts.gateway).toBe('https://gw.test')
    expect(opts.routeName).toBe(routeName)

    expect(h.createRoot).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.invocationCallOrder[0]).toBeLessThan(h.createRoot.mock.invocationCallOrder[0])

    // Imports are hoisted, so only a side-effect module imported first inits before App's graph evaluates.
    expect(h.appEvaluated).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.invocationCallOrder[0]).toBeLessThan(h.appEvaluated.mock.invocationCallOrder[0])

    expect(h.render).toHaveBeenCalledTimes(1)
    const root = h.render.mock.calls[0][0] as ReactElement<{ children: ReactElement<{ brand: unknown; children: ReactElement }> }>
    expect(root.type).toBe(StrictMode)
    const boundary = root.props.children
    expect(boundary.type).toBe(CrashBoundary)
    expect(isValidElement(boundary.props.brand) && boundary.props.brand.type).toBe(BrandMark)
    expect((boundary.props.brand as ReactElement<{ size?: number }>).props.size).toBe(20)
    expect(boundary.props.children.type).toBe(App)
  })
  it('main_passesTheGatewayOriginTheTransportsCall', async () => {
    const gatewayArg = async (env: string) => {
      vi.resetModules()
      h.initMonitoring.mockClear()
      vi.stubEnv('VITE_GATEWAY_URL', env)
      await import('./main')
      return (h.initMonitoring.mock.calls[0] as unknown as [string, { gateway: unknown }])[1].gateway
    }
    vi.stubGlobal('document', { getElementById: () => ({}) })
    vi.stubGlobal('window', { location: { search: '' } })

    vi.stubEnv('VITE_GATEWAY_URL', ' https://gw.test/// ')
    expect(gatewayBase()).toBe('https://gw.test')
    expect(await gatewayArg(' https://gw.test/// ')).toBe('https://gw.test')
    expect(await gatewayArg('')).toBeNull()
  })
  it('main_rendersOnlyAfterAPendingSignOutIsSent', async () => {
    const store = new Map([['invoice-os.sign-out-pending', '1']])
    vi.stubGlobal('localStorage', { getItem: (k: string) => store.get(k) ?? null, removeItem: (k: string) => void store.delete(k), setItem: vi.fn() })
    let answer: (r: Response) => void = () => {}
    const fetchMock = vi.fn((_url: string, _init: unknown) => new Promise<Response>((resolve) => (answer = resolve)))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    vi.stubGlobal('document', { getElementById: () => ({}) })
    vi.stubGlobal('window', { location: { search: '' } })
    vi.resetModules()
    h.render.mockClear()

    await import('./main')

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0]![0]).toBe('https://gw.test/auth/sign-out')
    // The sign-out is out and unanswered: the app has not rendered.
    expect(h.render).not.toHaveBeenCalled()
    answer(new Response(null, { status: 204 }))
    await vi.waitFor(() => expect(h.render).toHaveBeenCalledTimes(1))
    expect(store.has('invoice-os.sign-out-pending')).toBe(false)

    // A hand-off load sends nothing and keeps the marker; the app still renders.
    store.set('invoice-os.sign-out-pending', '1')
    fetchMock.mockClear()
    h.render.mockClear()
    vi.stubGlobal('window', { location: { search: '?handoff=abc' } })
    vi.resetModules()
    await import('./main')
    await vi.waitFor(() => expect(h.render).toHaveBeenCalledTimes(1))
    expect(fetchMock).not.toHaveBeenCalled()
    expect(store.has('invoice-os.sign-out-pending')).toBe(true)
    vi.unstubAllGlobals()
  })
})
