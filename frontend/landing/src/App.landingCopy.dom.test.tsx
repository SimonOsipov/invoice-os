// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// QA gap-fill. The retired-copy sweep in e2e/envCopy.test.ts only proves the OLD strings are
// absent; a reword to a third wording passes it. These are the positive halves, and they run
// on every push — the Playwright spec that also pins them runs only on a PR deploy gate.
//
// Each selector below is the one e2e/smoke/landing-content.spec.ts uses, asserted against the
// same rendered tree, so a selector that resolves to the wrong count fails here rather than
// costing a fleet rebuild.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import App from './App'

const HERO_PARAGRAPH = 'Bring invoices, approvals and changing country requirements into one connected solution. Available for Nigeria.'
const MODULES_HEADING = 'ASComply is your invoice compliance solution.'
const MODULES_INTRO_SECOND =
  'We help your team validate invoices before they are submitted, manage approvals internally, store audit-ready records and submit them to the regulatory bodies.'
const FOOTER_TAGLINE = 'Clarity for every invoice. Confidence for your business.'

function mount(): Document {
  document.body.innerHTML = renderToStaticMarkup(createElement(App))
  return document
}

// JSX wraps these strings over several source lines; Playwright's toHaveText normalizes the
// same way, so the comparison holds on both sides.
function textOf(el: Element | null): string {
  return (el?.textContent ?? '').replace(/\s+/g, ' ').trim()
}

describe('landing positioning copy, on the rendered tree', () => {
  it('the hero lead reads as v2', () => {
    const d = mount()
    const hero = d.querySelectorAll('#top p')
    expect(hero.length, '#top does not hold exactly one paragraph').toBe(1)
    expect(textOf(hero[0])).toBe(HERO_PARAGRAPH)
  })

  it('the Solution heading and its second intro paragraph read the Q4 wording', () => {
    const d = mount()
    const heading = d.querySelectorAll('#solution h2')
    expect(heading.length, '#solution does not hold exactly one heading').toBe(1)
    expect(textOf(heading[0])).toBe(MODULES_HEADING)

    const intro = d.querySelectorAll('#solution p:not(.mod-body)')
    expect(intro.length, '#solution does not hold exactly two intro paragraphs').toBe(2)
    expect(textOf(intro[1])).toBe(MODULES_INTRO_SECOND)
  })

  it('the footer tagline reads as v2', () => {
    const d = mount()
    const tagline = d.querySelectorAll('footer p')
    expect(tagline.length, 'footer does not hold exactly one tagline paragraph').toBe(1)
    expect(textOf(tagline[0])).toBe(FOOTER_TAGLINE)
  })
})

describe('landing names Nigeria only', () => {
  const bodyText = () => textOf(mount().body)

  it('no element on / links #coverage and #coverage is gone', () => {
    const d = mount()
    expect(d.querySelector('#coverage')).toBeNull()
    expect(d.querySelectorAll('[href$="#coverage"]')).toHaveLength(0)
    expect(d.querySelector('#intelligence'), 'control: #intelligence exists').not.toBeNull()
    expect(d.querySelectorAll('footer a[href$="#intelligence"]'), 'control: the footer links #intelligence').toHaveLength(1)
    expect(d.querySelectorAll('#intelligence button[aria-pressed]'), 'control: the four steps are pressed-state buttons').toHaveLength(4)
    expect(d.querySelectorAll('#intelligence button[aria-pressed]:not([data-intel-step])')).toHaveLength(0)
  })

  it('the landing text names no other country and no expansion', () => {
    const text = bodyText()
    expect(text, 'control: the H1 reads Nigeria').toContain('Nigeria moves.')
    for (const re of [/kenya/i, /south africa/i, /\bKRA\b/, /etims/i, /\bSARS\b/, /pan-african/i, /expansion/i, /across africa/i, /nigeria and africa/i, /africa moves/i]) {
      expect(text, String(re)).not.toMatch(re)
    }
  })

  it('every roadmap sentence on / is a kept sentence', () => {
    const sentences = (bodyText().match(/[^.]*roadmap[^.]*\./gi) ?? []).map((s) => s.trim())
    expect(sentences).toEqual([
      'Our roadmap connects official updates, AI-assisted analysis and human review.',
      'The systems shown on this page are roadmap targets, not confirmed partnerships or available connectors.',
    ])
  })
})
