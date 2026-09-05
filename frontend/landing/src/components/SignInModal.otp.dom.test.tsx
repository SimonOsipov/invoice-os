// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-016 unit half (task-893). Mounts SignInModal directly, starts at the OTP step --
// TEST-01's App.signIn.dom.test.tsx already owns dialog open/close (Core AC 8).
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SignInModal } from './SignInModal'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DEMO_CODE = '481920'

// Measured: a plain `el.value = x` write never reaches React state (state=[]);
// the native setter does (state=[7]).
function setNativeValue(el: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setter.call(el, value)
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function otpEl(i: number): HTMLInputElement {
  return document.getElementById('si-otp-' + i) as HTMLInputElement
}

async function typeDigit(i: number, digit: string): Promise<void> {
  await act(async () => setNativeValue(otpEl(i), digit))
}

async function typeCode(code: string): Promise<void> {
  for (let i = 0; i < code.length; i++) await typeDigit(i, code[i]!)
}

async function keydown(i: number, key: string): Promise<void> {
  await act(async () => {
    otpEl(i).dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
  })
}

async function clickByText(text: string): Promise<void> {
  const button = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
  expect(button, `expected a button labelled "${text}"`).toBeDefined()
  await act(async () => {
    button!.click()
  })
}

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>
let locationStub: { href: string }
let originalLocationDescriptor: PropertyDescriptor | undefined

beforeEach(() => {
  vi.useFakeTimers()
  originalLocationDescriptor = Object.getOwnPropertyDescriptor(window, 'location')
  locationStub = { href: 'https://www.ascomply.com/' }
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

// Reaches the OTP step directly: mount, then pick the firm persona via its stable
// data-persona selector.
async function mountAtOtp(): Promise<void> {
  await act(async () => {
    root.render(createElement(SignInModal, { onClose: vi.fn() }))
  })
  await act(async () => {
    document.querySelector<HTMLButtonElement>('[data-persona="firm"]')!.click()
  })
}

describe('vacuity controls', () => {
  // AC #5: without this, an assignment to window.location silently no-ops in an
  // untouched jsdom and the OTP-5/OTP-8/OTP-10 negatives would pass unfalsifiably.
  it('control: the location stub captures an assignment', () => {
    window.location.href = 'https://example.test/probe'
    expect(locationStub.href).toBe('https://example.test/probe')
    expect(consoleError).not.toHaveBeenCalled()
  })

  // AC #4: without this, a no-op typing helper would leave every other row green
  // for the wrong reason (nothing ever reaches DEMO_CODE, so both branches look inert).
  it('control: one keystroke reaches React state', async () => {
    await mountAtOtp()
    await typeDigit(0, '4')
    expect(otpEl(0).value).toBe('4')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('OTP box', () => {
  it('OTP-1: typing a digit advances focus to the next box', async () => {
    await mountAtOtp()
    await typeDigit(0, '4')
    // Fails if L61's `i < 5` guard is broken (e.g. `i < 0`): focus never moves.
    expect(document.activeElement).toBe(otpEl(1))
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-2: a non-digit is dropped and focus does not move', async () => {
    await mountAtOtp()
    await typeDigit(0, 'a')
    // Fails (false pass on absence) only if the digit filter is removed: then 'a'
    // would land in the box and (being truthy) also advance focus.
    expect(otpEl(0).value).toBe('')
    expect(document.activeElement).not.toBe(otpEl(1))
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-3: backspace on an empty box focuses the previous one', async () => {
    await mountAtOtp()
    await typeDigit(0, '1')
    await typeDigit(1, '2') // auto-advances to box 2, left empty
    await keydown(2, 'Backspace')
    expect(document.activeElement).toBe(otpEl(1))
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-4: backspace on a filled box does not move focus', async () => {
    await mountAtOtp()
    await typeDigit(0, '1')
    await typeDigit(1, '2')
    await typeDigit(2, '9') // box 2 holds 9, focus auto-advanced to box 3
    await keydown(2, 'Backspace')
    // Fails (false pass on absence) if the `!code[i]` half of the guard is dropped:
    // an unconditional walk-back would move focus back to box 2.
    expect(document.activeElement).toBe(otpEl(3))
    expect(consoleError).not.toHaveBeenCalled()
  })

  // The plan's own two assertions (button text, href) cannot see the mandated mutation
  // (an unconditional, synchronous verify() call after L61) -- it fires on every
  // keystroke and always takes the else branch, because the stale-closure `code` can
  // never equal DEMO_CODE mid-typing. These two extra assertions are what catch it:
  // the mutation shows a shake class and resets the boxes, which the happy path never does.
  it('OTP-5: a complete correct code does not auto-submit', async () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example.test')
    await mountAtOtp()
    await typeCode(DEMO_CODE)
    // Fails (false pass on absence) if verify() fires mid-typing: shake class appears.
    expect(document.querySelector('.si-shake')).toBeNull()
    // Fails (false pass on absence) on the same mutation: the boxes would be reset to ''.
    expect([0, 1, 2, 3, 4, 5].map((i) => otpEl(i).value).join('')).toBe(DEMO_CODE)
    expect(document.querySelector('button.v2-btn-primary')!.textContent).toContain('Verify & continue')
    expect(locationStub.href).toBe('https://www.ascomply.com/')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-6: the correct code enters the Signing in state', async () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example.test')
    await mountAtOtp()
    await typeCode(DEMO_CODE)
    await clickByText('Verify & continue')
    expect(document.querySelector('button.v2-btn-primary')!.textContent).toContain('Signing in')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-7: the correct code redirects to the firm workspace', async () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example.test')
    await mountAtOtp()
    await typeCode(DEMO_CODE)
    await clickByText('Verify & continue')
    await act(async () => {
      vi.advanceTimersByTime(1100)
    })
    // No slash before `?`: resolveBase strips trailing slashes off the input env value,
    // and the stubbed VITE_APP_URL carries none.
    expect(locationStub.href).toBe('https://app.example.test?persona=firm')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-8: a wrong code shows the error and clears the boxes', async () => {
    await mountAtOtp()
    await typeCode('000000')
    await clickByText('Verify & continue')
    expect(document.querySelector('.si-shake')).not.toBeNull()
    expect(document.body.textContent).toContain(`Use the demo code ${DEMO_CODE}`)
    expect([0, 1, 2, 3, 4, 5].every((i) => otpEl(i).value === '')).toBe(true)
    // Fails (false pass on absence) without the location vacuity control above: an
    // untouched stub would read the same whether or not a navigation was attempted.
    expect(locationStub.href).toBe('https://www.ascomply.com/')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-9: Enter on the last box verifies', async () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example.test')
    await mountAtOtp()
    await typeCode(DEMO_CODE)
    await keydown(5, 'Enter')
    expect(document.querySelector('button.v2-btn-primary')!.textContent).toContain('Signing in')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('OTP-10: no destination configured is a no-op', async () => {
    await mountAtOtp() // no vi.stubEnv -- VITE_APP_URL unset, destUrl -> null
    await typeCode(DEMO_CODE)
    await clickByText('Verify & continue')
    // Fails (false pass on absence) if the `if (!dest) return` guard is dropped:
    // setLoading(true) still fires and the button text flips to "Signing in...".
    expect(document.querySelector('button.v2-btn-primary')!.textContent).toContain('Verify & continue')
    expect(locationStub.href).toBe('https://www.ascomply.com/')
    expect(consoleError).not.toHaveBeenCalled()
  })
})
