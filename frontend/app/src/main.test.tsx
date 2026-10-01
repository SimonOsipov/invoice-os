import { StrictMode, isValidElement, type ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { CrashBoundary } from '@invoice-os/monitoring'

import App from './App'
import { BrandMark } from './icons'
import { routeName } from './lib/route'

const h = vi.hoisted(() => {
  const render = vi.fn()
  return { render, initMonitoring: vi.fn(), createRoot: vi.fn(() => ({ render })) }
})

vi.mock('@invoice-os/monitoring', () => ({
  initMonitoring: h.initMonitoring,
  CrashBoundary: () => null,
}))
vi.mock('react-dom/client', () => ({ createRoot: h.createRoot }))
vi.mock('./App', () => ({ default: () => null }))

describe('main', () => {
  it('main_startsMonitoringBeforeRenderAndWrapsTheApp', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    vi.stubGlobal('document', { getElementById: () => ({}) })
    await import('./main')

    expect(h.initMonitoring).toHaveBeenCalledTimes(1)
    const [service, opts] = h.initMonitoring.mock.calls[0] as unknown as [string, { gateway: string; routeName: unknown }]
    expect(service).toBe('app')
    expect(opts.gateway).toBe('https://gw.test')
    expect(opts.routeName).toBe(routeName)

    expect(h.createRoot).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.invocationCallOrder[0]).toBeLessThan(h.createRoot.mock.invocationCallOrder[0])

    expect(h.render).toHaveBeenCalledTimes(1)
    const root = h.render.mock.calls[0][0] as ReactElement<{ children: ReactElement<{ brand: unknown; children: ReactElement }> }>
    expect(root.type).toBe(StrictMode)
    const boundary = root.props.children
    expect(boundary.type).toBe(CrashBoundary)
    expect(isValidElement(boundary.props.brand) && boundary.props.brand.type).toBe(BrandMark)
    expect(boundary.props.children.type).toBe(App)
  })
})
