import { createElement, type ReactElement, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Home } from './components/Home'
import { GroupPage } from './components/GroupPage'
import { SceneView } from './components/SceneView'
import { COMING_SOON_IDS, FEATURES, GROUPS } from './content'
import { Icon } from './icons'
import { sceneState, thumbStep } from './scene'

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
    expect(where(ts, (s) => s['text-transform'] === 'uppercase' && s['font-size'] === '10px')).toHaveLength(5)
    const fields = where(ts, (s) => s.height === '38px')
    expect(fields).toHaveLength(5)
    expect(style(fields[0])).toMatchObject({
      border: '1px solid var(--status-red-border)',
      background: 'var(--status-red-bg)',
      'box-shadow': '0 0 0 2px var(--ring)',
    })
    expect(markup('validate', 0, 'player')).toContain(glyph('triangle-alert'))
    const icon = where(ts, (s) => s.animation === 'libFade 300ms')
    expect(icon.map((t) => style(t).color)).toEqual(Array(5).fill(null).map((_, i) => (i === 0 ? 'var(--status-red-text)' : 'var(--status-green-text)')))
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
})
