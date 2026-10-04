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
    await import('./main')
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

    vi.stubEnv('VITE_GATEWAY_URL', ' https://gw.test/// ')
    expect(gatewayBase()).toBe('https://gw.test')
    expect(await gatewayArg(' https://gw.test/// ')).toBe('https://gw.test')
    expect(await gatewayArg('')).toBeNull()
  })
})
