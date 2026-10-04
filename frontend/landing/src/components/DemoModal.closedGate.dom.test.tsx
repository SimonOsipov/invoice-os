// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://landing-pr-42.up.railway.app/" }
// The closed-gate hostname arm is the one DemoModal.form.dom.test.tsx never exercises (it runs at
// www.ascomply.com).
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoModal } from './DemoModal'
import { isProductionHost } from '../hubspot'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function noop() {}

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  vi.stubEnv('VITE_GATEWAY_URL', '')
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

const GATEWAY_URL = 'https://gw.x/contacts/demo-request'
const hsFormsCalls = (fetchMock: ReturnType<typeof vi.fn>) =>
  fetchMock.mock.calls.filter((c) => String(c[0]).includes('hsforms.com'))
const accepted = () => new Response(JSON.stringify({ status: 'accepted' }), { status: 202 })

async function mountAndFill(opts: { marketing?: boolean } = {}): Promise<void> {
  await act(async () => {
    root.render(createElement(DemoModal, { onClose: noop }))
  })
  await act(async () => {
    typeInto($('#dm-name'), 'Ada Okafor')
    typeInto($('#dm-email'), 'ada@okafor.ng')
    typeInto($('#dm-company'), 'Okafor & Partners')
  })
  await act(async () => $<HTMLInputElement>('#dm-consent').click())
  if (opts.marketing) await act(async () => $<HTMLInputElement>('#dm-marketing').click())
}

function openPreviewGate(): void {
  // Control: the hostname arm really is closed, so "no HubSpot call" cannot pass for the wrong reason.
  expect(isProductionHost(window.location.hostname)).toBe(false)
  vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
  vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
}

describe('MF-X9: a modal lead on a preview host completes locally and sends nothing to HubSpot', () => {
  for (const withGateway of [false, true]) {
    it(`the HubSpot credentials are present, the hostname is not, so no HubSpot call leaves the page (${withGateway ? 'with' : 'without'} VITE_GATEWAY_URL)`, async () => {
      openPreviewGate()
      if (withGateway) vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
      const fetchMock = vi.fn().mockImplementation(async () => accepted())
      vi.stubGlobal('fetch', fetchMock)

      await mountAndFill()
      vi.useFakeTimers()
      await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1400)
      })
      vi.useRealTimers()

      expect(hsFormsCalls(fetchMock)).toEqual([])
      if (withGateway) {
        expect(fetchMock.mock.calls.map((c) => String(c[0])), 'the gateway call is the only request').toEqual([GATEWAY_URL])
      } else {
        expect(fetchMock).not.toHaveBeenCalled()
      }
      expect(container.textContent).toContain("You're booked")
      expect(document.activeElement?.id).toBe('dm-success-done')
      expect(consoleError).not.toHaveBeenCalled()
    })
  }
})

describe('AUTH-17-08: off production the gateway call and the stub run together', () => {
  it('off production the request goes to the gateway only and waits for the stub', async () => {
    openPreviewGate()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    const fetchMock = vi.fn().mockImplementation(async () => accepted())
    vi.stubGlobal('fetch', fetchMock)
    await mountAndFill({ marketing: true })

    vi.useFakeTimers()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(GATEWAY_URL)
    expect(hsFormsCalls(fetchMock)).toEqual([])
    expect(JSON.parse(String((fetchMock.mock.calls[0]?.[1] as RequestInit).body))).toMatchObject({ email: 'ada@okafor.ng', name: 'Ada Okafor', company: 'Okafor & Partners' })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1299)
    })
    expect(container.textContent, 'the fast gateway answer does not cut the stub short').toContain('Booking…')
    expect(container.textContent).not.toContain("You're booked")

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    vi.useRealTimers()
    expect(container.textContent).toContain("You're booked")
  })

  it('a gateway answering inside the stub window adds nothing to it', async () => {
    openPreviewGate()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockImplementation(() => new Promise<Response>((resolve) => setTimeout(() => resolve(accepted()), 1000)))
    vi.stubGlobal('fetch', fetchMock)
    await mountAndFill()

    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    expect(fetchMock, 'control: the gateway call is in flight').toHaveBeenCalledTimes(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1299)
    })
    expect(container.textContent, 'control: both the gateway and the stub are still pending').toContain('Booking…')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    vi.useRealTimers()
    expect(container.textContent, 'the stub started with the gateway call, not after it').toContain("You're booked")
  })

  it('a slow gateway delays success past the stub', async () => {
    openPreviewGate()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    let release: () => void = () => undefined
    const slow = new Promise<void>((resolve) => (release = resolve))
    const fetchMock = vi.fn().mockImplementation(() => slow.then(accepted))
    vi.stubGlobal('fetch', fetchMock)
    await mountAndFill()

    vi.useFakeTimers()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    expect(fetchMock, 'control: the gateway call is in flight').toHaveBeenCalledTimes(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(container.textContent, 'the stub has run out, the gateway has not answered').toContain('Booking…')
    expect(container.textContent).not.toContain("You're booked")

    await act(async () => release())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    vi.useRealTimers()
    expect(container.textContent).toContain("You're booked")
  })

  for (const [label, failing] of [
    ['a 400', async () => new Response(JSON.stringify({ error: 'email is invalid' }), { status: 400 })],
    ['a 502', async () => new Response(JSON.stringify({ error: 'demo request is unavailable' }), { status: 502 })],
    ['a network rejection', async () => Promise.reject(new TypeError('Failed to fetch'))],
  ] as const) {
    it(`off production a gateway failure shows the error panel (${label})`, async () => {
      openPreviewGate()
      vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
      const fetchMock = vi.fn().mockImplementation(failing)
      vi.stubGlobal('fetch', fetchMock)
      await mountAndFill()

      vi.useFakeTimers()
      await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1400)
      })
      vi.useRealTimers()

      expect(fetchMock, 'control: the gateway was called').toHaveBeenCalledTimes(1)
      expect(container.textContent).toContain('Something went wrong')
      expect(container.textContent).not.toContain("You're booked")
    })
  }

  it('a tripped honeypot sends nothing anywhere and is no faster than a real submit', async () => {
    openPreviewGate()
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    const fetchMock = vi.fn().mockImplementation(async () => accepted())
    vi.stubGlobal('fetch', fetchMock)
    await mountAndFill({ marketing: true })
    $<HTMLInputElement>('input[name="website"]').value = 'https://spam.example'

    vi.useFakeTimers()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1299)
    })
    expect(container.textContent).toContain('Booking…')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    vi.useRealTimers()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).toContain("You're booked")
  })
})
