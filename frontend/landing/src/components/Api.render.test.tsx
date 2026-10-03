// @vitest-environment jsdom
// Api band, SSR markup (jsdom drops shorthands). V399-409 and V412-426 are retyped below.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { GLYPHS, type GlyphName } from '../icons'
import { Api } from './Api'

const V_EYEBROW = 'API & INTEGRATIONS'
const V_H2 = 'Compliance as an API. Drop it into any ERP.'
const V_HL = 'Drop it into any ERP.'
const V401 =
  'REST endpoints, signed webhooks, OAuth2 and a sandbox MBS/NRS adapter. Send invoice data in, get a validated, submission-ready document back, with a full audit trail.'
const V403_406: readonly (readonly [GlyphName, string])[] = [
  ['link', 'REST API: create, validate, fetch status, fetch documents'],
  ['activity', 'Signed webhooks on status change and submission events'],
  ['key-round', 'OAuth2 and scoped API keys, with per-tenant isolation'],
  ['globe', 'Sandbox MBS/NRS adapter. Production on accreditation.'],
]
const V_HEADER = 'POST /v1/invoices/validate'
const V412_426 = [
  '# Validate an invoice against Nigeria MBS rules',
  'curl https://api.ascomply.africa/v1/invoices/validate \\',
  '  -H "Authorization: Bearer sk_live_..." \\',
  `  -d '{ "buyer_tin": "12345678-0001",`,
  '        "currency": "NGN",',
  '        "vat_rate": 7.5,',
  `        "lines": [...] }'`,
  '',
  '# 200 OK',
  '{',
  '  "status": "validated",',
  '  "ready_to_submit": true,',
  '  "errors": [],',
  '  "nrs_reference": "pending"',
  '}',
].join('\n')
// V412-426 span colours with their text, in order.
const V_SPANS = [
  ['var(--surface-body)', '# Validate an invoice against Nigeria MBS rules'],
  ['var(--eyebrow-on-dark)', '"Authorization: Bearer sk_live_..."'],
  ['var(--eyebrow-on-dark)', `'{ "buyer_tin": "12345678-0001",\n        "currency": "NGN",\n        "vat_rate": 7.5,\n        "lines": [...] }'`],
  ['var(--surface-body)', '# 200 OK'],
  ['var(--eyebrow-on-dark)', '"status"'],
  ['var(--accent)', '"validated"'],
  ['var(--eyebrow-on-dark)', '"ready_to_submit"'],
  ['var(--accent)', 'true'],
  ['var(--eyebrow-on-dark)', '"errors"'],
  ['var(--eyebrow-on-dark)', '"nrs_reference"'],
  ['var(--accent)', '"pending"'],
] as const

function ssr(markup?: string): DocumentFragment {
  const tpl = document.createElement('template')
  tpl.innerHTML = markup ?? renderToStaticMarkup(createElement(Api, { onBookDemo: () => undefined }))
  return tpl.content
}

const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const paths = (el: Element | null | undefined) => [...(el?.querySelectorAll('path') ?? [])].map((p) => p.getAttribute('d'))
const style = (el: Element, prop: string) => new RegExp(`(?:^|;)\\s*${prop}:\\s*([^;]+)`, 'i').exec(el.getAttribute('style') ?? '')?.[1].trim()
const DOT_COLOURS = ['#ff5f57', '#febc2e', '#28c840']
const isDot = (el: Element) =>
  /^(99|999)(px)?$|^var\(--radius-pill\)$/.test(style(el, 'border-radius') ?? '') ||
  DOT_COLOURS.includes((style(el, 'background') ?? style(el, 'background-color') ?? '').toLowerCase())

describe('AP-01 the band is #api on the dark band', () => {
  it('is section.ds-section.band-dark with the dark eyebrow, the h2 with its dark-2 span, and V401', () => {
    const frag = ssr()
    const sections = frag.querySelectorAll('#api')
    expect(sections, 'one #api').toHaveLength(1)
    expect(sections[0].localName).toBe('section')
    expect([...sections[0].classList]).toEqual(['ds-section', 'band-dark'])

    const eyebrows = sections[0].querySelectorAll('.t-eyebrow')
    expect(eyebrows, 'one eyebrow').toHaveLength(1)
    expect(eyebrows[0].classList.contains('ds-eyebrow--dark')).toBe(true)
    expect(norm(eyebrows[0].textContent)).toBe(V_EYEBROW)

    const h2s = sections[0].querySelectorAll('h2')
    expect(h2s, 'one h2').toHaveLength(1)
    const hl = h2s[0].querySelectorAll('span.t-hl-dark2')
    expect(hl, 'one highlight span').toHaveLength(1)
    expect(norm(hl[0].textContent)).toBe(V_HL)
    h2s[0].querySelectorAll('br').forEach((br) => br.replaceWith(' '))
    expect(norm(h2s[0].textContent)).toBe(V_H2)

    const body = sections[0].querySelectorAll('p.t-body')
    expect(body, 'one paragraph').toHaveLength(1)
    expect(norm(body[0].textContent)).toBe(V401)
  })
})

describe('AP-02 four bullets with V’s glyphs', () => {
  it('draws link, activity, key-round, globe in order, with V403-406’s text', () => {
    const rows = [...ssr().querySelectorAll('#api svg')].filter((s) => !s.closest('[data-api-panel]')).map((s) => s.parentElement!)
    expect(rows, 'four bullet rows').toHaveLength(V403_406.length)
    expect(rows.map((r) => paths(r)), 'glyph paths, in order').toEqual(V403_406.map(([g]) => [...GLYPHS[g]]))
    expect(rows.map((r) => norm(r.querySelector('span')?.textContent)), 'bullet texts, in order').toEqual(V403_406.map(([, t]) => t))
  })
})

describe('AP-03 the sample is V’s text with no dots', () => {
  it('has the V header, the V pre text exactly, and no window-dot element', () => {
    const panel = ssr().querySelector('[data-api-panel]')
    expect(panel, 'control: the panel rendered').not.toBeNull()
    expect(norm(panel!.querySelector('.t-meta')?.textContent)).toBe(V_HEADER)
    const pre = panel!.querySelectorAll('pre')
    expect(pre, 'one pre').toHaveLength(1)
    expect(pre[0].textContent).toBe(V412_426)
    for (const needle of ['api.ascomply.africa', '"ready_to_submit"', '"nrs_reference": "pending"']) {
      expect(pre[0].textContent).toContain(needle)
    }

    const all = [...panel!.querySelectorAll('*')]
    expect(all.length, 'population: header, pre and its spans').toBeGreaterThan(10)
    expect(all.filter(isDot), 'a window-dot element').toEqual([])
  })

  it('control: the dot test reports a planted traffic-light span and an old radius', () => {
    const planted = ssr('<div data-api-panel><span style="width:10px;border-radius:99px;background:#FF5F57"></span><i style="background:#FEBC2E"></i><b style="border-radius:var(--radius-pill)"></b></div>')
    expect([...planted.querySelectorAll('[data-api-panel] *')].filter(isDot)).toHaveLength(3)
  })
})

describe('AP-04 sample colours follow V', () => {
  it('colours the pre’s spans: 2 body, 6 eyebrow-on-dark, 3 accent, with V’s text', () => {
    const spans = [...ssr().querySelectorAll('[data-api-panel] pre span')]
    expect(spans.map((s) => [style(s, 'color'), s.textContent])).toEqual(V_SPANS.map((s) => [...s]))
    const count = (c: string) => spans.filter((s) => style(s, 'color') === c).length
    expect([count('var(--surface-body)'), count('var(--eyebrow-on-dark)'), count('var(--accent)')]).toEqual([2, 6, 3])
  })
})

describe('AP-05 one accent CTA', () => {
  it('has one button, Request API access, ds-btn--accent', () => {
    const buttons = ssr().querySelectorAll('#api button')
    expect(buttons, 'one button in the band').toHaveLength(1)
    expect(norm(buttons[0].textContent)).toBe('Request API access')
    expect(buttons[0].classList.contains('ds-btn--accent')).toBe(true)
  })
})
