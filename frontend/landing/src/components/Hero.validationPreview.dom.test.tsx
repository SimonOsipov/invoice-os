// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-5: the validation-preview rows, and the tally invariant.
//
// Both tally strings (Hero.tsx:173,177) are HARDCODED literals -- HERO_CHECKS is
// consumed only by the .map() that renders the rows, never by the tally. So this
// test relates two literals to the imported list; it proves nothing about a
// computation, and it cannot catch a pair of literals that are wrong but
// self-consistent. It catches exactly one defect: an edit to HERO_CHECKS that is
// not mirrored in the tally (retag a row, add one, remove one -- the derived
// counts move, the literals do not).
//
// The list is a six-row excerpt of a fictional sixteen-check run, so `passed ===
// HERO_CHECKS.length - failures` is deliberately NOT asserted below -- that reading
// demands `14 === 4`, which is red against correct code. Same setup contract as
// App.signIn.dom.test.tsx: production URL, an installed memory localStorage, a
// console.error spy asserted empty.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from '../consent'
import { HERO_CHECKS } from '../data'
import { Hero } from './Hero'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const FAILURES_TALLY = '[data-tally="failures"]'
const PASSED_TALLY = '[data-tally="passed"]'

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

async function mountHero(): Promise<void> {
  await act(async () => {
    root.render(createElement(Hero, { onBookDemo: () => undefined, onSignIn: () => undefined }))
  })
}

describe('F-5: the validation-preview rows, and the tally invariant', () => {
  it('F5u-a: control needle -- HERO_CHECKS has 6 entries, and the mount renders spans', async () => {
    expect(HERO_CHECKS.length).toBe(6)
    await mountHero()
    expect(document.querySelectorAll('#top span').length).toBeGreaterThan(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F5u-b: one row per entry, pairing its own label with its own tag', async () => {
    await mountHero()
    for (const c of HERO_CHECKS) {
      const labelSpans = Array.from(document.querySelectorAll('#top span')).filter((s) => s.textContent === c.label)
      expect(labelSpans.length, `expected exactly one span labelled "${c.label}"`).toBe(1)
      const row = labelSpans[0].parentElement
      expect(row, 'expected the label span to have a parent row').not.toBeNull()
      expect(row!.textContent).toBe(c.label + c.tag)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F5u-c: the tag vocabulary is closed to PASS/WARN/FAIL -- the only oracle for the untyped `tag`', () => {
    const tags = new Set(HERO_CHECKS.map((c) => c.tag))
    expect(tags).toEqual(new Set(['PASS', 'WARN', 'FAIL']))
  })

  it('F5u-d/e/f: the rendered tally agrees with HERO_CHECKS under the resolved invariant', async () => {
    await mountHero()
    const failuresEl = document.querySelector(FAILURES_TALLY)
    const passedEl = document.querySelector(PASSED_TALLY)
    expect(failuresEl, 'expected [data-tally="failures"] to resolve').not.toBeNull()
    expect(passedEl, 'expected [data-tally="passed"] to resolve').not.toBeNull()

    const failMatch = failuresEl!.textContent!.match(/^(\d+) ERROR · (\d+) WARNING$/)
    expect(failMatch, `"${failuresEl!.textContent}" did not match the expected tally format`).not.toBeNull()
    const passMatch = passedEl!.textContent!.match(/^(\d+) \/ (\d+) CHECKS PASSED$/)
    expect(passMatch, `"${passedEl!.textContent}" did not match the expected tally format`).not.toBeNull()

    const errors = Number(failMatch![1])
    const warnings = Number(failMatch![2])
    const passed = Number(passMatch![1])
    const total = Number(passMatch![2])

    const failCount = HERO_CHECKS.filter((c) => c.tag === 'FAIL').length
    const warnCount = HERO_CHECKS.filter((c) => c.tag === 'WARN').length

    // F5u-d
    expect(errors).toBe(failCount)
    expect(warnings).toBe(warnCount)
    // F5u-e -- the shortfall, NOT `passed === HERO_CHECKS.length - failCount`
    // (that reading demands 14 === 4 and is red against correct code).
    expect(total - passed).toBe(failCount + warnCount)
    // F5u-f -- the six rows are an excerpt, never larger than the run they summarize.
    expect(total).toBeGreaterThanOrEqual(HERO_CHECKS.length)

    expect(consoleError).not.toHaveBeenCalled()
  })
})
