// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-12: exactly one billing period, defaulting to Monthly, prices and notes read from PLANS.
//
// The system map's criterion 1 reads "switching updates EVERY tier's price and note".
// Measured against the shipped page that is false: PLANS[2] (Firm / Enterprise) carries
// the same priceMonthly/priceAnnual ('Custom') and the same metaMonthly/metaAnnual
// ('PARTNER PROGRAM ... UNLIMITED TENANTS') in both periods, by design -- it is a
// contact-sales tier, not a metered one. So this file asserts the invariant that is
// actually true: each rendered value equals the imported PLANS value for the CURRENTLY
// SELECTED period, never "every tier's value changes" (see Decisions -> [f12-invariant]).
// A fixture-discrimination guard (F12-b) keeps that weaker claim honest: at least one
// tier's price and one tier's note must differ by period, or "matches the selected
// period" would be trivially satisfied by a build where the toggle does nothing.
//
// Billing notes are read by containment within each `#pricing .ios-price` card, not via
// `.mono` -- `#pricing .mono` resolves to 8 nodes (the Annual badge, 3 `/mo` units, the
// POPULAR badge, 3 meta divs minus overlaps) and is not a 1:1 card mapping. Same setup
// contract as App.signIn.dom.test.tsx: production URL, an installed memory localStorage,
// a console.error spy asserted empty.
//
// Criterion 3 (every tier's CTA opens the demo modal) is proved in
// App.demoCtas.dom.test.tsx, not here -- per Decisions -> [f12-criterion-3-placement],
// the 3 pricing CTAs are 3 of that file's 10-button roster and opening the modal needs
// the App-owned dialog, which this component-alone mount does not have.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from '../consent'
import { PLANS } from '../data'
import { Pricing } from './Pricing'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

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

async function mountPricing(): Promise<void> {
  await act(async () => {
    root.render(createElement(Pricing, { onBookDemo: () => undefined }))
  })
}

// The 2 period buttons carry no role/id/class of their own; the only `#pricing` buttons
// that DO carry a class are the 3 per-tier CTAs (`v2-btn`). Select structurally and click
// by index -- per Decisions, no text matching: the Annual button's label measures
// "Annual –2 MONTHS" (en dash), which an exact-textContent match would silently miss.
function getPeriodButtons(): HTMLButtonElement[] {
  return Array.from(document.querySelectorAll<HTMLButtonElement>('#pricing button')).filter(
    (b) => !b.className.includes('v2-btn'),
  )
}

function getMoneys(): Element[] {
  return Array.from(document.querySelectorAll('#pricing .money'))
}

function getCards(): Element[] {
  return Array.from(document.querySelectorAll('#pricing .ios-price'))
}

describe('F-12: one billing period, defaulting to Monthly, prices/notes from PLANS', () => {
  it('F12-a: control needle -- 3 plans, 3 .money, 3 .ios-price, 2 period buttons', async () => {
    expect(PLANS.length).toBe(3)
    await mountPricing()
    expect(getMoneys().length).toBe(PLANS.length)
    expect(getCards().length).toBe(PLANS.length)
    expect(getPeriodButtons().length).toBe(2)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F12-b: fixture can discriminate -- at least one tier differs by period on price and on note', () => {
    expect(PLANS.some((p) => p.priceMonthly !== p.priceAnnual)).toBe(true)
    expect(PLANS.some((p) => p.metaMonthly !== p.metaAnnual)).toBe(true)
  })

  it('F12-c: default period is Monthly -- exactly one active button, label starts with Monthly', async () => {
    await mountPricing()
    const buttons = getPeriodButtons()
    expect(buttons.length).toBe(2)
    const active = buttons.filter((b) => b.style.background === 'var(--bg-2)')
    expect(active.length).toBe(1)
    expect(active[0].textContent?.trim().startsWith('Monthly')).toBe(true)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F12-d: at rest -- each .money equals PLANS[i].priceMonthly, each card contains metaMonthly', async () => {
    await mountPricing()
    const moneys = getMoneys()
    const cards = getCards()
    expect(moneys.length).toBe(PLANS.length)
    expect(cards.length).toBe(PLANS.length)
    PLANS.forEach((p, i) => {
      expect(moneys[i].textContent, `tier ${i} price at rest`).toBe(p.priceMonthly)
      expect(cards[i].textContent?.includes(p.metaMonthly), `tier ${i} note at rest`).toBe(true)
    })
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F12-e: after Annual -- each .money equals PLANS[i].priceAnnual, each card contains metaAnnual', async () => {
    await mountPricing()
    const buttons = getPeriodButtons()
    expect(buttons.length).toBe(2)
    await act(async () => {
      buttons[1].click()
    })

    const moneys = getMoneys()
    const cards = getCards()
    expect(moneys.length).toBe(PLANS.length)
    expect(cards.length).toBe(PLANS.length)
    PLANS.forEach((p, i) => {
      expect(moneys[i].textContent, `tier ${i} price after Annual`).toBe(p.priceAnnual)
      expect(cards[i].textContent?.includes(p.metaAnnual), `tier ${i} note after Annual`).toBe(true)
    })
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F12-f: after Annual -- exactly one active button, and it is the Annual one', async () => {
    await mountPricing()
    const buttons = getPeriodButtons()
    await act(async () => {
      buttons[1].click()
    })

    const active = getPeriodButtons().filter((b) => b.style.background === 'var(--bg-2)')
    expect(active.length).toBe(1)
    // Matched by index, not exact textContent -- the label measures "Annual –2 MONTHS".
    expect(active[0]).toBe(getPeriodButtons()[1])
    expect(consoleError).not.toHaveBeenCalled()
  })
})
