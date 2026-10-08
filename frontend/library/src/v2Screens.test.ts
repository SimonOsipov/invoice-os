import { createElement, type ReactElement, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Home } from './components/Home'
import { GroupPage } from './components/GroupPage'
import { FeaturePage } from './components/FeaturePage'
import { Player } from './components/Player'
import { TourOverlay } from './components/TourOverlay'
import { SceneView } from './components/SceneView'
import { COMING_SOON_IDS, FEATURES, GROUPS, TOUR } from './content'
import { GLYPHS, Icon } from './icons'
import { START, type Clock } from './player'
import { sceneState, thumbStep } from './scene'
import type { Scene } from './types'
import type { Rect, TourState } from './tour'

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
      expect(attr(card, 'type')).toBe('button')
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

  it('SC-10 a card calls onGroup with its group id and the tour button calls onTour', () => {
    const calls: string[] = []
    type El = ReactElement<{ onClick?: () => void; children?: ReactNode }>
    const walk = (n: ReactNode, out: El[] = []): El[] => {
      if (Array.isArray(n)) n.forEach((c) => walk(c, out))
      else if (n && typeof n === 'object' && 'props' in n) {
        out.push(n as El)
        walk((n as El).props.children, out)
      }
      return out
    }
    const els = walk(Home({ demoHref: null, onGroup: (g) => calls.push(g.id), onTour: () => calls.push('tour') }))
    const cards = els.filter((e) => e.type === 'button')
    expect(cards).toHaveLength(11)
    const icons = els.filter((e) => e.type === Icon).map((e) => (e.props as { name?: string }).name)
    expect(icons).toEqual(GROUPS.map((g) => g.icon))
    cards.forEach((c) => c.props.onClick!())
    expect(calls).toEqual(GROUPS.map((g) => g.id))
    const tour = els.filter((e) => typeof e.type === 'function' && e.props.onClick)
    expect(tour).toHaveLength(1)
    tour[0].props.onClick!()
    expect(calls.at(-1)).toBe('tour')
  })

  it('SC-12 a null onTour removes the hero tour button and its row', () => {
    const render = (onTour: (() => void) | null) => parse(renderToStaticMarkup(createElement(Home, { demoHref: null, onGroup: noop, onTour })))
    const actionsRows = (ts: Tag[]) => ts.filter((t) => t.name === 'div' && style(t).gap === '14px' && style(t)['flex-wrap'] === 'wrap')
    const off = render(null)
    expect(off.filter((t) => t.name === 'button' && t.text === 'Take the tour')).toHaveLength(0)
    expect(actionsRows(off)).toHaveLength(0)
    expect(off.some((t) => t.text.includes('or take the tour'))).toBe(true)
    const on = render(noop)
    expect(on.filter((t) => t.name === 'button' && t.text === 'Take the tour')).toHaveLength(1)
    expect(actionsRows(on)).toHaveLength(1)
  })
})

const groupPage = (gid: string, openHref: string | null = null) => {
  const g = GROUPS.find((x) => x.id === gid)!
  return { g, ts: parse(renderToStaticMarkup(createElement(GroupPage, { group: g, openHref, onFeature: noop }))) }
}
const cardSlices = (ts: Tag[]) => {
  const cards = named(ts, 'button')
  return cards.map((c, k) => ({ card: c, inside: ts.slice(ts.indexOf(c), k + 1 < cards.length ? ts.indexOf(cards[k + 1]) : ts.length) }))
}

describe('library group page', () => {
  it('SC-05 the group header names the group and its number', () => {
    const { g, ts } = groupPage('recognition')
    expect(g.name).toBe('Document recognition')
    const [section] = named(ts, 'section')
    expect(style(section)).toMatchObject({ padding: '56px 40px 80px', 'max-width': '1180px', gap: '40px' })
    expect(style(ts[ts.indexOf(section) + 1])).toMatchObject({ 'max-width': '700px', gap: '18px' })
    eyebrow(ts, 'Group 02 of 11')
    expect(GROUPS).toHaveLength(11)
    const h2 = only(ts, 'h2')
    expect(attr(h2, 'class')).toBe('t-h2')
    expect(style(h2)['font-size']).toBe('44px')
    expect(h2.text).toBe('Document recognition')
    const lead = ts.filter((t) => attr(t, 'class') === 't-lead')
    expect(lead).toHaveLength(1)
    expect(lead[0].name).toBe('p')
    expect(lead[0].text).toBe(esc(g.intro))
  })

  it('SC-06 Open in Platform is an outline arrow link, or absent', () => {
    const href = 'https://app.example/invoices?via=library'
    const ts = groupPage('recognition', href).ts
    const a = only(ts, 'a')
    expect(a.text).toBe('Open in Platform')
    expect(attr(a, 'class')).toBe('ds-btn ds-btn--outline ds-btn--sm')
    expect(attr(a, 'href')).toBe(href)
    expect(ts[ts.indexOf(a) + 1].name).toBe('svg')
    const none = groupPage('recognition', null).ts
    expect(none.some((t) => t.text === 'Open in Platform')).toBe(false)
    expect(named(none, 'a')).toHaveLength(0)
  })

  it('SC-07 one card per feature with window title, title, short and duration', () => {
    GROUPS.forEach((gr) => {
      const { g, ts } = groupPage(gr.id)
      const grid = ts.find((t) => style(t)['grid-template-columns'] !== undefined)!
      expect(style(grid)).toMatchObject({ 'grid-template-columns': 'repeat(auto-fill, minmax(320px, 1fr))', gap: '20px' })
      const cards = cardSlices(ts)
      expect(cards.map((c) => attr(c.card, 'id'))).toEqual(g.feats.map((f) => `fc-${f.id}`))
      cards.forEach(({ card, inside }, k) => {
        const f = g.feats[k]
        expect(attr(card, 'type')).toBe('button')
        expect(style(card)).toMatchObject({
          background: 'var(--card)',
          border: '1px solid var(--border)',
          'border-radius': '6px',
          overflow: 'hidden',
          padding: '0',
        })
        const win = inside.find((t) => t.text === esc(f.sc.win))!
        expect(attr(win, 'class')).toBe('mono')
        expect(style(win)['font-size']).toBe('9px')
        const title = inside.find((t) => t.text === esc(f.title) && style(t)['font-size'] === '18px')!
        expect(title, `title of ${f.id}`).toBeDefined()
        expect(style(title)).toMatchObject({ 'font-size': '18px', 'line-height': '1.3', 'letter-spacing': '-0.01em' })
        const block = inside.find((t) => style(t).padding === '22px 24px 24px')!
        expect(style(block)).toMatchObject({ gap: '8px' })
        expect(inside.indexOf(block)).toBeLessThan(inside.indexOf(title))
        expect(attr(inside.find((t) => t.text === esc(f.short))!, 'class')).toBe('t-body-sm')
        const dur = f.sc.steps.length === 4 ? '0:13' : '0:10'
        expect(f.sc.steps.length === 4 || f.sc.steps.length === 3).toBe(true)
        expect(attr(inside.find((t) => t.text === dur)!, 'class')).toBe('mono')
        f.who.forEach((w) => expect(inside.some((t) => t.text.includes(esc(w)))).toBe(false))
      })
    })
  })

  it('SC-08 the card thumbnail is the window frame with its scene', () => {
    const { ts } = groupPage('recognition')
    const { inside } = cardSlices(ts)[0]
    const frame = inside[1]
    expect(style(frame)).toMatchObject({ height: '192px', background: 'var(--surface)' })
    const win = inside[2]
    expect(style(win)).toMatchObject({
      left: '20px',
      right: '20px',
      top: '16px',
      bottom: '44px',
      background: '#fff',
      'box-shadow': 'var(--shadow-soft)',
    })
    const bar = inside[3]
    expect(style(bar)).toMatchObject({ height: '22px', background: 'var(--muted)' })
    inside.slice(4, 7).forEach((d) => expect(style(d)).toMatchObject({ width: '6px', height: '6px' }))
    const sceneIdx = inside.findIndex((t, i) => i > 6 && style(t).flex === '1 1 0')
    expect(sceneIdx).toBeGreaterThan(0)
    const disc = inside.find((t) => style(t).width === '30px')!
    expect(style(disc).background).toBe('var(--accent)')
    const svg = inside[inside.indexOf(disc) + 1]
    expect([svg.name, attr(svg, 'width'), attr(svg, 'height')]).toEqual(['svg', '13', '13'])
    const chip = inside.find((t) => style(t).background === 'rgba(8,47,49,0.82)')!
    expect(style(chip)).toMatchObject({ right: '20px', bottom: '13px' })

    type El = ReactElement<{ children?: ReactNode; sc?: unknown; idx?: number; size?: string }>
    const walk = (n: ReactNode, out: El[] = []): El[] => {
      if (Array.isArray(n)) n.forEach((c) => walk(c, out))
      else if (n && typeof n === 'object' && 'props' in n) {
        out.push(n as El)
        walk((n as El).props.children, out)
      }
      return out
    }
    GROUPS.forEach((g) => {
      const cards = walk(GroupPage({ group: g, openHref: null, onFeature: noop })).filter((e) => e.type === 'button')
      cards.forEach((c, k) => {
        const views = walk(c.props.children).filter((e) => e.type === SceneView)
        expect(views, g.feats[k].id).toHaveLength(1)
        expect(views[0].props).toEqual({ sc: g.feats[k].sc, idx: thumbStep(g.feats[k]), size: 'thumb' })
      })
    })

    const imp = cardSlices(groupPage('invoices').ts).find((c) => attr(c.card, 'id') === 'fc-import-files')!
    expect(imp.inside.some((t) => t.text === esc('Lagos Freight & Logistics Ltd'))).toBe(true)
    expect(imp.inside.some((t) => t.text === 'Needs fixing')).toBe(true)
  })

  it('SC-09 a Coming soon card carries the pill, a shipped card does not', () => {
    const { ts } = groupPage('clients')
    const byId: Record<string, Tag[]> = Object.fromEntries(cardSlices(ts).map((c) => [attr(c.card, 'id'), c.inside]))
    const pill = byId['fc-contacts'].find((t) => t.text === 'Coming soon')!
    expect(style(pill).background).toBe('var(--status-progress-bg)')
    expect(byId['fc-portfolio'].some((t) => t.text === 'Coming soon')).toBe(false)
    expect(byId['fc-onboard-client'].some((t) => t.text === 'Coming soon')).toBe(false)
    const ids = GROUPS.flatMap((g) =>
      cardSlices(groupPage(g.id).ts)
        .filter((c) => c.inside.some((t) => t.text === 'Coming soon'))
        .map((c) => attr(c.card, 'id')!.slice(3)),
    )
    expect(ids).toHaveLength(11)
    expect([...ids].sort()).toEqual([...COMING_SOON_IDS].sort())
  })

  it('SC-11 a feature card calls onFeature with its feature id', () => {
    const calls: string[] = []
    type El = ReactElement<{ onClick?: () => void; children?: ReactNode }>
    const walk = (n: ReactNode, out: El[] = []): El[] => {
      if (Array.isArray(n)) n.forEach((c) => walk(c, out))
      else if (n && typeof n === 'object' && 'props' in n) {
        out.push(n as El)
        walk((n as El).props.children, out)
      }
      return out
    }
    GROUPS.forEach((g) => {
      calls.length = 0
      const cards = walk(GroupPage({ group: g, openHref: null, onFeature: (f) => calls.push(f.id) })).filter((e) => e.type === 'button')
      expect(cards).toHaveLength(g.feats.length)
      cards.forEach((c) => c.props.onClick!())
      expect(calls).toEqual(g.feats.map((f) => f.id))
      expect(g.feats.every((f) => f.gid === g.id)).toBe(true)
    })
  })
})

const feat = (id: string) => FEATURES.find((f) => f.id === id)!
const markup = (id: string, idx: number, size: 'thumb' | 'player') =>
  renderToStaticMarkup(createElement(SceneView, { sc: feat(id).sc, idx, size }))
const view = (id: string, idx: number, size: 'thumb' | 'player') => parse(markup(id, idx, size))
const where = (ts: Tag[], pred: (s: Record<string, string>, t: Tag) => boolean) => ts.filter((t) => pred(style(t), t))
const glyph = (name: 'pen-tool' | 'triangle-alert' | 'circle-check') => renderToStaticMarkup(createElement(Icon, { name, size: 16 }))

describe('library scenes', () => {
  it('SN-01 the player list shows the banner, the header and the focused row', () => {
    const ts = view('import-files', 1, 'player')
    const banner = where(ts, (s) => s.background === 'var(--mint-soft)' && s.animation === 'libFade 300ms ease-out')
    expect(banner).toHaveLength(1)
    const bi = ts.indexOf(banner[0])
    expect([ts[bi + 1].name, attr(ts[bi + 1], 'width'), attr(ts[bi + 1], 'height')]).toEqual(['svg', '15', '15'])
    expect(withText(ts, 'Mapped 9 of 9 columns').name).toBe('span')
    const head = where(ts, (s) => s['grid-template-columns'] === '0.5fr 2fr 1.2fr 1.2fr' && s['font-size'] === '10px')
    expect(head).toHaveLength(1)
    expect(ts.slice(ts.indexOf(head[0]) + 1, ts.indexOf(head[0]) + 5).map((t) => t.text)).toEqual(['Row', 'Customer', 'Amount', 'Status'])
    const rows = where(ts, (s) => s.animation === 'libPop 320ms ease-out')
    expect(rows.map((r) => style(r).background)).toEqual(['var(--mint-soft)', 'transparent'])
    const chips = ts.filter((t) => t.text === 'Imported')
    expect(chips.map((c) => style(c).background)).toEqual(Array(2).fill('var(--status-muted-bg)'))
    const later = view('import-files', 3, 'player')
    const rows3 = where(later, (s) => s.animation === 'libPop 320ms ease-out')
    expect(rows3).toHaveLength(4)
    expect(style(withText(later, 'Needs fixing')).color).toBe('var(--status-red-text)')
    expect(later.some((t) => t.text === 'Mapped 9 of 9 columns')).toBe(false)
  })

  it('SN-02 the thumbnail list shows customer and chip only', () => {
    const ts = view('import-files', 3, 'thumb')
    const rows = where(ts, (s) => s['grid-template-columns'] === 'minmax(0, 1fr) auto')
    expect(rows).toHaveLength(4)
    expect(where(ts, (s) => s['font-size'] === '10px' && s['font-weight'] === '600')).toHaveLength(4)
    expect(where(ts, (s) => s['font-size'] === '9px' && s['font-weight'] === '700')).toHaveLength(4)
    expect(ts.some((t) => t.text === 'Row')).toBe(false)
    expect(ts.some((t) => t.text === '₦ 4,820,000')).toBe(false)
    expect(markup('import-files', 3, 'thumb')).not.toContain('animation')
    const c = view('contacts', 0, 'thumb')
    expect(where(c, (s) => s['grid-template-columns'] === 'minmax(0, 1fr) auto')).toHaveLength(3)
    const banner = where(c, (s) => s.background === 'var(--status-red-bg)' && s['font-size'] === '9.5px')
    expect(banner).toHaveLength(1)
    expect(banner[0].text).toBe('Synced 3 contacts from Sage')
  })

  it('SN-03 the player form marks fields and shows the message', () => {
    const ts = view('validate', 0, 'player')
    // why: a message caps the player form at four fields
    expect(where(ts, (s) => s['text-transform'] === 'uppercase' && s['font-size'] === '10px')).toHaveLength(4)
    const fields = where(ts, (s) => s.height === '38px')
    expect(fields).toHaveLength(4)
    expect(style(fields[0])).toMatchObject({
      border: '1px solid var(--status-red-border)',
      background: 'var(--status-red-bg)',
      'box-shadow': '0 0 0 2px var(--ring)',
    })
    expect(markup('validate', 0, 'player')).toContain(glyph('triangle-alert'))
    const icon = where(ts, (s) => s.animation === 'libFade 300ms')
    expect(icon.map((t) => style(t).color)).toEqual(Array(4).fill(null).map((_, i) => (i === 0 ? 'var(--status-red-text)' : 'var(--status-green-text)')))
    const msg = ts.find((t) => t.text.startsWith('Seller TIN has 7 digits'))!
    expect(style(msg).background).toBe('var(--status-red-bg)')

    const fix = view('validate', 1, 'player')
    expect(fix.some((t) => t.text === '20184412-0001')).toBe(true)
    expect(style(where(fix, (s) => s.height === '38px')[0]).border).toBe('1px solid var(--ring)')
    expect(markup('validate', 1, 'player')).toContain(glyph('pen-tool'))
    expect(where(fix, (s) => s.animation === 'libPop 300ms ease-out')).toHaveLength(0)

    const ci = view('create-invoice', 0, 'player')
    expect(where(ci, (s) => s.transition === 'opacity 400ms').map((t) => style(t).opacity)).toEqual(['1', '1', '0', '0', '0'])

    const info = view('learns', 1, 'player').find((t) => t.text === esc('Layout saved for Adeyemi & Sons Trading') && style(t).animation !== undefined)!
    expect(style(info)).toMatchObject({ background: 'var(--mint-soft)', color: 'var(--tab-active-text)' })
  })

  it('SN-04 the doc form has its own grid per size', () => {
    const p = view('read-documents', 2, 'player')
    expect(where(p, (s) => s['grid-template-columns'] === '1fr 1.15fr')).toHaveLength(1)
    const panel = where(p, (s) => s.height === '306px')
    expect(panel).toHaveLength(1)
    expect(withText(p, 'INVOICE').name).toBe('span')
    const docRows = where(p, (s) => s.padding === '5px 7px')
    expect(docRows).toHaveLength(5)
    expect(style(docRows[3])).toMatchObject({ background: 'var(--peach-tint)', outline: '1.5px solid var(--accent)' })

    const t = view('read-documents', thumbStep(feat('read-documents')), 'thumb')
    expect(where(t, (s) => s['grid-template-columns'] === '0.85fr 1.15fr')).toHaveLength(1)
    expect(where(t, (s) => s.padding === '2px 4px')).toHaveLength(4)
    expect(where(t, (s) => s['grid-template-columns'] === '1fr')).toHaveLength(1)
    expect(where(t, (s) => s.height === '18px')).toHaveLength(3)

    const ci = view('create-invoice', 2, 'thumb')
    expect(where(ci, (s) => s['grid-template-columns'] === '1fr 1fr')).toHaveLength(1)
    expect(where(ci, (s) => s.height === '18px')).toHaveLength(4)
    expect(ci.some((x) => x.text === '12 fields checked · 0 errors')).toBe(true)
  })

  const formCases = FEATURES.flatMap((f) => {
    const sc = f.sc
    if (sc.kind !== 'form') return []
    return sc.steps.map((_, idx) => ({ id: f.id, idx, doc: !!sc.doc, st: sceneState(sc, idx) as Extract<ReturnType<typeof sceneState>, { kind: 'form' }> }))
  })
  // the focused field always; every marked field whenever they fit in cap fields
  const needed = (st: { fields: { l: string; mark: unknown; focused: boolean }[] }, cap: number) => {
    const idx = st.fields.flatMap((x, i) => (x.focused || x.mark ? [i] : []))
    const fits = idx.length > 0 && Math.max(...idx) - Math.min(...idx) < cap
    return st.fields.filter((x) => x.focused || (fits && x.mark)).map((x) => x.l)
  }
  const fitting = (st: Parameters<typeof needed>[0], cap: number) => needed(st, cap).length > 0

  it('SN-17 the player form caps at four fields under a message and keeps the focused and marked ones', () => {
    const cases = formCases.filter((c) => c.st.msg)
    expect(cases.filter((c) => fitting(c.st, 4)).length).toBeGreaterThan(0)
    for (const c of cases) {
      const shown = where(view(c.id, c.idx, 'player'), (s) => s['text-transform'] === 'uppercase' && s['font-size'] === '10px').map((t) => t.text)
      expect(shown.length, `${c.id} step ${c.idx}`).toBeLessThanOrEqual(4)
      for (const l of needed(c.st, 4)) expect(shown, `${c.id} step ${c.idx}`).toContain(esc(l))
    }
    expect(where(view('validate', 1, 'player'), (s) => s['text-transform'] === 'uppercase' && s['font-size'] === '10px')).toHaveLength(5)
    expect(where(view('read-documents', 2, 'player'), (s) => s.padding === '5px 7px')).toHaveLength(5)
  })

  it('SN-18 a thumbnail form shows at most its cap and keeps the focused and marked fields', () => {
    const cases = formCases.map((c) => ({ ...c, st: sceneState(feat(c.id).sc, thumbStep(feat(c.id))) as typeof c.st })).filter((c, i, all) => all.findIndex((x) => x.id === c.id) === i)
    expect(cases.length).toBeGreaterThan(0)
    expect(cases.some((c) => c.doc && c.st.msg)).toBe(true)
    for (const c of cases) {
      const cap = c.doc ? (c.st.msg ? 2 : 3) : 4
      const ts = view(c.id, thumbStep(feat(c.id)), 'thumb')
      const shown = where(ts, (s) => s['text-transform'] === 'uppercase' && s['font-size'] === '8px').map((t) => t.text)
      expect(shown.length, c.id).toBeLessThanOrEqual(cap)
      for (const l of needed(c.st, cap)) expect(shown, c.id).toContain(esc(l))
    }
    const boxes = (id: string) => where(view(id, thumbStep(feat(id)), 'thumb'), (s) => s.height === '18px')
    expect(boxes('learns')).toHaveLength(2)
    expect(boxes('create-invoice')).toHaveLength(4)
    expect(boxes('read-documents')).toHaveLength(3)
  })

  it('SN-05 the flow marks done, active and todo nodes', () => {
    const ts = view('submit-clear', 1, 'player')
    const nodes = where(ts, (s) => s.width === '40px' && s.height === '40px')
    expect(nodes.map((n) => n.text)).toEqual(['✓', '✓', '3', '4', '5'])
    expect(style(nodes[2]).background).toBe('var(--accent)')
    expect(style(nodes[0]).background).toBe('var(--primary)')
    const lines = where(ts, (s) => s.height === '2px' && s.position === 'absolute')
    expect(lines.map((l) => style(l).display)).toEqual(['none', 'block', 'block', 'block', 'block'])
    expect(withText(ts, 'STATUS').attrs).toContain('t-step')
    expect(ts.some((t) => t.text === 'Submitted · waiting for a response')).toBe(true)
    expect(ts.some((t) => t.text === 'Sent to FIRS')).toBe(true)

    const th = view('submit-clear', 1, 'thumb')
    expect(where(th, (s) => s.width === '22px' && s.height === '22px')).toHaveLength(5)
    expect(th.some((t) => t.text === 'Sent to FIRS')).toBe(false)
    expect(th.some((t) => t.text === 'Submitted · waiting for a response')).toBe(true)
  })

  it('SN-06 metrics grow tiles and bars per size', () => {
    const ts = view('overview', 0, 'player')
    expect(where(ts, (s) => s['font-size'] === '30px').map((t) => t.text)).toEqual(['193', '38%', '5'])
    const bars = where(ts, (s) => (s.transition ?? '').startsWith('height 700ms'))
    const hs = bars.map((b) => style(b).height)
    expect(hs).toHaveLength(7)
    expect([hs[0], hs.at(-1)]).toEqual(['21px', '57px'])
    expect(bars.map((b) => style(b).background)).toEqual([...Array(6).fill('var(--sage-panel)'), 'var(--primary)'])

    const th = view('overview', 0, 'thumb')
    const tb = where(th, (s) => s.flex === '1' && s['border-radius'] === '2px 2px 0 0')
    expect(tb.map((b) => style(b).height)).toEqual(['6px', '9px', '8px', '12px', '13px', '15px', '17px'])
    expect(where(th, (s) => s['font-size'] === '15px').map((t) => t.text)).toEqual(['193', '38%', '5'])
  })

  it('SN-07 toggles show the track and knob per size', () => {
    const ts = view('roles', 1, 'player')
    const rows = where(ts, (s) => s.padding === '13px 14px')
    expect(rows).toHaveLength(5)
    const tracks = where(ts, (s) => s.width === '38px' && s.height === '22px')
    const knobs = where(ts, (s) => s.width === '16px' && s.height === '16px')
    expect(style(tracks[0]).background).toBe('var(--primary)')
    expect(style(knobs[0]).transform).toBe('translateX(16px)')
    expect(style(tracks[3]).background).toBe('var(--input)')
    expect(style(knobs[3]).transform).toBe('translateX(0px)')
    expect(style(rows[3]).background).toBe('var(--mint-soft)')
    expect(style(rows[0]).background).toBe('transparent')

    const th = view('roles', 1, 'thumb')
    expect(where(th, (s) => s.padding === '6px 0')).toHaveLength(4)
    expect(where(th, (s) => s.transform === 'translateX(12px)').length).toBeGreaterThan(0)
    expect(th.some((t) => t.text === 'Read access')).toBe(false)
  })

  it('SN-08 the feed shows the first items per size', () => {
    const ts = view('audit-trail', 0, 'player')
    const items = where(ts, (s) => s['grid-template-columns'] === '62px 12px 1fr')
    expect(items).toHaveLength(2)
    expect(items.every((i) => style(i).animation === 'libPop 340ms ease-out')).toBe(true)
    expect(ts.some((t) => t.text === '14:02')).toBe(true)
    const dots = where(ts, (s) => s.width === '10px' && s.height === '10px')
    expect(dots.map((d) => style(d).background)).toEqual(['var(--status-green-text)', 'var(--accent)'])
    expect(ts.some((t) => t.text === 'Finance officer')).toBe(true)

    const th = view('audit-trail', 2, 'thumb')
    expect(where(th, (s) => s['grid-template-columns'] === '34px 8px minmax(0, 1fr)')).toHaveLength(4)
    expect(th.some((t) => t.text === 'Finance officer')).toBe(false)
  })

  it('SN-09 every feature renders at every step in both sizes', () => {
    const kinds = new Set<string>()
    FEATURES.forEach((f) => {
      f.sc.steps.forEach((_, i) => {
        kinds.add(sceneState(f.sc, i).kind)
        ;(['thumb', 'player'] as const).forEach((size) => {
          expect(markup(f.id, i, size).length, `${f.id} ${i} ${size}`).toBeGreaterThan(0)
        })
      })
    })
    expect(FEATURES).toHaveLength(26)
    expect([...kinds].sort()).toEqual(['feed', 'flow', 'form', 'list', 'metrics', 'toggles'])
  })

  const LONG = 'Extraordinarily Long Customer Name '.repeat(8).trim()
  const listSc = (rows: number): Scene => ({
    kind: 'list', win: 'w', cols: ['A', 'B', 'C', 'D'], grid: '1fr 1fr 1fr 1fr',
    rows: Array.from({ length: rows }, (_, i) => [`r${i}`, `${LONG} ${i}`, `${i}`, 'new'] as [string, string, string, 'new']),
    steps: [{ cap: 'plain' }, { cap: 'with banner', banner: 'Banner text' }],
  })
  const thumbRows = (ts: Tag[]) => where(ts, (s) => s['grid-template-columns'] === 'minmax(0, 1fr) auto')
  const draw = (sc: Scene, idx: number, size: 'thumb' | 'player') => parse(renderToStaticMarkup(createElement(SceneView, { sc, idx, size })))

  it('SN-10 the thumbnail list keeps three rows beside a banner and four without, and clips long text', () => {
    const plain = draw(listSc(6), 0, 'thumb')
    expect(thumbRows(plain)).toHaveLength(4)
    expect(plain.some((t) => t.text === 'Banner text')).toBe(false)
    const banner = draw(listSc(6), 1, 'thumb')
    expect(thumbRows(banner)).toHaveLength(3)
    expect(banner.filter((t) => t.text === 'Banner text')).toHaveLength(1)
    const cust = where(banner, (s) => s['font-size'] === '10px' && s['font-weight'] === '600')
    expect(cust).toHaveLength(3)
    expect(cust.every((t) => style(t)['text-overflow'] === 'ellipsis' && style(t)['white-space'] === 'nowrap')).toBe(true)
    expect(cust[0].text).toContain(LONG)
    expect(thumbRows(draw(listSc(2), 1, 'thumb'))).toHaveLength(2)
    const player = draw(listSc(6), 0, 'player')
    expect(where(player, (s) => s.animation === 'libPop 320ms ease-out')).toHaveLength(6)
    expect(where(player, (s) => s.animation === 'libFade 300ms ease-out')).toHaveLength(0)
  })

  const formSc = (doc: boolean): Scene => ({
    kind: 'form', win: 'w', ...(doc ? { doc: true as const } : {}),
    fields: ['A', 'B', 'C', 'D', 'E'].map((l) => ({ l, v: `${l}-value` })),
    steps: [
      { cap: 's0', reveal: 5, focus: 0, mark: { 0: 'ok', 1: 'err', 2: 'low', 3: 'fix' } },
      { cap: 's1', reveal: 5, focus: -1, msg: 'M', tone: 'ok' },
      { cap: 's2', msg: 'M', tone: 'err' },
      { cap: 's3', msg: 'M' },
    ],
  })
  const MARKS = [
    ['var(--status-green-border)', 'var(--status-green-bg)'],
    ['var(--status-red-border)', 'var(--status-red-bg)'],
    ['var(--status-amber-border)', 'var(--status-amber-bg)'],
    ['var(--ring)', '#fff'],
    ['var(--input)', '#fff'],
  ]

  it('SN-11 the thumbnail form colours each mark, slices fields per layout and colours the message by tone', () => {
    const boxes = (ts: Tag[]) => where(ts, (s) => s.height === '18px')
    const flat = draw(formSc(false), 0, 'thumb')
    expect(boxes(flat)).toHaveLength(4)
    boxes(flat).forEach((b, i) => expect([style(b).border, style(b).background]).toEqual([`1px solid ${MARKS[i][0]}`, MARKS[i][1]]))
    const doc = draw(formSc(true), 0, 'thumb')
    expect(boxes(doc)).toHaveLength(3)
    expect(where(doc, (s) => s.padding === '2px 4px')).toHaveLength(4)
    const msg = (step: number) => where(draw(formSc(false), step, 'thumb'), (s) => s['font-size'] === '9.5px')
    expect(msg(0)).toHaveLength(0)
    expect(style(msg(1)[0])).toMatchObject({ background: 'var(--status-green-bg)', color: 'var(--status-green-text)', border: '1px solid var(--status-green-border)' })
    expect(style(msg(2)[0])).toMatchObject({ background: 'var(--status-red-bg)', color: 'var(--status-red-text)', border: '1px solid var(--status-red-border)' })
    expect(style(msg(3)[0])).toMatchObject({ background: 'var(--mint-soft)', color: 'var(--tab-active-text)', border: '1px solid var(--border)' })
  })

  it('SN-12 the doc panel outlines the focused field solid, a low field dashed and the rest clear, in both sizes', () => {
    const expected = ['1.5px solid var(--accent)', '1.5px solid transparent', '1.5px dashed var(--status-amber-text)', '1.5px solid transparent']
    const rows = (ts: Tag[], pad: string) => where(ts, (s) => s.padding === pad)
    const t = rows(draw(formSc(true), 0, 'thumb'), '2px 4px')
    const p = rows(draw(formSc(true), 0, 'player'), '5px 7px')
    expect(t).toHaveLength(4)
    expect(p).toHaveLength(5)
    expect(t.map((r) => style(r).outline)).toEqual(expected)
    expect(p.slice(0, 4).map((r) => style(r).outline)).toEqual(expected)
    expect(p.map((r) => style(r).background)).toEqual(['var(--peach-tint)', 'transparent', 'transparent', 'transparent', 'transparent'])
    const hidden = { ...(formSc(true) as Extract<Scene, { kind: 'form' }>), steps: [{ cap: 'c', reveal: 2, mark: { 2: 'low' as const } }] }
    expect(rows(draw(hidden, 0, 'player'), '5px 7px').map((r) => style(r).outline)).toEqual(Array(5).fill('1.5px solid transparent'))
  })

  it('SN-13 the player form draws each mark glyph and colour and the thumbnail drops them', () => {
    const player = draw(formSc(false), 0, 'player')
    const boxes = where(player, (s) => s.height === '38px')
    expect(boxes).toHaveLength(5)
    boxes.forEach((b, i) => expect([style(b).border, style(b).background]).toEqual([`1px solid ${MARKS[i][0]}`, MARKS[i][1]]))
    expect(where(player, (s) => s['box-shadow'] === '0 0 0 2px var(--ring)')).toHaveLength(1)
    const icons = where(player, (s) => s.animation === 'libFade 300ms')
    expect(icons.map((t) => style(t).color)).toEqual(['var(--status-green-text)', 'var(--status-red-text)', 'var(--status-amber-text)', 'var(--teal)'])
    const html = renderToStaticMarkup(createElement(SceneView, { sc: formSc(false), idx: 0, size: 'player' }))
    expect(html).toContain(glyph('circle-check'))
    expect(html).toContain(glyph('pen-tool'))
    expect(html.match(new RegExp(glyph('triangle-alert').replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'g'))).toHaveLength(2)
    const msgP = (step: number) => where(draw(formSc(false), step, 'player'), (s) => s.animation === 'libPop 300ms ease-out')
    expect(msgP(0)).toHaveLength(0)
    expect(style(msgP(1)[0]).background).toBe('var(--status-green-bg)')
    expect(style(msgP(2)[0]).color).toBe('var(--status-red-text)')
    expect(style(msgP(3)[0]).background).toBe('var(--mint-soft)')
  })

  it('SN-14 flow todo nodes are white with muted text and a clear connector; the thumbnail has no transitions', () => {
    const f = feat('submit-clear')
    const ts = draw(f.sc, 0, 'player')
    const nodes = where(ts, (s) => s.width === '40px' && s.height === '40px')
    expect(nodes).toHaveLength(5)
    expect(style(nodes[0]).background).toBe('var(--primary)')
    expect(style(nodes[1])).toMatchObject({ background: 'var(--accent)', color: 'var(--accent-foreground)', border: '2px solid var(--accent)' })
    expect(style(nodes[4])).toMatchObject({ background: '#fff', color: 'var(--muted-foreground)', border: '2px solid var(--input)' })
    const lines = where(ts, (s) => s.height === '2px' && s.position === 'absolute')
    expect(lines.map((l) => style(l).background)).toEqual(['var(--primary)', 'var(--primary)', 'var(--border)', 'var(--border)', 'var(--border)'])
    const labels = where(ts, (s) => s['font-size'] === '13px' && s['font-weight'] === '700' && s['line-height'] === '1.3')
    expect(labels.map((l) => style(l).color)).toEqual(['var(--ink)', 'var(--ink)', ...Array(3).fill('var(--muted-foreground)')])
    const html = renderToStaticMarkup(createElement(SceneView, { sc: f.sc, idx: 1, size: 'thumb' }))
    expect(html).not.toContain('transition')
    expect(html).not.toContain('animation')
  })

  it('SN-15 the metrics thumbnail floors a bar at 3px and the player floors it at 4px', () => {
    const sc: Scene = { kind: 'metrics', win: 'w', tiles: [['A', 10, '']], bars: [0.5, 1], steps: [{ cap: 'c', grow: 0 }, { cap: 'c', grow: 1 }] }
    const th = (i: number) => where(draw(sc, i, 'thumb'), (s) => s.flex === '1' && s['border-radius'] === '2px 2px 0 0').map((b) => style(b).height)
    expect(th(0)).toEqual(['3px', '3px'])
    expect(th(1)).toEqual(['23px', '45px'])
    const pl = (i: number) => where(draw(sc, i, 'player'), (s) => (s.transition ?? '').startsWith('height 700ms')).map((b) => style(b).height)
    expect(pl(0)).toEqual(['4px', '4px'])
    expect(pl(1)).toEqual(['75px', '150px'])
  })

  it('SN-16 absent optional fields and empty collections render in both sizes', () => {
    const scenes: Scene[] = [
      listSc(0),
      formSc(false),
      { kind: 'flow', win: 'w', nodes: [['One', 's']], steps: [{ cap: 'c', at: 0, out: '' }] },
      { kind: 'metrics', win: 'w', tiles: [], bars: [], steps: [{ cap: 'c', grow: 1 }] },
      { kind: 'toggles', win: 'w', rows: [['T', 'd', false]], steps: [{ cap: 'c', focus: -1 }] },
      { kind: 'feed', win: 'w', items: [], steps: [{ cap: 'c', show: 0 }] },
    ]
    expect(scenes.map((s) => s.kind).sort()).toEqual(['feed', 'flow', 'form', 'list', 'metrics', 'toggles'])
    scenes.forEach((sc) =>
      (['thumb', 'player'] as const).forEach((size) => {
        const html = renderToStaticMarkup(createElement(SceneView, { sc, idx: 0, size }))
        expect(html.length, `${sc.kind} ${size}`).toBeGreaterThan(0)
      }),
    )
    const t = draw(scenes[4], 0, 'thumb')
    expect(where(t, (s) => s.transform === 'translateX(0px)')).toHaveLength(1)
    const toggled = draw({ ...(scenes[4] as Extract<Scene, { kind: 'toggles' }>), rows: [['T', 'd', true]] }, 0, 'thumb')
    expect(where(toggled, (s) => s.transform === 'translateX(12px)')).toHaveLength(1)
    expect(where(toggled, (s) => s.background === 'var(--primary)' && s.width === '26px')).toHaveLength(1)
  })
})

const FEATURE = (id: string) => FEATURES.find((f) => f.id === id)!
const playerEl = (id: string, clock: Clock, onToggle = noop, onSeek: (f: number) => void = noop) =>
  Player({ feature: FEATURE(id), clock, onToggle, onSeek }) as ReactElement
const playerView = (id: string, clock: Clock) => parse(renderToStaticMarkup(playerEl(id, clock)))
const walk = (n: ReactNode, out: ReactElement<Record<string, unknown>>[] = []) => {
  if (Array.isArray(n)) n.forEach((c) => walk(c, out))
  else if (n && typeof n === 'object' && 'props' in n) {
    out.push(n as ReactElement<Record<string, unknown>>)
    walk((n as ReactElement<{ children?: ReactNode }>).props.children, out)
  }
  return out
}
const inButton = (ts: Tag[]) => ts.slice(ts.indexOf(only(ts, 'button')) + 1).filter((t) => t.name === 'svg')
const PAUSED: Clock = { tenths: 34, playing: false, ended: false }

describe('library player', () => {
  it('PL-01 the player frame, window and scene box', () => {
    const ts = playerView('import-files', START)
    expect(style(ts[0])).toMatchObject({ 'border-radius': '10px', background: 'var(--surface)', 'box-shadow': 'var(--shadow-elegant)' })
    expect(style(ts[1])).toMatchObject({ padding: '28px 28px 0' })
    expect(style(ts[2])).toMatchObject({ 'border-radius': '8px', background: 'var(--card)' })
    const bar = where(ts, (s) => s.height === '36px')
    expect(bar).toHaveLength(1)
    const dots = where(ts, (s) => s.width === '9px' && s.height === '9px')
    expect(dots).toHaveLength(3)
    dots.forEach((d) => expect(style(d).background).toBe('var(--input)'))
    const win = withText(ts, 'Import · sahara-foods-june.csv')
    expect(attr(win, 'class')).toBe('mono')
    expect(style(win)['font-size']).toBe('11px')
    const box = where(ts, (s) => s.height === '350px')
    expect(box).toHaveLength(1)
    expect(style(box[0])).toMatchObject({ padding: '20px 22px', overflow: 'hidden' })
  })

  it('PL-02 the caption shows the step number and caption under the window', () => {
    const ts = playerView('import-files', PAUSED)
    const num = withText(ts, '02 / 04')
    expect(attr(num, 'class')).toBe('mono')
    expect(style(num)).toMatchObject({ 'font-size': '12px', color: 'var(--accent)' })
    const cap = withText(ts, 'Columns are matched to invoice fields')
    expect(style(cap)).toMatchObject({ 'font-size': '16px', 'font-weight': '600', color: 'var(--surface-foreground)' })
    const row = where(ts, (s) => s['min-height'] === '66px')
    expect(row).toHaveLength(1)
    expect(ts.indexOf(row[0])).toBeGreaterThan(ts.indexOf(where(ts, (s) => s.height === '350px')[0]))
  })

  it('PL-03 the controls show state, time, progress and ticks', () => {
    const playing = playerView('import-files', { tenths: 34, playing: true, ended: false })
    const btn = only(playing, 'button')
    expect(attr(btn, 'aria-label')).toBe('Play or pause')
    expect(style(btn)).toMatchObject({ width: '40px', height: '40px', background: 'var(--accent)' })
    expect(where(playing, (s) => s.width === '4px' && s.height === '14px')).toHaveLength(2)
    expect(inButton(playing)).toHaveLength(0)
    const time = withText(playing, '0:03 / 0:13')
    expect(style(time)).toMatchObject({ width: '82px', color: 'var(--surface-body)' })
    expect(where(playing, (s) => s.background === 'var(--accent)' && s.transition === 'width 100ms linear').map((t) => style(t).width)).toEqual(['25%'])
    const ticks = where(playing, (s) => s.width === '2px' && s.background === 'var(--surface)')
    expect(ticks.map((t) => style(t).left)).toEqual(['25%', '50%', '75%'])

    const paused = playerView('import-files', PAUSED)
    expect(inButton(paused).map((t) => attr(t, 'width'))).toEqual(['16'])
    expect(renderToStaticMarkup(playerEl('import-files', PAUSED))).toContain(renderToStaticMarkup(createElement(Icon, { name: 'play', size: 16 })))

    const ended = { tenths: 136, playing: false, ended: true }
    const done = playerView('import-files', ended)
    expect(inButton(done).map((t) => attr(t, 'width'))).toEqual(['16'])
    expect(renderToStaticMarkup(playerEl('import-files', ended))).toContain(renderToStaticMarkup(createElement(Icon, { name: 'rotate-cw', size: 16 })))
    expect(where(done, (s) => s.transition === 'width 100ms linear').map((t) => style(t).width)).toEqual(['100%'])
    const over = playerView('import-files', { ...ended, tenths: 200 })
    expect(where(over, (s) => s.transition === 'width 100ms linear').map((t) => style(t).width)).toEqual(['100%'])

    const three = playerView('create-invoice', START)
    expect(where(three, (s) => s.width === '2px' && s.background === 'var(--surface)')).toHaveLength(2)
    expect(withText(three, '0:00 / 0:10')).toBeDefined()

    expect(withText(done, '0:13 / 0:13')).toBeDefined()
    expect(FEATURES.length).toBeGreaterThan(0)
    for (const f of FEATURES) {
      const n = f.sc.steps.length
      const lefts = where(playerView(f.id, START), (s) => s.width === '2px' && s.background === 'var(--surface)').map((t) => style(t).left)
      expect(lefts).toEqual(Array.from({ length: n - 1 }, (_, i) => `${((i + 1) / n) * 100}%`))
    }
  })

  it('PL-04 the play button toggles and the scrub seeks to the click fraction', () => {
    let toggled = 0
    let sought: number | null = null
    const els = walk(playerEl('import-files', START, () => { toggled++ }, (f) => { sought = f }))
    const play = els.filter((e) => e.type === 'button')
    expect(play).toHaveLength(1)
    ;(play[0].props.onClick as () => void)()
    expect(toggled).toBe(1)
    const scrub = els.filter((e) => e.props.id === 'lib-scrub')
    expect(scrub).toHaveLength(1)
    ;(scrub[0].props.onClick as (e: unknown) => void)({ currentTarget: { getBoundingClientRect: () => ({ left: 100, width: 400 }) }, clientX: 300 })
    expect(sought).toBe(0.5)
    const click = (clientX: number) => (scrub[0].props.onClick as (e: unknown) => void)({ currentTarget: { getBoundingClientRect: () => ({ left: 100, width: 400 }) }, clientX })
    click(500)
    expect(sought).toBe(1)
    click(100)
    expect(sought).toBe(0)
    expect(toggled).toBe(1)
  })
})

const featurePage = (id: string, openHref: string | null = null) => {
  const f = FEATURE(id)
  const g = GROUPS.find((x) => x.id === f.gid)!
  return { f, g, ts: parse(renderToStaticMarkup(createElement(FeaturePage, { group: g, feature: f, openHref, onGroup: noop, onFeature: noop }))) }
}
// Tags of one panel: from its eyebrow to the next eyebrow (or the end).
const panelOf = (ts: Tag[], label: string) => {
  const start = ts.indexOf(eyebrow(ts, label))
  const next = ts.findIndex((t, i) => i > start && attr(t, 'class') === 't-eyebrow')
  return ts.slice(start, next < 0 ? ts.length : next)
}

describe('library feature page', () => {
  it('FP-01 the header shows the group, the title and the description', () => {
    const { f, g, ts } = featurePage('validate')
    expect(g.name).toBe('Rules & validation')
    const [section] = named(ts, 'section')
    expect(style(section)).toMatchObject({ padding: '40px 40px 88px', 'max-width': '1080px', gap: '36px' })
    expect(style(ts[ts.indexOf(section) + 1])).toMatchObject({ 'max-width': '720px', gap: '18px' })
    const back = named(ts, 'button')[0]
    expect(attr(back, 'class')).toBe('lib-back')
    expect(style(back)).toMatchObject({ color: 'var(--link)', 'font-size': '13px' })
    const svg = ts[ts.indexOf(back) + 1]
    expect([svg.name, attr(svg, 'width'), attr(svg, 'height')]).toEqual(['svg', '15', '15'])
    expect(renderToStaticMarkup(createElement(FeaturePage, { group: g, feature: f, openHref: null, onGroup: noop, onFeature: noop }))).toContain(
      renderToStaticMarkup(createElement(Icon, { name: 'chevron-left', size: 15 })),
    )
    const label = ts.slice(ts.indexOf(svg)).find((t) => t.name === 'span')!
    expect([label.name, label.text]).toEqual(['span', esc(g.name)])
    const h2 = only(ts, 'h2')
    expect(attr(h2, 'class')).toBe('t-h2')
    expect(style(h2)['font-size']).toBe('44px')
    expect(h2.text).toBe(f.title)
    const lead = ts.filter((t) => attr(t, 'class') === 't-lead')
    expect(lead).toHaveLength(1)
    expect(lead[0].text).toBe(esc(f.desc))
  })

  it('FP-02 Open in Platform is a primary arrow link for a shipped feature, absent for Coming soon', () => {
    const href = 'https://a.example/invoices?via=library'
    const ts = featurePage('validate', href).ts
    const a = only(ts, 'a')
    expect(a.text).toBe('Open in Platform')
    expect(attr(a, 'class')).toBe('ds-btn ds-btn--primary ds-btn--sm')
    expect(attr(a, 'href')).toBe(href)
    expect(ts[ts.indexOf(a) + 1].name).toBe('svg')
    expect(ts.some((t) => t.text === 'Coming soon')).toBe(false)

    const soon = featurePage('alerts', null).ts
    expect(FEATURE('alerts').status).toBe('soon')
    const pill = withText(soon, 'Coming soon')
    expect(style(pill).background).toBe('var(--status-progress-bg)')
    expect(soon.indexOf(pill)).toBeLessThan(soon.indexOf(only(soon, 'h2')))
    expect(named(soon, 'a')).toHaveLength(0)
  })

  it('FP-03 What you get lists the benefits', () => {
    const { f, ts } = featurePage('validate')
    const p = panelOf(ts, 'What you get')
    const rows = p.filter((t) => attr(t, 'class') === 't-body')
    expect(f.benefits).toHaveLength(3)
    expect(rows.map((r) => r.text)).toEqual(f.benefits.map(esc))
    const icons = where(p, (s) => s.color === 'var(--teal)')
    expect(icons).toHaveLength(3)
    icons.forEach((i) => expect(attr(p[p.indexOf(i) + 1], 'width')).toBe('18'))
    rows.forEach((r) => expect(style(r).color).toBe('var(--foreground)'))
    const grid = where(ts, (s) => s['grid-template-columns'] === 'repeat(auto-fit, minmax(260px, 1fr))')
    expect(grid).toHaveLength(1)
    expect(style(grid[0]).gap).toBe('40px')
  })

  it('FP-04 In this demo lists the captions and marks the current step', () => {
    const { f, ts } = featurePage('import-files')
    const p = panelOf(ts, 'In this demo')
    const btns = named(p, 'button')
    expect(btns).toHaveLength(f.sc.steps.length)
    const steps = btns.map((b) => p.slice(p.indexOf(b), p.indexOf(b) + 3))
    expect(steps.map((x) => x[1].text)).toEqual(['01', '02', '03', '04'])
    expect(f.sc.steps).toHaveLength(4)
    expect(steps.map((x) => x[2].text)).toEqual(f.sc.steps.map((s) => esc(s.cap)))
    expect(attr(btns[0], 'aria-current')).toBe('step')
    expect(style(steps[0][2])['font-weight']).toBe('700')
    expect(style(btns[0]).color).toBe('var(--ink)')
    expect(style(steps[0][1]).color).toBe('var(--teal)')
    for (const k of [1, 2, 3]) {
      expect(attr(btns[k], 'aria-current')).toBeUndefined()
      expect(style(steps[k][2])['font-weight']).toBe('500')
      expect(style(btns[k]).color).toBe('var(--text-copy)')
      expect(style(steps[k][1]).color).toBe('var(--step-label)')
    }
  })

  it('FP-05 Related lists each rel feature with its group', () => {
    const { f, ts } = featurePage('import-files')
    const p = panelOf(ts, 'Related')
    const cards = named(p, 'button')
    expect(cards.map((c) => attr(c, 'class'))).toEqual(['lib-card', 'lib-card', 'lib-card'])
    const want = f.rel.map((id) => {
      const r = FEATURE(id)
      return [GROUPS.find((g) => g.id === r.gid)!.name, r.title]
    })
    const got = cards.map((c) => {
      const [g, t] = p.slice(p.indexOf(c) + 1, p.indexOf(c) + 3)
      expect(attr(g, 'class')).toBe('t-step')
      expect(style(t)['font-size']).toBe('15px')
      return [g.text, t.text]
    })
    expect(got).toEqual(want.map(([g, t]) => [esc(g), esc(t)]))
    expect(got[0]).toEqual(['Document recognition', 'Read PDFs and scans'])
    expect(named(panelOf(featurePage('roles').ts, 'Related'), 'button')).toHaveLength(2)
  })

  it('FP-06 a feature with no rel and no benefits renders empty panels, and an unknown rel id is skipped', () => {
    const f = FEATURE('import-files')
    const g = GROUPS.find((x) => x.id === f.gid)!
    const render = (feature: typeof f) =>
      parse(renderToStaticMarkup(createElement(FeaturePage, { group: g, feature, openHref: null, onGroup: noop, onFeature: noop })))
    const bare = render({ ...f, rel: [], benefits: [] })
    for (const label of ['What you get', 'In this demo', 'Related']) expect(eyebrow(bare, label)).toBeDefined()
    expect(named(panelOf(bare, 'In this demo'), 'button')).toHaveLength(f.sc.steps.length)
    expect(named(panelOf(bare, 'Related'), 'button')).toHaveLength(0)
    expect(panelOf(bare, 'What you get').filter((t) => attr(t, 'class') === 't-body')).toHaveLength(0)
    const skip = render({ ...f, rel: ['no-such-feature', 'roles'] })
    const cards = named(panelOf(skip, 'Related'), 'button')
    expect(cards).toHaveLength(1)
    expect(panelOf(skip, 'Related').some((t) => t.text === 'Roles and permissions')).toBe(true)
  })
})

describe('library tour', () => {
  const MENU: TourState = { i: 0, phase: 'menu' }
  const CARD: TourState = { i: 0, phase: 'card' }
  const R: Rect = { x: 6, y: 97, w: 208, h: 42 }
  const WIN = { w: 1440, h: 900 }
  const overlay = (tour: TourState, rect: Rect | null = R, win = WIN) =>
    parse(renderToStaticMarkup(createElement(TourOverlay, { tour, rect, win, onBack: noop, onNext: noop, onWatch: noop, onClose: noop })))
  const EASE = '380ms var(--ease-out)'

  it('TR-01 the overlay is a full-window layer over a click blocker', () => {
    const ts = overlay(MENU)
    expect(style(ts[0])).toEqual({ position: 'absolute', inset: '0', 'z-index': '60' })
    expect(style(ts[1])).toEqual({ position: 'absolute', inset: '0' })
    expect(ts[1].text).toBe('')
    expect(ts[2].name).toBe('div')
    expect(style(ts[2]).left).toBeDefined()
  })

  it('TR-02 the spotlight sits on the rectangle with a dimming ring', () => {
    const spot = style(overlay(MENU)[2])
    expect(spot).toMatchObject({ left: '6px', top: '97px', width: '208px', height: '42px', 'border-radius': '10px', 'pointer-events': 'none' })
    expect(spot['box-shadow']).toBe('0 0 0 9999px rgba(8,47,49,0.66), 0 0 0 2px var(--accent)')
    expect(spot.transition).toBe(`left ${EASE}, top ${EASE}, width ${EASE}, height ${EASE}`)
    const centre = style(overlay(MENU, null)[2])
    expect(centre).toMatchObject({ left: '720px', top: '450px' })
    expect(Number.parseFloat(centre.width)).toBe(0)
    expect(Number.parseFloat(centre.height)).toBe(0)
  })

  it('TR-03 the callout card carries the prototype box', () => {
    expect(style(overlay(CARD)[3])).toMatchObject({
      width: '360px',
      background: 'var(--card)',
      'border-radius': '8px',
      padding: '22px 24px 20px',
      'box-shadow': 'var(--shadow-elegant)',
      display: 'flex',
      'flex-direction': 'column',
      gap: '12px',
      transition: `left ${EASE}, top ${EASE}, bottom ${EASE}`,
    })
  })

  it('TR-04 the callout is placed with left and top, or left and bottom', () => {
    const menu = style(overlay(MENU)[3])
    expect(menu).toMatchObject({ left: '232px', top: '85px' })
    expect(menu).not.toHaveProperty('bottom')
    const card = style(overlay(CARD, { x: 320, y: 340, w: 356, h: 316 }, { w: 1440, h: 700 })[3])
    expect(card).toMatchObject({ left: '320px', bottom: '376px' })
    expect(card).not.toHaveProperty('top')
  })

  it('TR-05 the callout shows the step, the title, the text and a close button', () => {
    const ts = overlay(CARD)
    const step = ts.filter((t) => attr(t, 'class') === 't-step')
    expect(step.map((t) => t.text)).toEqual(['STEP 02 OF 14'])
    const title = withText(ts, TOUR[0].t)
    expect(style(title)).toMatchObject({ 'font-size': '19px', 'font-weight': '700', 'letter-spacing': '-0.02em', 'line-height': '1.25' })
    const body = withText(ts, TOUR[0].d)
    expect(attr(body, 'class')).toBe('t-body-sm')
    expect(style(body).color).toBe('var(--foreground)')
    const x = ts.filter((t) => t.name === 'button' && attr(t, 'class') === 'lib-tour-x')
    expect(x).toHaveLength(1)
    expect(attr(x[0], 'aria-label')).toBe('Close tour')
    expect(style(x[0])).toMatchObject({ color: 'var(--muted-foreground)', padding: '2px' })
    const svg = after(ts, x[0], 'svg')
    expect([attr(svg, 'width'), attr(svg, 'height')]).toEqual(['16', '16'])
    expect(after(ts, svg, 'path') && attr(after(ts, svg, 'path'), 'd')).toBe(GLYPHS.x[0])
  })

  it('TR-06 Watch demo shows on card steps only', () => {
    const ts = overlay(CARD)
    const watch = withText(ts, 'Watch demo')
    expect(watch.name).toBe('button')
    expect(style(watch)).toMatchObject({
      color: 'var(--link)',
      'border-bottom': '1px solid var(--link)',
      'font-size': '13px',
      'font-weight': '700',
      padding: '0',
    })
    expect(overlay(MENU).some((t) => t.text === 'Watch demo')).toBe(false)
    const row = ts[ts.indexOf(watch) - 1]
    expect(style(row)).toEqual({ display: 'flex', 'align-items': 'center', 'justify-content': 'space-between', gap: '10px', 'margin-top': '6px' })
  })

  it('TR-07 Back is the outline button and is disabled on step 1; Next reads Finish on the last step', () => {
    const ts = overlay(MENU)
    const back = withText(ts, 'Back')
    const next = withText(ts, 'Next')
    expect(attr(back, 'class')).toBe('ds-btn ds-btn--outline ds-btn--sm')
    expect(back.attrs).toMatch(/\sdisabled(=|\s|$)/)
    expect(attr(next, 'class')).toBe('ds-btn ds-btn--primary ds-btn--sm')
    expect(next.attrs).not.toContain('disabled')
    const wrap = ts[ts.indexOf(back) - 1]
    expect(style(wrap)).toEqual({ display: 'flex', gap: '8px', 'margin-left': 'auto' })
    expect(ts[ts.indexOf(back) + 1]).toBe(next)
    expect(withText(overlay(CARD), 'Back').attrs).not.toContain('disabled')
    const last = overlay({ i: 6, phase: 'card' })
    expect(withText(last, 'Finish')).toBeDefined()
    expect(last.some((t) => t.text === 'Next')).toBe(false)
  })

  it('TR-08 each control calls its handler', () => {
    const calls: string[] = []
    type El = ReactElement<{ onClick?: () => void; children?: ReactNode }>
    const walk = (n: ReactNode, out: El[] = []): El[] => {
      if (Array.isArray(n)) n.forEach((c) => walk(c, out))
      else if (n && typeof n === 'object' && 'props' in n) {
        out.push(n as El)
        walk((n as El).props.children, out)
      }
      return out
    }
    const els = walk(
      TourOverlay({
        tour: { i: 0, phase: 'card' },
        rect: R,
        win: WIN,
        onBack: () => calls.push('back'),
        onNext: () => calls.push('next'),
        onWatch: () => calls.push('watch'),
        onClose: () => calls.push('close'),
      }),
    ).filter((e) => e.props.onClick)
    const label = (e: El) => (e.type === 'button' && (e.props as { className?: string }).className === 'lib-tour-x' ? 'Close' : e.props.children)
    expect(els.map(label)).toEqual(['Close', 'Watch demo', 'Back', 'Next'])
    for (const e of els) {
      const before = calls.length
      e.props.onClick!()
      expect(calls).toHaveLength(before + 1)
    }
    expect(calls).toEqual(['close', 'watch', 'back', 'next'])
  })
})
