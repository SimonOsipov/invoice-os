// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-3: all ten rendered demo CTAs -- spread across six scopes, in six different
// components -- open the same "Book a demo" modal. Also discharges F-12 criterion 3
// (every pricing tier's CTA opens the demo modal): roster entries 6-8 are the three
// PLANS[].cta values, the same ones Pricing.billingPeriod.dom.test.tsx reads; per
// Decisions -> [f12-criterion-3-placement] that file points back here rather than
// re-proving it, because proving it needs the App-owned dialog a component-alone
// mount does not have.
//
// LIMIT (Decisions -> qa-debate J2): the three #accountants CTAs (FIRM/INHOUSE/FINTECH)
// are clicked while only one AudienceCopy layer is visible -- the other two sit behind
// `visibility: hidden` / `aria-hidden` (Audience.tsx:380-388), never selected via their
// tab first. jsdom runs no CSS layout or visibility engine, so those clicks land. That
// is sound for what F-3 claims: the same onBookDemo is wired identically on all three,
// so there is no false pass. It is NOT proof a visitor can reach the hidden two -- a
// regression that made a hidden layer genuinely non-interactive (an inverted
// pointer-events: none, say) would not be caught here, and no e2e closes the gap either:
// landing-demo.spec.ts:259 clicks only the banner CTA.
//
// Same setup contract as App.signIn.dom.test.tsx: production URL, an installed memory
// localStorage, a console.error spy asserted empty.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'
import { FIRM, INHOUSE, FINTECH, PLANS } from './data'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const DEMO_DIALOG_LABEL = 'Book a demo'
const SCOPES = ['header', '#top', '#accountants', '#pricing', '#demo', 'footer']

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

  vi.resetModules()
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

async function mountApp(): Promise<void> {
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
}

// Local, unexported copy per Decisions -> [click-by-text-duplicated]: neither existing
// copy (consentActions.mount.dom.test.tsx / App.signIn.dom.test.tsx) is imported. The
// `root` parameter is what makes "exactly one within this scope" expressible.
async function clickByText(root: ParentNode, text: string): Promise<void> {
  const button = Array.from(root.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
  expect(button, `expected a button labelled "${text}" within the given scope`).toBeDefined()
  await act(async () => {
    button!.click()
  })
}

// Scope + label roster, in page order. Six of the ten labels are the imported constant
// the CTA renders (FIRM/INHOUSE/FINTECH.cta, PLANS[].cta); the other four -- Nav, Hero,
// DemoCta and Footer's own "Book a demo" -- have no shared constant behind them (Footer's
// copy lives in a local, unexported `COLS` array), so they are given as the literal each
// component renders. Labels repeat ACROSS scopes ("Book a demo" is Nav's, PLANS[1]'s and
// Footer's) but never WITHIN one -- that is what the per-entry scope assertion checks.
const ROSTER: { scope: string; label: string }[] = [
  { scope: 'header', label: 'Book a demo' }, // Nav.tsx:180
  { scope: '#top', label: 'Book a demo →' }, // Hero.tsx:38
  { scope: '#accountants', label: FIRM.cta },
  { scope: '#accountants', label: INHOUSE.cta },
  { scope: '#accountants', label: FINTECH.cta },
  { scope: '#pricing', label: PLANS[0].cta },
  { scope: '#pricing', label: PLANS[1].cta },
  { scope: '#pricing', label: PLANS[2].cta },
  { scope: '#demo', label: 'Book my demo →' }, // DemoCta.tsx:176
  { scope: 'footer', label: 'Book a demo' }, // Footer.tsx:84
]

// The eight controls in these six scopes that are NOT demo CTAs, named so the 18-button
// completeness guard below (F3-f) is not a magic number:
//   header        -- "Explore the platform" (sign-in)
//   #top          -- "Explore the platform" (sign-in)
//   #accountants  -- the firm / inhouse / fintech audience-switch tabs (3)
//   #pricing      -- the Monthly / Annual billing-period toggle (2)
//   footer        -- "Cookie choices"
const NON_CTA_COUNT = 8

describe('F-3: all ten rendered demo CTAs open the same modal', () => {
  it('F3-a: control needle -- zero dialogs at rest, and every scope resolves to >= 1 button', async () => {
    await mountApp()
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    for (const scope of SCOPES) {
      const scopeEl = document.querySelector(scope)
      expect(scopeEl, `expected scope "${scope}" to resolve`).not.toBeNull()
      expect(scopeEl!.querySelectorAll('button').length, `expected >=1 button in "${scope}"`).toBeGreaterThan(0)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F3-b: the roster is exactly 10 entries, and no scope hides a duplicate CTA label', () => {
    expect(ROSTER.length).toBe(10)
    expect(new Set(PLANS.map((p) => p.cta)).size).toBe(3)
    expect(new Set([FIRM.cta, INHOUSE.cta, FINTECH.cta]).size).toBe(3)
  })

  // F3-f: measured 2 (header) + 2 (#top) + 6 (#accountants) + 5 (#pricing) + 1 (#demo) +
  // 2 (footer) = 18 = the 10-entry roster + the 8 named non-CTA controls above. Asserted
  // with the demo modal closed -- App.tsx:114-115 mounts SignInModal/DemoModal as
  // siblings of Footer, outside every one of these six scopes, but an OPEN modal still
  // adds buttons to the page (its own Close, and form controls) that this total ignores
  // by construction.
  it('F3-f: the six scopes hold exactly 18 buttons in total, modal closed', async () => {
    await mountApp()
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    const total = SCOPES.reduce((sum, scope) => sum + document.querySelector(scope)!.querySelectorAll('button').length, 0)
    expect(total).toBe(ROSTER.length + NON_CTA_COUNT)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each(ROSTER)('F3-c/d/e: "$label" in "$scope" resolves once, opens the demo dialog, and closes cleanly', async ({ scope, label }) => {
    await mountApp()
    const scopeEl = document.querySelector(scope)!

    const matches = Array.from(scopeEl.querySelectorAll('button')).filter((b) => b.textContent?.trim() === label)
    expect(matches.length, `expected exactly one "${label}" button within "${scope}"`).toBe(1)

    await clickByText(scopeEl, label)

    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const dialog = document.querySelector(DIALOG)!
    expect(dialog.getAttribute('aria-label')).toBe(DEMO_DIALOG_LABEL)

    const closeButton = dialog.querySelector<HTMLButtonElement>('button[aria-label="Close"]')
    expect(closeButton, 'expected the modal Close control').not.toBeNull()
    await act(async () => {
      closeButton!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })
})
