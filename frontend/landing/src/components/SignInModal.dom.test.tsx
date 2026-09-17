// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Mounts SignInModal directly. App.signIn.dom.test.tsx owns opening and closing it from the nav.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { LANDING_PERSONAS } from '../auth'
import { SignInModal } from './SignInModal'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const HOME = 'https://www.ascomply.com/'
const CODE_STEP_TEXT = ["Verify it's you", 'Verify & continue', 'Back to accounts', 'Resend code', 'Signing in', '481920', "That code doesn't match"]
const FOOTER_TEXT = ['Forgot password?', 'Password reset is disabled', 'SSO · OAUTH2', 'OAUTH2']

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>
let locationStub: { href: string }
let originalLocationDescriptor: PropertyDescriptor | undefined

beforeEach(() => {
  vi.useFakeTimers()
  originalLocationDescriptor = Object.getOwnPropertyDescriptor(window, 'location')
  locationStub = { href: HOME }
  Object.defineProperty(window, 'location', { value: locationStub, writable: true, configurable: true })

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (originalLocationDescriptor) Object.defineProperty(window, 'location', originalLocationDescriptor)
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.useRealTimers()
})

async function mount(onClose: () => void = vi.fn()): Promise<void> {
  await act(async () => {
    root.render(createElement(SignInModal, { onClose }))
  })
}

function dialog(): HTMLElement {
  const d = document.querySelector<HTMLElement>('[role="dialog"]')
  expect(d, 'expected the Sign in dialog').not.toBeNull()
  return d!
}

// Asserts presence first so a missing persona fails as an assertion, not a TypeError.
async function clickPersona(id: string): Promise<void> {
  const b = document.querySelector<HTMLButtonElement>(`[data-persona="${id}"]`)
  expect(b, `expected persona ${id} on the picker`).not.toBeNull()
  await act(async () => {
    b!.click()
  })
}

// Unset targets are stubbed to '' (resolveBase → null) so a shell-exported VITE_* cannot leak in.
function stubTargets(env: Partial<Record<'VITE_APP_URL' | 'VITE_OPS_URL' | 'VITE_SUPPORT_URL', string>>): void {
  for (const k of ['VITE_APP_URL', 'VITE_OPS_URL', 'VITE_SUPPORT_URL'] as const) vi.stubEnv(k, env[k] ?? '')
}

const ALL_TARGETS = { VITE_APP_URL: 'https://app.example.test', VITE_OPS_URL: 'https://ops.example.test', VITE_SUPPORT_URL: 'https://support.example.test' }
const CASES = [
  ['developer', 'https://ops.example.test?persona=developer'],
  ['support', 'https://support.example.test?persona=support'],
  ['firm', 'https://app.example.test?persona=firm'],
  ['inhouse', 'https://app.example.test?persona=inhouse'],
] as const

describe('controls', () => {
  it('T01-1: control — the location stub captures an assignment', () => {
    window.location.href = 'https://example.test/probe'
    expect(locationStub.href).toBe('https://example.test/probe')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('T01-2: control — the picker renders four personas in order', async () => {
    await mount()
    const d = dialog()
    expect(d.getAttribute('aria-label')).toBe('Sign in')
    expect(d.textContent).toContain('Choose an account')
    expect(d.textContent).toContain('Pick a demo profile to continue')
    const ids = Array.from(d.querySelectorAll<HTMLElement>('[data-persona]'), (b) => b.dataset.persona)
    expect(ids).toEqual(['developer', 'support', 'firm', 'inhouse'])
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('the pick', () => {
  it('T01-3: each persona control is a focusable button', async () => {
    await mount()
    const buttons = dialog().querySelectorAll<HTMLButtonElement>('[data-persona]')
    expect(buttons.length).toBe(4)
    for (const b of Array.from(buttons)) {
      expect(b.tagName).toBe('BUTTON')
      expect(b.disabled).toBe(false)
      expect(b.tabIndex).toBeGreaterThanOrEqual(0)
      b.focus()
      expect(document.activeElement).toBe(b)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each(CASES)('T01-4: %s navigates at once to its own target', async (id, expected) => {
    expect(CASES).toHaveLength(4)
    stubTargets(ALL_TARGETS)
    await mount()
    await clickPersona(id)
    expect(locationStub.href).toBe(expected)
    expect(vi.getTimerCount()).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('T01-5: no intermediate step follows a pick', async () => {
    stubTargets(ALL_TARGETS)
    await mount()
    await clickPersona('firm')
    const d = dialog()
    expect(d.querySelectorAll('input').length).toBe(0)
    for (const s of CODE_STEP_TEXT) {
      expect(d.textContent, `dialog still shows "${s}"`).not.toContain(s)
    }
    expect(d.querySelectorAll('[data-persona]').length).toBe(4)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('T01-6: an unconfigured target is a no-op', async () => {
    stubTargets({})
    await mount()
    await clickPersona('firm')
    expect(locationStub.href).toBe(HOME)
    expect(dialog().querySelectorAll('[data-persona]').length).toBe(4)
    expect(dialog().querySelectorAll('input').length).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('T01-7: the null target is per persona, not all-or-nothing', async () => {
    stubTargets({ VITE_OPS_URL: 'https://ops.example.test' })
    await mount()
    await clickPersona('firm')
    expect(locationStub.href).toBe(HOME)
    await clickPersona('developer')
    expect(locationStub.href).toBe('https://ops.example.test?persona=developer')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('removed chrome', () => {
  it('T01-8: the picker has no password or SSO footer', async () => {
    await mount()
    const d = dialog()
    expect(d.textContent).toContain('Choose an account')
    expect(d.querySelectorAll('a').length).toBe(0)
    for (const s of FOOTER_TEXT) {
      expect(d.textContent, `dialog still shows "${s}"`).not.toContain(s)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('persona data', () => {
  it('QA-1: personas carry no email or destLabel', () => {
    expect(LANDING_PERSONAS.map((p) => p.id)).toEqual(['developer', 'support', 'firm', 'inhouse'])
    for (const p of LANDING_PERSONAS) {
      expect(Object.keys(p)).toContain('target')
      expect(Object.keys(p)).not.toContain('email')
      expect(Object.keys(p)).not.toContain('destLabel')
    }
  })
})

describe('Escape', () => {
  it('T01-8b: Escape closes; the listener is removed on unmount', async () => {
    const onClose = vi.fn()
    await mount(onClose)

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    })
    expect(onClose).not.toHaveBeenCalled()

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(onClose).toHaveBeenCalledTimes(1)

    act(() => {
      root.unmount()
    })
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })
})
