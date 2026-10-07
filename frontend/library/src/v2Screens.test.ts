import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Home } from './components/Home'
import { GROUPS } from './content'

type Tag = { name: string; attrs: string; text: string }
const attr = (t: { attrs: string }, name: string) => t.attrs.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1]
const style = (t: { attrs: string }): Record<string, string> =>
  Object.fromEntries(
    (attr(t, 'style') ?? '')
      .split(';')
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)]),
  )
const parse = (html: string): Tag[] =>
  [...html.matchAll(/<([a-z][a-z0-9]*)\b([^>]*)>([^<]*)/g)].map((m) => ({ name: m[1], attrs: ` ${m[2]}`, text: m[3].trim() }))
const esc = (t: string) => t.replace(/&/g, '&amp;')
const noop = () => {}
const home = (demoHref: string | null) => parse(renderToStaticMarkup(createElement(Home, { demoHref, onGroup: noop, onTour: noop })))
const named = (ts: Tag[], name: string) => ts.filter((t) => t.name === name)
const only = (ts: Tag[], name: string) => {
  const hit = named(ts, name)
  expect(hit, `exactly one <${name}>`).toHaveLength(1)
  return hit[0]
}
const withText = (ts: Tag[], text: string) => {
  const hit = ts.filter((t) => t.text === text)
  expect(hit, `exactly one tag with text "${text}"`).toHaveLength(1)
  return hit[0]
}
const eyebrow = (ts: Tag[], text: string) => {
  const hit = ts.filter((t) => attr(t, 'class') === 't-eyebrow' && t.text === text)
  expect(hit, `eyebrow "${text}"`).toHaveLength(1)
  return hit[0]
}
const after = (ts: Tag[], t: Tag, name: string) => ts.slice(ts.indexOf(t) + 1).find((x) => x.name === name)!

describe('library home', () => {
  it('SC-01 the hero carries the grid, the eyebrow, the headline, the lead and the tour button', () => {
    const ts = home(null)
    const [hero] = named(ts, 'section')
    expect(style(hero)).toMatchObject({ padding: '72px 40px 80px', overflow: 'hidden' })
    const grid = ts[ts.indexOf(hero) + 1]
    expect(attr(grid, 'class')).toBe('hero-grid hero-grid-fade')
    expect(style(grid)).toMatchObject({ inset: '0', opacity: '0.7' })
    const col = ts[ts.indexOf(grid) + 1]
    expect(style(col)).toMatchObject({ 'max-width': '880px', gap: '24px' })
    eyebrow(ts, 'Feature library')
    const h1 = only(ts, 'h1')
    expect(style(h1)).toMatchObject({
      'font-size': 'clamp(42px, 5vw, 68px)',
      'line-height': '1.07',
      'letter-spacing': '-0.05em',
      'font-weight': '700',
      color: 'var(--ink)',
    })
    expect(h1.text).toBe('Every step from invoice')
    const i = ts.indexOf(h1)
    expect(ts[i + 1].name).toBe('br')
    expect(ts[i + 2].name).toBe('span')
    expect(ts[i + 2].text).toBe('to clearance.')
    expect(style(ts[i + 2]).color).toBe('var(--teal)')
    const lead = ts.filter((t) => attr(t, 'class') === 't-lead')
    expect(lead).toHaveLength(1)
    expect(lead[0].name).toBe('p')
    expect(lead[0].text).toBe(
      'Short screen recordings and plain explanations of what ASComply does to an invoice, from import to FIRS clearance. Pick a group or take the tour.',
    )
    expect(style(lead[0])['max-width']).toBe('600px')
    const tour = withText(ts, 'Take the tour')
    expect(tour.name).toBe('button')
    expect(attr(tour, 'class')).toBe('ds-btn ds-btn--primary ds-btn--md')
    expect(after(ts, tour, 'svg')).toBeDefined()
    expect(ts.indexOf(after(ts, tour, 'svg'))).toBe(ts.indexOf(tour) + 1)
  })

  it('SC-02 eleven group cards in order with number, name, one-liner and count', () => {
    const ts = home(null)
    const [, sage] = named(ts, 'section')
    expect(style(sage)).toMatchObject({ background: 'var(--sage)', padding: '80px 40px' })
    expect(style(ts[ts.indexOf(sage) + 1])).toMatchObject({ 'max-width': '1120px', gap: '40px' })
    eyebrow(ts, 'The library')
    const h2 = withText(ts, 'Eleven groups, one invoice workflow.')
    expect(h2.name).toBe('h2')
    expect(style(h2)['font-size']).toBe('40px')
    expect(withText(ts, 'Each group has two or three demos recorded on the sample companies in the platform, with the steps captioned.').name).toBe('p')
    const grid = ts.find((t) => style(t)['grid-template-columns'] !== undefined)!
    expect(style(grid)).toMatchObject({ 'grid-template-columns': 'repeat(auto-fill, minmax(300px, 1fr))', gap: '16px' })

    const cards = named(ts, 'button').filter((t) => t.text !== 'Take the tour')
    expect(cards).toHaveLength(11)
    expect(GROUPS).toHaveLength(11)
    cards.forEach((card, k) => {
      const g = GROUPS[k]
      expect(style(card)).toMatchObject({
        background: 'var(--sage-card)',
        border: '1px solid var(--sage-card-border)',
        'border-radius': '6px',
        padding: '24px',
        gap: '14px',
      })
      const next = k + 1 < cards.length ? ts.indexOf(cards[k + 1]) : ts.length
      const inside = ts.slice(ts.indexOf(card), next)
      const tile = inside[2]
      expect(style(tile)).toMatchObject({
        width: '40px',
        height: '40px',
        'border-radius': '6px',
        background: 'var(--sage-panel)',
        color: 'var(--primary)',
      })
      const svg = inside[3]
      expect([svg.name, attr(svg, 'width'), attr(svg, 'height')]).toEqual(['svg', '20', '20'])
      const step = inside.find((t) => attr(t, 'class') === 't-step')!
      expect(step.name).toBe('span')
      expect(step.text).toBe(g.n)
      const name = inside.find((t) => t.text === esc(g.name))!
      expect(style(name)).toMatchObject({ 'font-size': '18px', 'font-weight': '700' })
      const one = inside.find((t) => t.text === esc(g.one))!
      expect(attr(one, 'class')).toBe('t-body-sm')
      expect(style(one).color).toBe('var(--text-copy)')
      const count = inside.find((t) => t.text === `${g.feats.length} demos`)!
      expect(attr(count, 'class')).toBe('mono')
      expect(style(count)).toMatchObject({
        'font-size': '11px',
        'font-weight': '700',
        'letter-spacing': '0.06em',
        color: 'var(--tab-active-text)',
        'text-transform': 'uppercase',
      })
    })
    expect(cards[0] && ts.slice(ts.indexOf(cards[0])).filter((t) => t.text === '3 demos').length).toBeGreaterThan(0)
    const first = ts.slice(ts.indexOf(cards[0]), ts.indexOf(cards[1]))
    expect(first.some((t) => t.text === '01')).toBe(true)
    expect(first.some((t) => t.text === 'Invoices')).toBe(true)

    const [, , band] = named(ts, 'section')
    expect(style(band).padding).toBe('72px 40px')
    expect(style(ts[ts.indexOf(band) + 1])['max-width']).toBe('1120px')
    expect(style(withText(ts, 'See it on your own invoices.'))['font-size']).toBe('38px')
  })

  it('SC-03 the peach band offers Book the Demo', () => {
    const ts = home('https://l.example/?demo')
    const band = named(ts, 'section')[2]
    expect(style(band).background).toBe('var(--peach-band)')
    eyebrow(ts, 'Book a demo')
    const h2 = withText(ts, 'See it on your own invoices.')
    expect(h2.name).toBe('h2')
    const span = ts[ts.indexOf(h2) + 2]
    expect(attr(span, 'class')).toBe('t-hl-peach')
    expect(span.text).toBe('Start with a demo.')
    const a = only(ts, 'a')
    expect(a.text).toBe('Book the Demo')
    expect(attr(a, 'class')).toBe('ds-btn ds-btn--primary ds-btn--lg')
    expect(attr(a, 'href')).toBe('https://l.example/?demo')
    expect(ts[ts.indexOf(a) + 1].name).toBe('svg')
  })

  it('SC-04 the band keeps its copy and drops the button when the landing URL is unset', () => {
    const ts = home(null)
    expect(withText(ts, 'Start with a demo.')).toBeDefined()
    expect(ts.some((t) => t.text === 'Book the Demo')).toBe(false)
    expect(named(ts, 'a')).toHaveLength(0)
  })
})
