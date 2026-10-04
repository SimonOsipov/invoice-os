import { StrictMode, isValidElement, type ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { CrashBoundary } from '@invoice-os/monitoring'

import { BrandMark } from './icons'

const h = vi.hoisted(() => {
  const render = vi.fn()
  return {
    render,
    instrumentEvaluated: vi.fn(),
    appEvaluated: vi.fn(),
    bootAnalytics: vi.fn(),
    createRoot: vi.fn(() => ({ render })),
  }
})

vi.mock('./instrument', () => {
  h.instrumentEvaluated()
  return {}
})
vi.mock('@invoice-os/monitoring', () => ({ CrashBoundary: () => null }))
vi.mock('react-dom/client', () => ({ createRoot: h.createRoot }))
vi.mock('./App', () => {
  h.appEvaluated()
  return { default: () => null }
})
vi.mock('./analytics', () => ({ bootAnalytics: h.bootAnalytics }))

describe('main', () => {
  it('main_startsMonitoringBeforeTheAppGraphWrapsTheAppAndKeepsAnalytics', async () => {
    vi.stubGlobal('document', { getElementById: () => ({}) })
    await import('./main')
    const { default: App } = await import('./App')

    expect(h.instrumentEvaluated).toHaveBeenCalledTimes(1)
    expect(h.createRoot).toHaveBeenCalledTimes(1)
    expect(h.instrumentEvaluated.mock.invocationCallOrder[0]).toBeLessThan(h.createRoot.mock.invocationCallOrder[0])

    // Imports are hoisted, so only a side-effect module imported first evaluates before App's graph.
    expect(h.appEvaluated).toHaveBeenCalledTimes(1)
    expect(h.instrumentEvaluated.mock.invocationCallOrder[0]).toBeLessThan(h.appEvaluated.mock.invocationCallOrder[0])

    expect(h.render).toHaveBeenCalledTimes(1)
    const root = h.render.mock.calls[0][0] as ReactElement<{ children: ReactElement<{ brand: unknown; children: ReactElement }> }>
    expect(root.type).toBe(StrictMode)
    const boundary = root.props.children
    expect(boundary.type).toBe(CrashBoundary)
    expect(isValidElement(boundary.props.brand) && boundary.props.brand.type).toBe(BrandMark)
    expect(boundary.props.children.type).toBe(App)

    expect(h.bootAnalytics).toHaveBeenCalledTimes(1)
    expect(h.render.mock.invocationCallOrder[0]).toBeLessThan(h.bootAnalytics.mock.invocationCallOrder[0])
  })
})
