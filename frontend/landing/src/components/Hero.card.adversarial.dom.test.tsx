// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The card renders from HERO_CHECKS, and nothing else in the frame names --shadow-elegant.
/// <reference types="node" />
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

import App from '../App'
import type { HeroCheck } from '../data'
import { GLYPHS } from '../icons'
import { Hero } from './Hero'

const state = vi.hoisted(() => ({ checks: null as HeroCheck[] | null }))

vi.mock('../data', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../data')>()
  return {
    ...actual,
    get HERO_CHECKS() {
      return state.checks ?? actual.HERO_CHECKS
    },
  }
})

function ssr(node: ReturnType<typeof createElement>): DocumentFragment {
  const t = document.createElement('template')
  t.innerHTML = renderToStaticMarkup(node)
  return t.content
}
const hero = () => ssr(createElement(Hero, { onBookDemo: () => undefined }))
const styleOf = (el: Element | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const decl of (el?.getAttribute('style') ?? '').split(/;(?![^(]*\))/)) {
    const i = decl.indexOf(':')
    if (i > 0) out[decl.slice(0, i).trim()] = decl.slice(i + 1).trim()
  }
  return out
}

const EIGHT: HeroCheck[] = [
  { label: 'a', tag: 'FAIL', icon: 'x', bg: 'var(--bg-a)', fg: 'var(--fg-a)' },
  { label: 'b', tag: 'PASS', icon: 'check', bg: 'var(--bg-b)', fg: 'var(--fg-b)' },
  { label: 'c', tag: 'WARN', icon: 'triangle-alert', bg: 'var(--bg-c)', fg: 'var(--fg-c)' },
  { label: 'd', tag: 'WARN', icon: 'sparkles', bg: 'var(--bg-d)', fg: 'var(--fg-d)' },
  { label: 'e', tag: 'PASS', icon: 'check-check', bg: 'var(--bg-e)', fg: 'var(--fg-e)' },
  { label: 'f', tag: 'FAIL', icon: 'x', bg: 'var(--bg-f)', fg: 'var(--fg-f)' },
  { label: 'g', tag: 'PASS', icon: 'check', bg: 'var(--bg-g)', fg: 'var(--fg-g)' },
  { label: 'h', tag: 'PASS', icon: 'check', bg: 'var(--bg-h)', fg: 'var(--fg-h)' },
]

describe('HC-12 the rows come from HERO_CHECKS, one per entry, in order', () => {
  it('eight entries give eight rows, row i delayed i x 120ms and drawn from its own entry', () => {
    state.checks = EIGHT
    try {
      const rows = [...hero().querySelectorAll('#top .hero-row')]
      expect(rows.length).toBe(EIGHT.length)
      EIGHT.forEach((c, i) => {
        expect(styleOf(rows[i])['animation-delay'], `row ${i} delay`).toBe(`${i * 120}ms`)
        expect(rows[i].textContent, `row ${i} reads its own entry`).toBe(c.label + c.tag)
        const icon = rows[i].querySelector('span')!
        expect([...icon.querySelectorAll('svg path')].map((p) => p.getAttribute('d'))).toEqual([...GLYPHS[c.icon]])
        expect(styleOf(icon).background, `row ${i} icon background`).toBe(c.bg)
        expect(styleOf(icon).color, `row ${i} icon colour`).toBe(c.fg)
      })
    } finally {
      state.checks = null
    }
  })

  it('no entries give no rows and no phantom one, and the card still renders', () => {
    state.checks = []
    try {
      const top = hero().querySelector('#top')!
      expect(top.querySelectorAll('.hero-card').length, 'control: the card rendered').toBe(1)
      expect(top.querySelectorAll('.hero-row').length).toBe(0)
    } finally {
      state.checks = null
    }
  })

  it('the real HERO_CHECKS has unique labels (they are the React keys) and a drawn glyph per row', async () => {
    const { HERO_CHECKS } = await vi.importActual<typeof import('../data')>('../data')
    expect(HERO_CHECKS.length).toBe(6)
    expect(new Set(HERO_CHECKS.map((c) => c.label)).size).toBe(HERO_CHECKS.length)
    const rows = [...hero().querySelectorAll('#top .hero-row')]
    expect(rows.length).toBe(HERO_CHECKS.length)
    rows.forEach((r, i) => expect(r.querySelectorAll('span svg path').length, `row ${i} draws a glyph`).toBeGreaterThan(0))
  })
})

describe('HC-13 the scanline is decorative: hidden, empty, unfocusable', () => {
  it('.hero-scan has aria-hidden, no children, no role and no tab stop, and precedes the rows', () => {
    const scan = hero().querySelector('#top .hero-scan')!
    expect(scan, 'control: the scanline rendered').not.toBeNull()
    expect(scan.getAttribute('aria-hidden')).toBe('true')
    expect(scan.childNodes.length).toBe(0)
    expect(scan.hasAttribute('role')).toBe(false)
    expect(scan.hasAttribute('tabindex')).toBe(false)
    expect(scan.nextElementSibling?.classList.contains('hero-row'), 'the first row follows the scanline').toBe(true)
  })
})

describe('HC-14 --shadow-elegant is named once across the header, #top, the audience strip and the footer (D-21)', () => {
  it('one inline use in the SSR App frame, and it is the card', () => {
    const app = ssr(createElement(App))
    const frame = ['header', '#top', '[data-strip="audience"]', 'footer'].map((sel) => app.querySelector(sel))
    frame.forEach((el, i) => expect(el, `control: frame part ${i} rendered`).not.toBeNull())
    const users = frame.flatMap((root) => [root!, ...root!.querySelectorAll('*')]).filter((el) => (el.getAttribute('style') ?? '').includes('--shadow-elegant'))
    expect(users.length).toBe(1)
    expect([...users[0].classList].sort()).toEqual(['card-floating', 'hero-card'])
  })
})
