// @vitest-environment jsdom
// FAQ band (jsdom): structure, aside copy, five items, single-open state, aria wiring. Stickiness and layout are the topology job's.
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { click, mountView, show, spyConsoleError, unmountView, type View } from './ds/dsDom.test.util'
import { Faq } from './Faq'

// FAQ (V825-830), retyped.
const V_FAQ: readonly (readonly [q: string, a: string])[] = [
  ['What does ASComply Africa do?', 'ASComply brings invoice creation, validation, approvals and records into a connected compliance workspace.'],
  [
    'Do we need to replace our accounting system?',
    'ASComply is designed to work alongside your accounting and business systems. During a demo, we can discuss your current setup, data imports and integration requirements. Prebuilt ERP and accounting connectors are in progress.',
  ],
  [
    'Are the integration partners already available?',
    'The integration and partner ecosystem is in progress. The systems shown on this page are roadmap targets, not confirmed partnerships or available connectors. Contact our team to discuss your system and current availability.',
  ],
  [
    'How does AI-supported regulatory monitoring work?',
    'The planned workflow follows official tax authority updates, uses AI assistance to summarise changes and assess potential impact, and routes the analysis to a compliance specialist for review. Approved changes can then inform country workflows.',
  ],
  [
    'Can accounting firms work with multiple clients?',
    'The platform includes a multi-client partner workspace, so accounting and tax teams can manage client companies and review their invoice workflows from one place. Ask for a walkthrough tailored to your practice.',
  ],
]

const V_EYEBROW = 'A LITTLE MORE CLARITY'
const V_H2 = 'Good questions. Clear answers.'
const V_HL = 'Clear answers.'
const V_LEAD = 'Need to go deeper?'
const V_REST = "We'll walk through your workflow."
const V_CTA = 'Talk to our team →'

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(async () => {
  view = mountView()
  consoleError = spyConsoleError()
  await show(view, createElement(Faq, { onBookDemo: () => undefined }))
})

afterEach(() => {
  expect(consoleError, 'console.error was called').not.toHaveBeenCalled()
  unmountView(view)
  vi.restoreAllMocks()
})

const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
/** Text with each <br> read as a space. */
function spaced(el: Element): string {
  const copy = el.cloneNode(true) as Element
  copy.querySelectorAll('br').forEach((br) => br.replaceWith(' '))
  return norm(copy.textContent)
}
const root = () => view.container.querySelector<HTMLElement>('#faq')
const heads = () => Array.from(view.container.querySelectorAll<HTMLButtonElement>('.ds-faq-btn'))
const answers = () => Array.from(view.container.querySelectorAll<HTMLElement>('.ds-faq-a'))

/** Index of each open item, from both the header and the answer. */
function openState() {
  return {
    expanded: heads().flatMap((h, i) => (h.getAttribute('aria-expanded') === 'true' ? [i] : [])),
    shown: answers().flatMap((a, i) => (a.hidden ? [] : [i])),
  }
}

describe('FAQ band', () => {
  it('FA-01 the band is #faq on the off-white band, aside and list side by side', () => {
    expect(view.container.children, 'Faq renders one root').toHaveLength(1)
    const r = view.container.firstElementChild!
    expect([r.localName, r.id, r.className]).toEqual(['section', 'faq', 'ds-section band-cream'])
    const splits = r.querySelectorAll('.split')
    expect(splits, 'one .split').toHaveLength(1)
    expect(
      Array.from(splits[0].children).map((c) => [c.hasAttribute('data-faq-aside'), c.hasAttribute('data-faq-list')]),
      'the aside, then the list, and nothing else',
    ).toEqual([
      [true, false],
      [false, true],
    ])
  })

  it('FA-02 the aside copy is V’s', () => {
    const aside = root()?.querySelector('[data-faq-aside]')
    expect(aside, 'control: the aside rendered').not.toBeNull()
    const eyebrows = aside!.querySelectorAll('.t-eyebrow')
    expect(eyebrows, 'one eyebrow').toHaveLength(1)
    expect(norm(eyebrows[0].textContent)).toBe(V_EYEBROW)

    const h2s = aside!.querySelectorAll('h2')
    expect(h2s, 'one h2').toHaveLength(1)
    expect(spaced(h2s[0])).toBe(V_H2)
    const spans = h2s[0].querySelectorAll('span')
    expect(spans, 'one span').toHaveLength(1)
    expect([norm(spans[0].textContent), spans[0].style.color], 'the second line is teal').toEqual([V_HL, 'var(--teal)'])

    const ps = aside!.querySelectorAll('p.t-body')
    expect(ps, 'one paragraph').toHaveLength(1)
    const strong = ps[0].querySelectorAll('strong')
    expect(strong, 'one strong').toHaveLength(1)
    expect(norm(strong[0].textContent)).toBe(V_LEAD)
    expect(spaced(ps[0])).toBe(`${V_LEAD} ${V_REST}`)

    const buttons = aside!.querySelectorAll('button')
    expect(buttons, 'one button in the aside').toHaveLength(1)
    expect(norm(buttons[0].textContent)).toBe(V_CTA)
    expect(buttons[0].classList.contains('ds-btn--text')).toBe(true)
  })

  it('FA-03 the five questions and answers, first open', () => {
    expect(root(), 'control: the band rendered').not.toBeNull()
    const list = root()!.querySelector('[data-faq-list]')
    expect(list, 'control: the list rendered').not.toBeNull()
    expect(list!.querySelectorAll('.ds-faq-btn'), 'five headers in the list').toHaveLength(V_FAQ.length)
    expect(root()!.querySelectorAll('.ds-faq-btn'), 'no header outside the list').toHaveLength(V_FAQ.length)
    expect(heads().map((h) => norm(h.textContent)), 'questions, in order').toEqual(V_FAQ.map(([q]) => q))
    expect(answers().map((a) => norm(a.textContent)), 'answers, in order').toEqual(V_FAQ.map(([, a]) => a))
    expect(openState(), 'only the first item is open').toEqual({ expanded: [0], shown: [0] })
  })

  it('FA-04 one item opens at a time', () => {
    expect(heads(), 'control: five headers').toHaveLength(V_FAQ.length)
    for (const at of [2, 4]) {
      click(heads()[at])
      expect(openState(), `after item ${at + 1}`).toEqual({ expanded: [at], shown: [at] })
    }
  })

  it('FA-05 the open item closes on a second click', () => {
    expect(openState(), 'control: item 1 starts open').toEqual({ expanded: [0], shown: [0] })
    click(heads()[0])
    expect(openState(), 'after clicking the open item').toEqual({ expanded: [], shown: [] })
    expect(answers(), 'population: five answers, all hidden').toHaveLength(V_FAQ.length)
    click(heads()[1])
    expect(openState(), 'after item 2').toEqual({ expanded: [1], shown: [1] })
  })

  it('FA-06 aria-controls names the answer', () => {
    expect(heads(), 'control: five headers').toHaveLength(V_FAQ.length)
    const ids = heads().map((h) => h.getAttribute('aria-controls'))
    expect(new Set(ids).size, 'five distinct ids').toBe(V_FAQ.length)
    ids.forEach((id, i) => {
      expect(id, `item ${i + 1} names an id`).toBeTruthy()
      expect(document.getElementById(id!), `item ${i + 1} resolves to its answer`).toBe(answers()[i])
    })
  })
})
