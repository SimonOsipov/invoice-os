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
import { FINTECH, FIRM, INHOUSE } from './data'

const HERO_PARAGRAPH =
  "ASComply Africa is the solution between your business and Nigeria's Merchant Buyer Solution. Create, validate, approve, archive, and transmit compliant invoices — through the dashboard or the API."
const MODULES_HEADING = 'ASComply is your invoice compliance solution.'
const MODULES_INTRO_SECOND =
  'We help your team validate invoices before they are submitted, manage approvals internally, store audit-ready records and submit them to the regulatory bodies.'
const FOOTER_TAGLINE = 'E-invoicing compliance solution for African businesses.'

const STEP_01_BODY =
  'Pull invoices from your ERP via API, or upload CSV / XLSX from any accounting system, or a PDF or scan of the invoice. No migration.'
const STEP_01_POINTS = ['REST API & webhooks', 'CSV / XLSX / PDF import', 'ERP connectors']
const STEP_02_TITLE = 'Validate against MBS rules — and your own'
const STEP_02_BODY =
  'Every invoice is checked against the golden MBS rule pack — tax IDs, VAT/WHT, totals, duplicates, mandatory fields — plus the rules your company adds on top.'
const STEP_02_POINTS = ['Golden MBS rule pack', 'Your own company rules', 'Inline fix suggestions']

const FIRM_BODY =
  'Manage filings, validation queues and readiness scores across your whole book of business. Switch between clients in one login, and become their compliance partner.'

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
  it('the hero paragraph names ASComply the solution, not a layer', () => {
    const d = mount()
    const hero = d.querySelectorAll('#top p')
    expect(hero.length, '#top does not hold exactly one paragraph').toBe(1)
    expect(textOf(hero[0])).toBe(HERO_PARAGRAPH)
  })

  it('the Solution heading and its second intro paragraph read as shipped', () => {
    const d = mount()
    const heading = d.querySelectorAll('#modules h2')
    expect(heading.length, '#modules does not hold exactly one heading').toBe(1)
    expect(textOf(heading[0])).toBe(MODULES_HEADING)

    const intro = d.querySelectorAll('#modules p:not(.mod-body)')
    expect(intro.length, '#modules does not hold exactly two intro paragraphs').toBe(2)
    expect(textOf(intro[1])).toBe(MODULES_INTRO_SECOND)
  })

  it('the footer tagline is one sentence', () => {
    const d = mount()
    const tagline = d.querySelectorAll('footer p')
    expect(tagline.length, 'footer does not hold exactly one tagline paragraph').toBe(1)
    expect(textOf(tagline[0])).toBe(FOOTER_TAGLINE)
  })

  it('How it works steps 01 and 02 read as shipped', () => {
    const d = mount()
    const cells = d.querySelectorAll('#how .ios-grid > div')
    expect(cells.length, '#how does not hold exactly three step cells').toBe(3)

    expect(textOf(cells[0].querySelector('p'))).toBe(STEP_01_BODY)
    for (const point of STEP_01_POINTS) {
      expect(textOf(cells[0]), `step 01 cell missing chip: ${point}`).toContain(point)
    }

    expect(textOf(cells[1].querySelector('h3'))).toBe(STEP_02_TITLE)
    expect(textOf(cells[1].querySelector('p'))).toBe(STEP_02_BODY)
    for (const point of STEP_02_POINTS) {
      expect(textOf(cells[1]), `step 02 cell missing chip: ${point}`).toContain(point)
    }
  })
})

describe('audience tab copy, on the rendered tree', () => {
  // The 2 stacked-layer columns' direct children, 3 layers each: mocks 0-2, copy 3-5.
  // The naive '#accountants [aria-hidden]' also catches every glyph svg.
  const LAYER_SELECTOR = '#accountants .ios-grid > div > div[aria-hidden]'

  it('all three audiences carry the same number of features', () => {
    expect(FIRM.features).toHaveLength(5)
    expect(INHOUSE.features).toHaveLength(5)
    expect(FINTECH.features).toHaveLength(5)
  })

  it('the firms body drops the distribution-channel clause and gains the fifth feature', () => {
    const d = mount()
    const layers = d.querySelectorAll(LAYER_SELECTOR)
    expect(layers.length, 'layer selector must resolve to exactly 6 elements').toBe(6)

    const firm = textOf(layers[3])
    expect(firm).toContain(FIRM_BODY)
    expect(firm).not.toContain('distribution channel for ASComply')
    expect(firm).toContain('Per-client rule sets')
    expect(firm).toContain("Stack each client's own checks on top of the golden MBS rules.")
  })

  it("the fifth firms glyph matches its siblings' size and stroke", () => {
    const d = mount()
    const layers = d.querySelectorAll(LAYER_SELECTOR)
    expect(layers.length, 'layer selector must resolve to exactly 6 elements').toBe(6)

    const glyphs = layers[3].querySelectorAll('span > svg')
    expect(glyphs.length, 'the firms copy layer must hold exactly 5 feature glyphs').toBe(5)
    for (const g of glyphs) {
      expect(g.getAttribute('width')).toBe('18')
      expect(g.getAttribute('height')).toBe('18')
      expect(g.getAttribute('viewBox')).toBe('0 0 24 24')
      expect(g.getAttribute('stroke-width')).toBe('1.6')
    }
  })

  it('the in-house ERP feature names Odoo', () => {
    const d = mount()
    const layers = d.querySelectorAll(LAYER_SELECTOR)
    expect(layers.length, 'layer selector must resolve to exactly 6 elements').toBe(6)
    expect(textOf(layers[4])).toContain('Two-way sync with SAP, NetSuite, Sage, QuickBooks & Odoo.')
  })
})
