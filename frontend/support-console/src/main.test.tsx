import { StrictMode, isValidElement, type ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { CrashBoundary } from '@invoice-os/monitoring'

import { BrandMark } from './icons'

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
    vi.stubGlobal('document', { getElementById: () => ({}) })
    await import('./main')
    const { default: App } = await import('./App')

    expect(h.initMonitoring).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.calls[0]).toEqual(['support-console'])

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
    expect(boundary.props.children.type).toBe(App)
  })
})
