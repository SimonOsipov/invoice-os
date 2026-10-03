// @vitest-environment jsdom
// SSR markup, because jsdom drops shorthands and applies no CSS.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import App from '../App'
import { GLYPHS, type GlyphName } from '../icons'
import { Modules } from './Modules'

const V190 = 'ASComply sits between your business, your accounting system, your tax adviser and the regulated e-invoicing infrastructure.'
// Q4 keeps the repo wording, not V191.
const REPO_BODY_2 =
  'We help your team validate invoices before they are submitted, manage approvals internally, store audit-ready records and submit them to the regulatory bodies.'
const V865_878: readonly [string, string, GlyphName][] = [
  ['Business profile', 'Multi-tenant setup, tax details, numbering, currency, branches.', 'building-2'],
  ['User access', 'Role-based access, team invites, accountant-client links.', 'users'],
  ['Customer / vendor', 'Buyer & seller database, TIN & company verification, duplicate detection.', 'contact'],
  ['Invoice management', 'Drafts, line items, credit & debit notes, cancellations.', 'file-text'],
  ['Validation engine', 'Rule-based checks for fields, tax logic, totals, numbering.', 'circle-check'],
  ['Approval workflow', 'Creator, reviewer, approver, rejection notes, status trail.', 'check'],
  ['Document generation', 'PDF, JSON, XML/UBL export, QR placeholder, versioning.', 'file-check'],
  ['Integration / API', 'REST, webhooks, ERP connectors, API keys, OAuth2.', 'link'],
  ['Archive & audit', 'Immutable logs, document storage, search, retention rules.', 'archive'],
  ['Reporting & analytics', 'Volume, tax summaries, error patterns, readiness score.', 'chart-line'],
  ['Partner portal', 'Accountants manage multiple client companies & exports.', 'users-round'],
  ['Platform admin', 'Tenants, subscriptions, country modules, support, config.', 'settings'],
]

function toFrag(node: Parameters<typeof renderToStaticMarkup>[0]): DocumentFragment {
  const tpl = document.createElement('template')
  tpl.innerHTML = renderToStaticMarkup(node)
  return tpl.content
}
const ssr = () => toFrag(createElement(Modules))

const styleOf = (el: Element | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const decl of (el?.getAttribute('style') ?? '').split(/;(?![^(]*\))/)) {
    const i = decl.indexOf(':')
    if (i > 0) out[decl.slice(0, i).trim()] = decl.slice(i + 1).trim()
  }
  return out
}
const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const paths = (el: Element | null | undefined) => [...(el?.querySelectorAll('path') ?? [])].map((p) => p.getAttribute('d'))

const RETIRED_ID = 'modules'

describe('SO-01 #solution exists once and the retired section id is gone', () => {
  it('the page holds one id="solution" and none with the retired id', () => {
    const page = toFrag(createElement(App))
    expect(page.querySelectorAll('[id="solution"]').length).toBe(1)
    expect(page.querySelectorAll(`[id="${RETIRED_ID}"]`).length).toBe(0)
  })

  it('the band is the page <section> between #problem and #platform, and holds the only .mod-grid', () => {
    const page = toFrag(createElement(App))
    const ids = [...page.querySelectorAll('section[id]')].map((s) => s.id)
    expect(ids.length, 'control: the page rendered its sections').toBeGreaterThan(3)
    expect(ids.indexOf('solution')).toBe(ids.indexOf('problem') + 1)
    expect(ids.indexOf('platform')).toBe(ids.indexOf('solution') + 1)
    expect(page.querySelectorAll('.mod-grid').length).toBe(1)
    expect(page.querySelectorAll('#solution .mod-grid > .mod-cell').length).toBe(12)
  })

  it('no element on the page still carries ios-4; the page sections are the control', () => {
    const page = toFrag(createElement(App))
    expect(page.querySelectorAll('section').length, 'control: the page rendered its sections').toBeGreaterThan(10)
    expect(page.querySelectorAll('.ios-4').length).toBe(0)
  })

  it('neither Q4-retired phrase is anywhere on the page', () => {
    const text = norm(toFrag(createElement(App)).textContent)
    expect(text, 'control: the Solution copy is on the page').toContain('compliance solution')
    expect(text).not.toContain('compliance workflow layer')
    expect(text).not.toContain('licensed transmission partners')
  })
})

describe('SO-02 the dark eyebrow and the Q4 H2', () => {
  it('reads THE SOLUTION in the dark eyebrow and a t-h2 whose last child is the t-hl-dark2 span', () => {
    const frag = ssr()
    const eyebrows = frag.querySelectorAll('.t-eyebrow')
    expect(eyebrows.length).toBe(1)
    expect(eyebrows[0].classList.contains('ds-eyebrow--dark')).toBe(true)
    expect(norm(eyebrows[0].textContent)).toBe('THE SOLUTION')

    const h2s = frag.querySelectorAll('h2')
    expect(h2s.length).toBe(1)
    expect(h2s[0].classList.contains('t-h2')).toBe(true)
    expect(norm(h2s[0].textContent)).toBe('ASComply is your invoice compliance solution.')
    const last = h2s[0].lastElementChild
    expect(last?.tagName).toBe('SPAN')
    expect(last?.classList.contains('t-hl-dark2')).toBe(true)
    expect(norm(last?.textContent)).toBe('compliance solution.')
  })

  it('breaks the line after "invoice": the first line is plain text and the span is on the second', () => {
    const h2 = ssr().querySelector('h2')
    const nodes = [...(h2?.childNodes ?? [])]
    const br = nodes.findIndex((n) => (n as Element).tagName === 'BR')
    const span = nodes.findIndex((n) => (n as Element).tagName === 'SPAN')
    expect(br, 'control: the heading has a <br>').toBeGreaterThan(0)
    expect(span).toBeGreaterThan(br)
    expect(norm(nodes.slice(0, br).map((n) => n.textContent).join(''))).toBe('ASComply is your invoice')
    expect(h2?.querySelectorAll('.t-hl-dark2').length).toBe(1)
  })
})

describe('SO-03 the repo body, no retired copy', () => {
  it('holds the V190 and repo paragraphs as t-body, and neither Q4-retired phrase', () => {
    const frag = ssr()
    const ps = [...frag.querySelectorAll('p:not(.mod-body)')]
    expect(ps.length).toBe(2)
    expect(ps.map((p) => norm(p.textContent))).toEqual([V190, REPO_BODY_2])
    ps.forEach((p, i) => {
      expect(p.classList.contains('t-body'), `paragraph ${i} class`).toBe(true)
      expect(styleOf(p).color, `paragraph ${i} colour`).toBe('var(--surface-body)')
    })

    const text = norm(frag.textContent)
    expect(text).toContain('compliance solution')
    expect(text).not.toContain('compliance workflow layer')
    expect(text).not.toContain('licensed transmission partners')
  })
})

describe('SO-04 twelve cells in V865-878 order with V icons', () => {
  it('.mod-grid holds 12 .mod-cell, each with its h3, .mod-body paragraph and glyph, and sets no inline tracks', () => {
    const frag = ssr()
    const grids = frag.querySelectorAll('.mod-grid')
    expect(grids.length).toBe(1)
    expect(grids[0].getAttribute('style') ?? '').not.toContain('grid-template-columns')

    const cells = [...frag.querySelectorAll('.mod-grid > .mod-cell')]
    expect(cells.length).toBe(12)
    cells.forEach((cell, i) => {
      const [title, body, icon] = V865_878[i]
      expect(norm(cell.querySelector('h3')?.textContent), `cell ${i} title`).toBe(title)
      expect(norm(cell.querySelector('p.t-body-sm.mod-body')?.textContent), `cell ${i} body`).toBe(body)
      expect(paths(cell.querySelector('.mod-icon svg')), `cell ${i} glyph`).toEqual([...GLYPHS[icon]])
      expect(cell.querySelector('.mod-icon svg')?.getAttribute('width'), `cell ${i} icon size`).toBe('20')
      expect(cell.querySelector('.mod-icon svg')?.getAttribute('stroke-width'), `cell ${i} stroke`).toBe('2')
      expect(cell.querySelectorAll('h3').length, `cell ${i} h3 count`).toBe(1)
      expect(cell.querySelectorAll('p').length, `cell ${i} p count`).toBe(1)
      expect(cell.querySelectorAll('svg').length, `cell ${i} svg count`).toBe(1)
    })
  })

  it('the 12 cells carry 12 distinct titles and 12 distinct glyphs', () => {
    const cells = [...ssr().querySelectorAll('.mod-grid > .mod-cell')]
    expect(cells.length).toBe(12)
    expect(new Set(cells.map((c) => norm(c.querySelector('h3')?.textContent))).size).toBe(12)
    expect(new Set(cells.map((c) => paths(c.querySelector('svg')).join('|'))).size).toBe(12)
  })
})
