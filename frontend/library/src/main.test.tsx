import { StrictMode, type ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { CrashBoundary } from '@invoice-os/monitoring'

const h = vi.hoisted(() => {
  const render = vi.fn()
  return { render, initMonitoring: vi.fn(), createRoot: vi.fn(() => ({ render })), initsBeforeMark: -1 }
})

vi.mock('@invoice-os/monitoring', () => ({
  initMonitoring: h.initMonitoring,
  CrashBoundary: () => null,
}))
vi.mock('react-dom/client', () => ({ createRoot: h.createRoot }))
// Evaluated when main.tsx's own import runs: counts inits that ran before it.
vi.mock('@invoice-os/design-tokens/v2/assets/mark.png', () => {
  h.initsBeforeMark = h.initMonitoring.mock.calls.length
  return { default: 'mark-stub.png' }
})

describe('main', () => {
  it('main_startsMonitoringBeforeRenderAndWrapsTheRoot', async () => {
    vi.stubGlobal('document', { getElementById: () => ({}) })
    await import('./main')

    expect(h.initMonitoring).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.calls[0]).toEqual(['library'])

    expect(h.createRoot).toHaveBeenCalledTimes(1)
    expect(h.initMonitoring.mock.invocationCallOrder[0]).toBeLessThan(h.createRoot.mock.invocationCallOrder[0])

    expect(h.render).toHaveBeenCalledTimes(1)
    const root = h.render.mock.calls[0][0] as ReactElement<{ children: ReactElement<{ brand: ReactElement<{ src?: unknown }> }> }>
    expect(root.type).toBe(StrictMode)
    const boundary = root.props.children
    expect(boundary.type).toBe(CrashBoundary)
    expect(boundary.props.brand.type).toBe('img')
    expect(boundary.props.brand.props.src).toBe('mark-stub.png')
    // Imports evaluate in order: init must already have run when main.tsx's later imports load.
    expect(h.initsBeforeMark).toBe(1)
  })
})
