// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-5: the validation-preview rows, and the tally invariant.
//
// Both tally strings (in Hero.tsx) are HARDCODED literals -- HERO_CHECKS is
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
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from '../App'
import type { ConsentStore } from '../consent'
import { HERO_CHECKS } from '../data'
import { GLYPHS } from '../icons'
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
    root.render(createElement(Hero, { onBookDemo: () => undefined }))
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

// jsdom drops shorthands and var() values, so inline styles are read from the SSR markup.
function ssrFragment(node: ReturnType<typeof createElement>): DocumentFragment {
  const t = document.createElement('template')
  t.innerHTML = renderToStaticMarkup(node)
  return t.content
}
const ssrHero = () => ssrFragment(createElement(Hero, { onBookDemo: () => undefined }))

const styleOf = (el: Element | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const decl of (el?.getAttribute('style') ?? '').split(/;(?![^(]*\))/)) {
    const i = decl.indexOf(':')
    if (i > 0) out[decl.slice(0, i).trim()] = decl.slice(i + 1).trim()
  }
  return out
}

const textOf = (el: Element | null | undefined): string => (el?.textContent ?? '').replace(/\s+/g, ' ').trim()
const pathsOf = (el: Element | null | undefined): string[] => [...(el?.querySelectorAll('svg path') ?? [])].map((p) => p.getAttribute('d') ?? '')

async function mountCard(): Promise<HTMLElement> {
  await mountHero()
  const card = document.querySelector<HTMLElement>('#top .hero-card')
  expect(card, 'expected #top .hero-card to render').not.toBeNull()
  return card!
}

describe('HC-01 the card carries the elegant shadow, alone in #top', () => {
  it('exactly one element in #top has an inline box-shadow: .card-floating.hero-card, var(--shadow-elegant)', () => {
    const top = ssrHero().querySelector('#top')!
    const shadowed = [top, ...top.querySelectorAll('*')].filter((el) => 'box-shadow' in styleOf(el))
    expect(shadowed.length, 'expected exactly one inline box-shadow in #top').toBe(1)
    expect([...shadowed[0].classList].sort()).toEqual(['card-floating', 'hero-card'])
    expect(styleOf(shadowed[0])['box-shadow']).toBe('var(--shadow-elegant)')
    expect([top, ...top.querySelectorAll('*')].filter((el) => el.getAttribute('style')?.includes('--shadow-elegant')).length).toBe(1)
  })
})

describe('HC-02 the card head, invoice number and badge', () => {
  it('holds the title, the Illustrative view tag, the invoice number and one progress Validating badge with a dot', async () => {
    const card = await mountCard()
    expect(textOf(card.querySelector('.t-card-title'))).toBe('ASComply Platform')
    const meta = [...card.querySelectorAll('.t-meta')].map(textOf)
    expect(meta).toContain('Illustrative view')
    expect(meta).toContain('INV-2026-00481')
    const badges = card.querySelectorAll('.ds-badge')
    expect(badges.length, 'expected exactly one badge in the card').toBe(1)
    expect(badges[0].classList.contains('ds-badge--progress')).toBe(true)
    expect(badges[0].querySelectorAll('.ds-badge-dot').length).toBe(1)
    expect(textOf(badges[0])).toBe('Validating')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('HC-03 rows fade in 120ms apart', () => {
  it('there are HERO_CHECKS.length .hero-row elements with animation-delay i x 120ms, in order', () => {
    const rows = [...ssrHero().querySelectorAll('#top .hero-row')]
    expect(HERO_CHECKS.length).toBe(6)
    expect(rows.length).toBe(HERO_CHECKS.length)
    expect(rows.map((r) => styleOf(r)['animation-delay'])).toEqual(['0ms', '120ms', '240ms', '360ms', '480ms', '600ms'])
    rows.forEach((r, i) => expect(textOf(r), `row ${i} reads its own check`).toBe(HERO_CHECKS[i].label + HERO_CHECKS[i].tag))
  })
})

describe('HC-04 each row icon follows its outcome', () => {
  it('PASS rows are check on success, the WARN row triangle-alert on progress, the FAIL row x on destructive', () => {
    const rows = [...ssrHero().querySelectorAll('#top .hero-row')]
    expect(rows.length, 'control: the rows rendered').toBe(HERO_CHECKS.length)
    expect(HERO_CHECKS.map((c) => c.tag)).toEqual(['PASS', 'PASS', 'PASS', 'WARN', 'PASS', 'FAIL'])

    const want = {
      PASS: { glyph: GLYPHS.check, bg: 'var(--status-success-bg)', fg: 'var(--status-success-fg)' },
      WARN: { glyph: GLYPHS['triangle-alert'], bg: 'var(--status-progress-bg)', fg: 'var(--status-progress-fg)' },
      FAIL: { glyph: GLYPHS.x, bg: 'color-mix(in srgb, var(--destructive) 14%, var(--card))', fg: 'var(--destructive)' },
    } as const
    HERO_CHECKS.forEach((c, i) => {
      const icon = rows[i].querySelector('span')!
      const w = want[c.tag as keyof typeof want]
      expect(pathsOf(icon), `row ${i} (${c.tag}) glyph`).toEqual([...w.glyph])
      expect(styleOf(icon).background, `row ${i} (${c.tag}) background`).toBe(w.bg)
      expect(styleOf(icon).color, `row ${i} (${c.tag}) colour`).toBe(w.fg)
      const tag = [...rows[i].querySelectorAll('span')].find((s) => textOf(s) === c.tag)
      expect(styleOf(tag).color, `row ${i} tag colour follows the icon`).toBe(w.fg)
    })
  })
})

describe('HC-05 the scanline is decorative', () => {
  it('exactly one .hero-scan, aria-hidden, with no text', async () => {
    await mountHero()
    const scans = document.querySelectorAll('#top .hero-scan')
    expect(scans.length).toBe(1)
    expect(scans[0].getAttribute('aria-hidden')).toBe('true')
    expect(scans[0].textContent).toBe('')
  })
})

describe('HC-06 the two tiles', () => {
  it('primary check-check beside Invoice workflow, accent sparkles beside Regulatory intelligence', async () => {
    const card = await mountCard()
    const tiles = [...card.querySelectorAll('.ds-icontile')]
    expect(tiles.length).toBe(2)
    const want = [
      ['ds-icontile--primary', GLYPHS['check-check'], 'Invoice workflow', 'Validate. Review. Keep the record.'],
      ['ds-icontile--accent', GLYPHS.sparkles, 'Regulatory intelligence', 'AI-supported.'],
    ] as const
    want.forEach(([tone, glyph, title, caption], i) => {
      expect(tiles[i].classList.contains(tone), `tile ${i} tone`).toBe(true)
      expect(pathsOf(tiles[i])).toEqual([...glyph])
      const text = tiles[i].nextElementSibling
      expect(text, `tile ${i} has a text block beside it`).not.toBeNull()
      expect(text!.children.length).toBe(2)
      expect(textOf(text!.children[0])).toBe(title)
      expect(textOf(text!.children[1])).toBe(caption)
      expect(text!.children[1].classList.contains('t-caption')).toBe(true)
    })
  })
})

describe('HC-07 the card adds no paragraph and keeps the tally hooks unique', () => {
  it('on the SSR App, #top holds the lead as its one p, the card none, and one of each data-tally', () => {
    const app = ssrFragment(createElement(App))
    const card = app.querySelector('#top .hero-card')
    expect(card, 'expected the card in the SSR App').not.toBeNull()
    expect(card!.querySelectorAll('p').length, 'no <p> in the card').toBe(0)
    expect(app.querySelectorAll('#top p').length).toBe(1)
    expect(app.querySelectorAll(FAILURES_TALLY).length).toBe(1)
    expect(app.querySelectorAll(PASSED_TALLY).length).toBe(1)
    expect(card!.querySelectorAll(FAILURES_TALLY).length, 'the tallies live in the card').toBe(1)
    expect(card!.querySelectorAll(PASSED_TALLY).length).toBe(1)
  })
})
