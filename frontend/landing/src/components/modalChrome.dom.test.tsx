// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Mounted shell behaviour of the sign-in and demo modals:
// Close, overlay click, and the demo modal's Escape, focus restore and Tab trap.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoModal, isFocusable } from './DemoModal'
import { SignInModal } from './SignInModal'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root
let offsetParentBefore: PropertyDescriptor | undefined

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', '')
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  // jsdom has no layout: offsetParent is always null, which would make every element unfocusable to the trap.
  offsetParentBefore = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', {
    configurable: true,
    get(this: HTMLElement) {
      return this.isConnected ? (this.parentElement ?? document.body) : null
    },
  })
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (offsetParentBefore) Object.defineProperty(HTMLElement.prototype, 'offsetParent', offsetParentBefore)
  else delete (HTMLElement.prototype as { offsetParent?: unknown }).offsetParent
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
})

type Which = 'sign-in' | 'demo'
const MODALS: Which[] = ['sign-in', 'demo']

async function mount(which: Which, onClose: () => void): Promise<HTMLElement> {
  await act(async () => {
    root.render(which === 'demo' ? createElement(DemoModal, { onClose }) : createElement(SignInModal, { onClose }))
  })
  const d = document.querySelector<HTMLElement>('[role="dialog"]')
  expect(d, `expected the ${which} dialog`).not.toBeNull()
  return d!
}

function closeButton(d: HTMLElement): HTMLButtonElement {
  const buttons = d.querySelectorAll<HTMLButtonElement>('button[aria-label="Close"]')
  expect(buttons.length, 'expected exactly one Close button').toBe(1)
  return buttons[0]
}

function key(target: Element, init: KeyboardEventInit): KeyboardEvent {
  const e = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
  act(() => {
    target.dispatchEvent(e)
  })
  return e
}

describe('the modal shell: close, overlay click, Escape, focus restore, Tab trap', () => {
  for (const which of MODALS) {
    it(`MD-01 ${which}: Close calls onClose once; a click inside the card does not close; the overlay does`, async () => {
      const onClose = vi.fn()
      const d = await mount(which, onClose)
      act(() => {
        closeButton(d).click()
      })
      // The card stops propagation, so the overlay's own onClose must not fire a second time.
      expect(onClose).toHaveBeenCalledTimes(1)

      const card = d.querySelector<HTMLElement>(':scope > div')
      expect(card, 'expected the card').not.toBeNull()
      act(() => {
        card!.click()
      })
      expect(onClose).toHaveBeenCalledTimes(1)

      act(() => {
        d.click()
      })
      expect(onClose).toHaveBeenCalledTimes(2)
    })
  }

  it('MD-02 demo: Escape closes once, other keys do not, and the listener is gone after unmount', async () => {
    const onClose = vi.fn()
    await mount('demo', onClose)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    expect(onClose).not.toHaveBeenCalled()
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(onClose).toHaveBeenCalledTimes(1)
    act(() => root.unmount())
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(onClose).toHaveBeenCalledTimes(1)
    root = createRoot(container)
  })

  it('MD-03 demo: unmount refocuses the element that held focus when the modal mounted', async () => {
    const focused: Element[] = []
    const real = HTMLElement.prototype.focus
    vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement, ...args) {
      focused.push(this)
      return real.apply(this, args)
    })
    const d = await mount('demo', vi.fn())
    const name = d.querySelector<HTMLElement>('#dm-name')
    expect(name, 'expected #dm-name').not.toBeNull()
    // The form's mount effect focuses #dm-name first, so the shell captures it, not the opener.
    expect(focused.filter((el) => el === name).length).toBe(1)
    act(() => root.unmount())
    expect(focused.filter((el) => el === name).length, 'unmount did not restore focus').toBe(2)
    root = createRoot(container)
  })

  it('MD-04 demo: the Tab trap wraps at both ends and leaves the middle alone', async () => {
    const d = await mount('demo', vi.fn())
    const close = closeButton(d)
    const submit = d.querySelector<HTMLButtonElement>('button[type="submit"]')
    const name = d.querySelector<HTMLInputElement>('#dm-name')
    expect(submit, 'expected the submit').not.toBeNull()
    expect(name, 'expected #dm-name').not.toBeNull()

    act(() => submit!.focus())
    const wrapForward = key(submit!, { key: 'Tab' })
    expect(wrapForward.defaultPrevented, 'Tab on the last control must wrap').toBe(true)
    expect(document.activeElement).toBe(close)

    const wrapBack = key(close, { key: 'Tab', shiftKey: true })
    expect(wrapBack.defaultPrevented, 'Shift+Tab on the first control must wrap').toBe(true)
    expect(document.activeElement).toBe(submit)

    act(() => close.focus())
    expect(key(close, { key: 'Tab' }).defaultPrevented, 'Tab on the first control is the browser’s').toBe(false)
    act(() => name!.focus())
    expect(key(name!, { key: 'Tab', shiftKey: true }).defaultPrevented, 'Shift+Tab mid-form is the browser’s').toBe(false)
    act(() => submit!.focus())
    expect(key(submit!, { key: 'Enter' }).defaultPrevented, 'only Tab is trapped').toBe(false)
    expect(key(submit!, { key: 'Tab', shiftKey: true }).defaultPrevented, 'Shift+Tab on the last control is the browser’s').toBe(false)
  })

  it('MD-05 demo: the honeypot is outside the trap while the Close button and the submit are inside it', async () => {
    const d = await mount('demo', vi.fn())
    const honeypot = d.querySelector<HTMLElement>('input[name="website"]')
    const submit = d.querySelector<HTMLElement>('button[type="submit"]')
    expect(honeypot, 'expected the honeypot').not.toBeNull()
    expect(submit, 'expected the submit').not.toBeNull()
    expect(isFocusable(closeButton(d))).toBe(true)
    expect(isFocusable(submit!)).toBe(true)
    expect(isFocusable(honeypot!)).toBe(false)
  })
})
