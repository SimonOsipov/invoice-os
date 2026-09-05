// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-10: exactly one audience segment shown at a time, defaulting to firms, and
// switching both the illustrative panel (left) and the copy (right) together.
//
// All three variants render simultaneously and permanently (Audience.tsx:29-37,
// 366-389) -- only `aria-hidden` / inline `visibility` distinguish the active one.
// So "the fintech copy is present after clicking Fintech" is true even before the
// click; every switching assertion below is paired with its anti-vacuity half
// (nothing else is visible / no stale copy remains). Same setup contract as
// App.signIn.dom.test.tsx: production URL, an installed memory localStorage, a
// console.error spy asserted empty.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from '../consent'
import { API_TENANTS, FINTECH, FIRM, INHOUSE } from '../data'
import { Audience } from './Audience'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// `#accountants .ios-grid > div > div[aria-hidden]` -- the 2 stacked-layer columns'
// direct children, 3 layers each. The naive `#accountants [aria-hidden]` returns 23:
// icons.tsx marks every SVG glyph `aria-hidden` too, and this selector must not catch them.
const LAYER_SELECTOR = '#accountants .ios-grid > div > div[aria-hidden]'

function memoryStorage(): ConsentStore {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      map.set(k, String(v))
    },
  }
}

let container: HTMLDivElement
let root: Root
let originalStorage: PropertyDescriptor | undefined
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage(), configurable: true, writable: true })

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
  else delete (globalThis as { localStorage?: unknown }).localStorage
  vi.restoreAllMocks()
})

async function mountAudience(): Promise<void> {
  await act(async () => {
    root.render(createElement(Audience, { onBookDemo: () => undefined }))
  })
}

// The 3 tab buttons carry no role/aria-selected/id/class of their own; the only
// `#accountants` buttons that DO carry a class are the 3 audience CTAs (`v2-btn`).
// Select structurally and click by index -- per Decisions, no text matching here.
function getTabs(): HTMLButtonElement[] {
  return Array.from(document.querySelectorAll<HTMLButtonElement>('#accountants button')).filter(
    (b) => !b.className.includes('v2-btn'),
  )
}

function visibleLayers(): Element[] {
  const layers = document.querySelectorAll(LAYER_SELECTOR)
  expect(layers.length, 'layer selector must resolve to exactly 6 elements before it is iterated').toBe(6)
  return Array.from(layers).filter((l) => l.getAttribute('aria-hidden') === 'false')
}

describe('F-10: exactly one audience, defaulting to firms, switching panel and copy', () => {
  it('F10-a: control needle -- 6 layers, 3 tabs', async () => {
    await mountAudience()
    expect(document.querySelectorAll(LAYER_SELECTOR).length).toBe(6)
    expect(getTabs().length).toBe(3)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F10-b: default -- exactly 2 visible layers, one holding FIRM.headline', async () => {
    await mountAudience()
    const visible = visibleLayers()
    expect(visible.length).toBe(2)
    const withHeadline = visible.filter((l) => l.textContent?.includes(FIRM.headline))
    expect(withHeadline.length, 'expected exactly one visible layer to carry FIRM.headline').toBe(1)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F10-c: default -- exactly one tab is active, and it is the firms tab', async () => {
    await mountAudience()
    const tabs = getTabs()
    expect(tabs.length).toBe(3)
    const active = tabs.filter((t) => t.style.background === 'var(--bg-2)')
    expect(active.length).toBe(1)
    expect(active[0]).toBe(tabs[0])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F10-d: sweeping firm/inhouse/fintech keeps exactly 2 visible layers and 1 active tab', async () => {
    await mountAudience()
    expect(getTabs().length).toBe(3)
    for (let i = 0; i < 3; i++) {
      await act(async () => {
        getTabs()[i].click()
      })
      expect(visibleLayers().length, `audience index ${i}`).toBe(2)
      const active = getTabs().filter((t) => t.style.background === 'var(--bg-2)')
      expect(active.length, `audience index ${i}`).toBe(1)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F10-e/f: after Fintech, copy AND panel switched, and no visible layer still holds FIRM.headline', async () => {
    await mountAudience()
    const tabs = getTabs()
    expect(tabs.length).toBe(3)
    await act(async () => {
      tabs[2].click()
    })

    const visible = visibleLayers()
    expect(visible.length).toBe(2)

    // F10-e, copy half: the AudienceCopy layer switched to Fintech.
    const withFintechHeadline = visible.filter((l) => l.textContent?.includes(FINTECH.headline))
    expect(withFintechHeadline.length).toBe(1)
    // F10-e, panel half: the FintechMock layer switched too (a fixture string only it renders).
    const withTenantName = visible.filter((l) => l.textContent?.includes(API_TENANTS[0].name))
    expect(withTenantName.length).toBe(1)

    // F10-f, anti-vacuity: without this, the assertions above would have passed
    // before any click, because all three variants are always in the DOM.
    const withFirmHeadline = visible.filter((l) => l.textContent?.includes(FIRM.headline))
    expect(withFirmHeadline.length).toBe(0)

    expect(consoleError).not.toHaveBeenCalled()
  })

  // Sanity that INHOUSE is imported for a reason beyond satisfying the AC's
  // "every expectation reads an imported constant" -- covered by the sweep (F10-d)
  // reaching the inhouse tab; this asserts its headline specifically.
  it('F10-g: selecting in-house shows exactly one layer holding INHOUSE.headline', async () => {
    await mountAudience()
    await act(async () => {
      getTabs()[1].click()
    })
    const visible = visibleLayers()
    const withHeadline = visible.filter((l) => l.textContent?.includes(INHOUSE.headline))
    expect(withHeadline.length).toBe(1)
    expect(consoleError).not.toHaveBeenCalled()
  })
})
