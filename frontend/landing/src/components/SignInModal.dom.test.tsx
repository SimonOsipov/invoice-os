// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Mounts SignInModal directly. App.signIn.dom.test.tsx owns opening and closing it from the nav.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SIGN_IN_UNAVAILABLE as UNAVAILABLE } from '../signIn'
import { SignInModal } from './SignInModal'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  vi.useFakeTimers()

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.useRealTimers()
})

async function mount(onClose: () => void = vi.fn(), state: string | null = null, initialError?: string, onCreateAccount?: () => void, initialView?: 'sign-in' | 'forgot'): Promise<void> {
  await act(async () => {
    root.render(createElement(SignInModal, { onClose, heldState: () => state, initialError, onCreateAccount, initialView }))
  })
}

// Gateway unset: signInConfigured() is false whatever the SPA targets are.
function unconfigured(): void {
  vi.stubEnv('VITE_GATEWAY_URL', '')
}

function configured(): void {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.example.test')
}

const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'

function dialog(): HTMLElement {
  const d = document.querySelector<HTMLElement>('[role="dialog"]')
  expect(d, 'expected the Platform login dialog').not.toBeNull()
  return d!
}

// Unset targets are stubbed to '' (resolveBase → null) so a shell-exported VITE_* cannot leak in.
function stubTargets(env: Partial<Record<'VITE_APP_URL' | 'VITE_OPS_URL' | 'VITE_SUPPORT_URL', string>>): void {
  for (const k of ['VITE_APP_URL', 'VITE_OPS_URL', 'VITE_SUPPORT_URL'] as const) vi.stubEnv(k, env[k] ?? '')
}

const ALL_TARGETS = { VITE_APP_URL: 'https://app.example.test', VITE_OPS_URL: 'https://ops.example.test', VITE_SUPPORT_URL: 'https://support.example.test' }

describe('the real door only', () => {
  const h3s = (d: HTMLElement) => Array.from(d.querySelectorAll('h3'), (h) => h.textContent)

  it('SM-01: the configured dialog is the real door only', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    await mount(vi.fn(), STATE, undefined, vi.fn())
    const d = dialog()
    expect(d.querySelectorAll('form').length, 'the email form').toBe(1)
    expect(Array.from(d.querySelectorAll('button')).some((b) => b.textContent === 'Create an account'), 'the create link').toBe(true)
    expect(h3s(d)).toEqual(['Sign in to your workspace'])
    expect(d.querySelectorAll('[data-persona]').length).toBe(0)
    expect(d.querySelectorAll('[data-testid="persona-picker"]').length).toBe(0)
    expect(d.textContent).not.toContain('Choose an account')
    expect(d.textContent).not.toContain('demo profile')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('SM-13: registration closed hides only the create link', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    await mount(vi.fn(), STATE)
    const d = dialog()
    expect(d.querySelectorAll('form').length, 'control: the form stays').toBe(1)
    expect(d.querySelectorAll('input[type="password"]').length).toBe(1)
    expect(h3s(d), 'the heading stays and nothing else is added').toEqual(['Sign in to your workspace'])
    expect(d.textContent).not.toContain('Create an account')
    expect(d.textContent).not.toContain('New to ASComply?')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each([
    ['gateway unset', () => { stubTargets(ALL_TARGETS); unconfigured() }],
    ['app unset', () => { stubTargets({}); configured() }],
  ] as const)('SM-12 %s: unconfigured shows the unavailable copy', async (label, setup) => {
    setup()
    await mount(vi.fn(), STATE, undefined, vi.fn())
    const d = dialog()
    expect(h3s(d), label).toEqual(['Sign in to your workspace'])
    expect(d.textContent, label).toContain(UNAVAILABLE)
    expect(d.querySelectorAll('form').length, label).toBe(0)
    expect(d.querySelectorAll('input').length, label).toBe(0)
    expect(d.textContent, label).not.toContain('Create an account')
    expect(d.querySelectorAll('[data-persona]').length, label).toBe(0)
    expect(d.textContent, label).not.toContain('Choose an account')
    expect(d.textContent, label).not.toContain('demo profile')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('v2 content', () => {
  const norm = (s: string) => s.replace(/\s+/g, ' ').trim()

  // Declarations of every rule whose selector list names `selector`, across the dialog's <style> tags.
  function declsOf(selector: string): string[] {
    const css = Array.from(dialog().querySelectorAll('style'), (s) => s.textContent ?? '').join('\n')
    const out: string[] = []
    for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      if (m[1].split(',').some((sel) => norm(sel) === selector)) out.push(...m[2].split(';').map(norm).filter(Boolean))
    }
    return out
  }

  it.each([
    ['unconfigured', unconfigured, 0],
    ['configured', () => { stubTargets(ALL_TARGETS); configured() }, 1],
  ])('SM-02 %s: the eyebrow is PLATFORM LOGIN', async (_label, setup, forms) => {
    setup()
    await mount(vi.fn(), STATE)
    const d = dialog()
    expect(d.querySelectorAll('input[type="password"]').length, 'control: the form shows only when configured').toBe(forms)
    const eyebrows = Array.from(d.querySelectorAll('.t-eyebrow'))
    expect(eyebrows.map((e) => e.textContent)).toEqual(['PLATFORM LOGIN'])
    expect(d.textContent).not.toContain('SIGN IN')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('SM-03: the heading keeps the v2 style', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    await mount(vi.fn(), STATE)
    const h3s = Array.from(dialog().querySelectorAll<HTMLElement>('h3'))
    expect(h3s.map((h) => h.textContent)).toEqual(['Sign in to your workspace'])
    for (const h of h3s) {
      const label = h.textContent ?? ''
      expect(h.style.fontSize, label).toBe('22px')
      expect(h.style.fontWeight, label).toBe('700')
      expect(h.style.letterSpacing, label).toBe('-0.03em')
      expect(h.style.color, label).toBe('var(--ink)')
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('SM-06: the body follows the v2 spacing, unconfigured and configured', async () => {
    for (const [label, setup] of [['unconfigured', unconfigured], ['configured', () => { stubTargets(ALL_TARGETS); configured() }]] as const) {
      setup()
      await mount(vi.fn(), STATE)
      const d = dialog()
      const body = d.querySelector<HTMLElement>('.t-eyebrow')!.parentElement!.parentElement as HTMLElement
      expect(body.style.padding, label).toBe('22px 20px 20px')
      expect((body.firstElementChild as HTMLElement).style.marginBottom, `${label} eyebrow wrapper`).toBe('14px')
      const headings = Array.from(d.querySelectorAll<HTMLElement>('h3'))
      expect(headings.map((h) => h.textContent), label).toEqual(['Sign in to your workspace'])
      expect(headings[0].parentElement, `${label} heading sits in the body`).toBe(body)
      expect(d.querySelectorAll('[data-persona]').length, `${label} no persona rows`).toBe(0)
      expect(d.querySelectorAll('[data-testid="persona-picker"]').length, `${label} no picker`).toBe(0)
      act(() => root.render(null))
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('SM-07 configured: the workspace heading, form and create link follow in v2 order', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    await mount(vi.fn(), STATE, undefined, vi.fn())
    const d = dialog()
    const create = Array.from(d.querySelectorAll('button')).find((b) => b.textContent === 'Create an account')
    const order = [
      d.querySelector('.t-eyebrow'),
      Array.from(d.querySelectorAll('h3')).find((h) => h.textContent === 'Sign in to your workspace'),
      d.querySelector('form'),
      create,
    ]
    order.forEach((el, i) => expect(el, `step ${i} missing`).toBeTruthy())
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1]!.compareDocumentPosition(order[i]!) & Node.DOCUMENT_POSITION_FOLLOWING, `step ${i - 1} must precede step ${i}`).toBeTruthy()
    }
    expect((order[1] as HTMLElement).style.margin).toBe('0px 0px 16px')
    const row = create!.parentElement as HTMLElement
    expect(row.textContent).toContain('New to ASComply?')
    expect(row.nextElementSibling, 'the create row ends the body').toBeNull()
    expect(d.querySelectorAll('[data-persona]').length).toBe(0)
    expect(d.textContent).not.toContain('or explore with a demo profile')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('SM-09: held state and initialError reach the form under the new body', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    // No held state: the form offers the start bounce.
    await mount(vi.fn(), null, 'Sign-in failed. Try again.')
    let d = dialog()
    const bounce = Array.from(d.querySelectorAll('button')).find((b) => b.textContent === 'Continue with email')
    expect(bounce, 'expected the start-bounce button').toBeDefined()
    expect(d.querySelectorAll('input[type="password"]').length).toBe(0)
    expect(Array.from(d.querySelectorAll('[role="alert"]'), (a) => a.textContent?.trim())).toEqual(['Sign-in failed. Try again.'])
    expect(d.querySelectorAll('.t-eyebrow').length).toBe(1)

    // A held state: the credentials form replaces the bounce, and the error still shows.
    await act(async () => root.render(null))
    await mount(vi.fn(), STATE, 'Sign-in failed. Try again.')
    d = dialog()
    expect(d.querySelectorAll('input[type="password"]').length).toBe(1)
    expect(Array.from(d.querySelectorAll('button')).some((b) => b.textContent === 'Continue with email')).toBe(false)
    expect(Array.from(d.querySelectorAll('[role="alert"]'), (a) => a.textContent?.trim())).toEqual(['Sign-in failed. Try again.'])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('SM-11: nothing defeats the focus ring, and the dialog is modal and named Platform login', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    await mount(vi.fn(), STATE)
    const d = dialog()
    expect(d.getAttribute('role')).toBe('dialog')
    expect(d.getAttribute('aria-modal')).toBe('true')
    expect(d.getAttribute('aria-label')).toBe('Platform login')
    expect(d.querySelectorAll('[aria-label="Sign in"]').length).toBe(0)
    expect(declsOf('.si-close:focus-visible'), 'control: the parser finds the ring rule').toContain('outline: 2px solid var(--ring)')
    const css = Array.from(d.querySelectorAll('style'), (s) => s.textContent ?? '').join('\n')
    expect(css, 'the picker rules are gone with the picker').not.toContain('.si-persona')
    expect(css).not.toMatch(/outline(-style)?\s*:\s*(none|0)/)
    const controls = Array.from(d.querySelectorAll<HTMLElement>('input, button'))
    expect(controls.length).toBeGreaterThan(0)
    for (const c of controls) expect(c.style.getPropertyValue('outline'), c.outerHTML.slice(0, 60)).not.toMatch(/^(none|0)/)
    expect(consoleError).not.toHaveBeenCalled()
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

  it('SM-14: a click inside the card keeps the dialog open and a scrim click closes it', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    const onClose = vi.fn()
    await mount(onClose, STATE)
    const d = dialog()
    const inside = [d.querySelector('.t-eyebrow'), d.querySelector('h3'), d.querySelector('input')]
    inside.forEach((el, i) => expect(el, `inside target ${i} missing`).not.toBeNull())
    for (const el of inside) {
      await act(async () => {
        el!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
    }
    expect(onClose, 'a click in the card does not close').not.toHaveBeenCalled()
    await act(async () => {
      d.click()
    })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('the forgot view', () => {
  const BOOT_ERROR = 'Sign-in failed. Try again.'
  const SIGN_IN_HEADING = 'Sign in to your workspace'
  const RESET_HEADING = 'Reset your password'

  const headings = () => Array.from(dialog().querySelectorAll('h3'), (h) => h.textContent)
  const buttonTexts = () => Array.from(dialog().querySelectorAll('button'), (b) => b.textContent?.trim())

  async function press(label: string): Promise<void> {
    const btn = Array.from(dialog().querySelectorAll('button')).find((b) => b.textContent?.trim() === label)
    expect(btn, `expected a "${label}" button`).toBeDefined()
    await act(async () => {
      btn!.click()
    })
  }

  it('Forgot password? opens the forgot view and Back returns', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    await mount(vi.fn(), STATE, undefined, vi.fn())
    expect(headings()).toEqual([SIGN_IN_HEADING])
    expect(dialog().querySelectorAll('input[type="password"]').length, 'control: the sign-in form shows').toBe(1)

    await press('Forgot password?')
    const d = dialog()
    expect(headings()).toEqual([RESET_HEADING])
    expect(Array.from(d.querySelectorAll('label'), (l) => l.textContent)).toEqual(['Work email'])
    expect(d.querySelectorAll('input[type="email"]').length).toBe(1)
    expect(d.querySelectorAll('input[type="password"]').length, 'the sign-in form is gone').toBe(0)
    expect(buttonTexts()).toEqual(expect.arrayContaining(['Send reset link', 'Back to sign in']))
    expect(buttonTexts(), 'the sign-in submit is gone').not.toContain('Sign in →')
    expect(Array.from(d.querySelectorAll('.t-eyebrow'), (e) => e.textContent), 'the eyebrow stays').toEqual(['PLATFORM LOGIN'])
    expect(buttonTexts(), 'the create link stays').toContain('Create an account')

    await press('Back to sign in')
    expect(headings()).toEqual([SIGN_IN_HEADING])
    expect(dialog().querySelectorAll('input[type="password"]').length).toBe(1)
    expect(buttonTexts()).not.toContain('Send reset link')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('initialView forgot opens on the forgot view, with or without a held state', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    for (const state of [STATE, null]) {
      await mount(vi.fn(), state, undefined, undefined, 'forgot')
      expect(headings(), `state ${state}`).toEqual([RESET_HEADING])
      expect(buttonTexts(), `state ${state}`).toEqual(expect.arrayContaining(['Send reset link', 'Back to sign in']))
      await act(async () => root.render(null))
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('the boot error does not return after Back', async () => {
    stubTargets(ALL_TARGETS)
    configured()
    await mount(vi.fn(), STATE, BOOT_ERROR)
    expect(Array.from(dialog().querySelectorAll('[role="alert"]'), (a) => a.textContent?.trim()), 'control: the boot error shows').toEqual([BOOT_ERROR])

    await press('Forgot password?')
    expect(headings()).toEqual([RESET_HEADING])
    expect(dialog().textContent, 'hidden in the forgot view').not.toContain(BOOT_ERROR)

    await press('Back to sign in')
    expect(headings()).toEqual([SIGN_IN_HEADING])
    expect(dialog().querySelectorAll('input[type="password"]').length, 'control: the sign-in form is back').toBe(1)
    expect(dialog().textContent, 'not shown again').not.toContain(BOOT_ERROR)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('the forgot view is unavailable without a gateway', async () => {
    stubTargets(ALL_TARGETS)
    unconfigured()
    await mount(vi.fn(), STATE, undefined, undefined, 'forgot')
    const d = dialog()
    expect(d.textContent).toContain(UNAVAILABLE)
    expect(d.querySelectorAll('form').length).toBe(0)
    expect(d.querySelectorAll('input').length).toBe(0)
    expect(buttonTexts()).not.toContain('Send reset link')
    expect(consoleError).not.toHaveBeenCalled()
  })
})
