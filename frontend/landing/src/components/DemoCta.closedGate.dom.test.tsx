// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://landing-pr-42.up.railway.app/" }
// The card on a NON-production host. The hostname arm of the gate is the one D1-D9
// never exercise — every one of them runs at www.ascomply.com — yet every PR
// preview and every local visit takes this path.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoCta } from './DemoCta'
import { isProductionHost } from '../hubspot'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function $<T extends HTMLElement>(sel: string): T {
  const el = container.querySelector<T>(sel)
  expect(el, `expected ${sel}`).not.toBeNull()
  return el!
}

function typeInto(input: HTMLInputElement, value: string): void {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setValue.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('X9: a card lead on a preview host completes locally and sends nothing', () => {
  it('the HubSpot credentials are present, the hostname is not, so no fetch leaves the page', async () => {
    // Control needle: the gate's hostname arm really is closed here, so the
    // "no fetch" assertion below cannot pass for the wrong reason.
    expect(isProductionHost(window.location.hostname)).toBe(false)
    vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
    vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)

    await act(async () => {
      root.render(createElement(DemoCta))
    })
    await act(async () => {
      typeInto($('#dc-name'), 'Ada Okafor')
      typeInto($('#dc-email'), 'ada@okafor.ng')
      typeInto($('#dc-company'), 'Okafor & Partners')
    })
    await act(async () => $<HTMLInputElement>('#dc-consent').click())

    vi.useFakeTimers()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1400)
    })
    vi.useRealTimers()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).toContain("You're booked")
    expect(document.activeElement?.id).toBe('dc-success')
    expect(consoleError).not.toHaveBeenCalled()
  })
})
