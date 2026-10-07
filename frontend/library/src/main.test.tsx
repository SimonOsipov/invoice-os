import { StrictMode, type ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { CrashBoundary } from '@invoice-os/monitoring'

const h = vi.hoisted(() => {
  const render = vi.fn()
  return { render, initMonitoring: vi.fn(), createRoot: vi.fn(() => ({ render })) }
})

vi.mock('@invoice-os/monitoring', () => ({
  initMonitoring: h.initMonitoring,
  CrashBoundary: () => null,
}))
vi.mock('react-dom/client', () => ({ createRoot: h.createRoot }))

describe('main', () => {
  it('main_startsMonitoringBeforeRenderAndWrapsTheRoot', async () => {
    vi.stubGlobal('document', { getElementById: () => ({}) })
    await import('./main')

    expect(h.initMonitoring).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.calls[0]).toEqual(['library'])

    expect(h.createRoot).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.invocationCallOrder[0]).toBeLessThan(h.createRoot.mock.invocationCallOrder[0])

    expect(h.render).toHaveBeenCalledTimes(1)
    const root = h.render.mock.calls[0][0] as ReactElement<{ children: ReactElement<{ brand: ReactElement }> }>
    expect(root.type).toBe(StrictMode)
    const boundary = root.props.children
    expect(boundary.type).toBe(CrashBoundary)
    expect(boundary.props.brand.type).toBe('img')
  })
})
