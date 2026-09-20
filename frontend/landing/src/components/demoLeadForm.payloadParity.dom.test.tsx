// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Mode A (RED) — transcribes Test Specs P1, P2. Both are NEW-BEHAVIOUR: today
// #demo has no dc-* fields at all, so driving the card through the shared
// fetch seam fails on the first missing element. Harness copied from
// DemoModal.retry.dom.test.tsx / App.demoCtas.dom.test.tsx.
/// <reference types="node" />
import { act, createElement } from 'react'
import type { ReactElement } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoCta } from './DemoCta'
import { DemoModal } from './DemoModal'
import { CONSENT_TEXT, DEFAULT_TAXPAYER_SIZE } from './demoForm'
import { buildSubmission, type DemoLead } from '../hubspot'
import type { ConsentStore } from '../consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function noop() {}

// DemoCta's prop shape is mid-migration (loses onBookDemo); erase it so this
// file typechecks on both sides of that edit.
const Cta = DemoCta as unknown as () => ReactElement

let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function openGate(): void {
  vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
  vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
}

function typeInto(input: HTMLInputElement, value: string): void {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setValue.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function memoryStorage(): ConsentStore {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      map.set(k, String(v))
    },
  }
}

// Mounts `node` in its own container, drives the three inputs + consent under
// `prefix`, submits, and returns the parsed body of the last fetch call.
async function mountAndCapture(node: ReactElement, prefix: 'dc' | 'dm', fetchMock: ReturnType<typeof vi.fn>): Promise<unknown> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(node)
  })

  function $<T extends HTMLElement>(sel: string): T {
    const el = container.querySelector<T>(sel)
    expect(el, `expected ${sel}`).not.toBeNull()
    return el!
  }

  await act(async () => {
    typeInto($(`#${prefix}-name`), 'Ada Okafor')
    typeInto($(`#${prefix}-email`), 'ada@okafor.ng')
    typeInto($(`#${prefix}-company`), 'Okafor & Partners')
  })
  await act(async () => {
    $<HTMLInputElement>(`#${prefix}-consent`).click()
  })
  await act(async () => {
    $<HTMLButtonElement>('button[type="submit"]').click()
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })

  const call = fetchMock.mock.calls[fetchMock.mock.calls.length - 1]
  const body = JSON.parse(((call?.[1] as RequestInit | undefined)?.body as string | undefined) ?? 'null')

  await act(async () => root.unmount())
  container.remove()
  return body
}

describe('P1 (AC-T3, NEW-BEHAVIOUR): card and popup build an identical payload', () => {
  it('the two captured bodies deep-equal each other and buildSubmission', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)

    const dcBody = await mountAndCapture(createElement(Cta), 'dc', fetchMock)
    const dmBody = await mountAndCapture(createElement(DemoModal, { onClose: noop }), 'dm', fetchMock)

    const lead: DemoLead = {
      name: 'Ada Okafor',
      email: 'ada@okafor.ng',
      company: 'Okafor & Partners',
      role: 'Finance or Accounting lead',
      size: DEFAULT_TAXPAYER_SIZE,
      volume: '1k–10k',
      consent: true,
    }
    const expected = buildSubmission(lead, CONSENT_TEXT)

    expect(dcBody).toEqual(expected)
    expect(dmBody).toEqual(expected)
    expect(dcBody).toEqual(dmBody)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('P2 ([id-prefix]): both surfaces mount together with no id collision', () => {
  it('opening the footer dialog while the card is mounted keeps dc-* and dm-* apart', async () => {
    const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
    Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage(), configurable: true, writable: true })
    vi.resetModules()

    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)

    const mod = (await import('../App')) as { default: () => ReactElement }
    await act(async () => {
      root.render(createElement(mod.default))
    })

    const footer = document.querySelector('footer')
    expect(footer, 'expected a footer').not.toBeNull()
    const footerCta = Array.from(footer?.querySelectorAll('button') ?? []).find((b) => b.textContent?.trim() === 'Book a demo')
    expect(footerCta, "expected the footer's Book a demo button").toBeDefined()
    if (footerCta) {
      await act(async () => {
        footerCta.click()
      })
    }

    expect(document.querySelectorAll('#dc-name').length, 'expected exactly one #dc-name').toBe(1)
    expect(document.querySelectorAll('#dm-name').length, 'expected exactly one #dm-name').toBe(1)

    const prefixed = Array.from(document.querySelectorAll('[id^="dc-"], [id^="dm-"]')).map((el) => el.id)
    expect(prefixed.length).toBeGreaterThan(0)
    expect(new Set(prefixed).size).toBe(prefixed.length)
    // The popup takes focus on open; the card does not steal it on mount ([card-no-mount-focus]).
    expect(document.activeElement?.id).toBe('dm-name')

    await act(async () => root.unmount())
    container.remove()
    if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
    else delete (globalThis as { localStorage?: unknown }).localStorage
  })
})
