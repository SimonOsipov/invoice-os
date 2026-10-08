import { createElement, type ComponentProps } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Button } from './components/Button'
import { ComingSoonPill } from './components/ComingSoonPill'
import { Logo } from './components/Logo'
import { JourneyStepper } from './components/JourneyStepper'
import { Sidebar } from './components/Sidebar'
import { GROUPS, STAGES } from './content'
import { GLYPHS, Icon } from './icons'
import { parseLibraryPath, type Route } from './route'

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
const only = (ts: Tag[], name: string) => {
  const hit = ts.filter((t) => t.name === name)
  expect(hit, `exactly one <${name}>`).toHaveLength(1)
  return hit[0]
}
const withText = (ts: Tag[], text: string) => {
  const hit = ts.filter((t) => t.text === text)
  expect(hit, `exactly one tag with text "${text}"`).toHaveLength(1)
  return hit[0]
}
const btn = (props: Omit<ComponentProps<typeof Button>, 'children'>, label: string) =>
  createElement(Button, { ...props, children: label })
const render = (el: Parameters<typeof renderToStaticMarkup>[0]) => parse(renderToStaticMarkup(el))

describe('library shell primitives', () => {
  it('SH-01 Icon draws a 24-grid stroke glyph at stroke 2', () => {
    const ts = render(createElement(Icon, { name: 'bell', size: 17 }))
    const svg = only(ts, 'svg')
    expect(['width', 'height', 'viewBox', 'fill', 'stroke', 'stroke-width', 'aria-hidden'].map((a) => attr(svg, a))).toEqual([
      '17', '17', '0 0 24 24', 'none', 'currentColor', '2', 'true',
    ])
    const paths = ts.filter((t) => t.name === 'path')
    expect(paths.map((p) => attr(p, 'd'))).toEqual([
      'M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9',
      'M10.3 21a1.94 1.94 0 0 0 3.4 0',
    ])
    expect(render(createElement(Icon, { name: 'pen-tool' })).filter((t) => t.name === 'path')).toHaveLength(4)
    expect(GLYPHS['pen-tool']).toHaveLength(4)
    expect(GLYPHS['rotate-cw']).toEqual(['M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8', 'M21 3v5h-5'])
    expect(GLYPHS['pen-tool'].slice(0, 3)).toEqual([
      'M15.707 21.293a1 1 0 0 1-1.414 0l-1.586-1.586a1 1 0 0 1 0-1.414l5.586-5.586a1 1 0 0 1 1.414 0l1.586 1.586a1 1 0 0 1 0 1.414z',
      'm18 13-1.375-6.874a1 1 0 0 0-.746-.776L3.235 2.028a1 1 0 0 0-1.207 1.207L5.35 15.879a1 1 0 0 0 .776.746L13 18',
      'm2.3 2.3 7.286 7.286',
    ])
    expect(GLYPHS['pen-tool'][3]).toBe('M9 11a2 2 0 1 0 4 0a2 2 0 1 0 -4 0')
  })

  it('SH-02 Button renders each variant and size the prototype uses', () => {
    const cls = (props: Omit<ComponentProps<typeof Button>, 'children'>) =>
      attr(only(render(btn(props, 'Go')), 'button'), 'class')
    expect(cls({})).toBe('ds-btn ds-btn--primary ds-btn--md')
    expect(cls({ variant: 'primary', size: 'lg' })).toBe('ds-btn ds-btn--primary ds-btn--lg')
    expect(cls({ variant: 'outline', size: 'sm' })).toBe('ds-btn ds-btn--outline ds-btn--sm')
    expect(cls({ variant: 'outlineDark', size: 'sm' })).toBe('ds-btn ds-btn--outlineDark ds-btn--sm')
  })

  it('SH-02b Button passes disabled to the button only', () => {
    const off = only(render(btn({ disabled: true }, 'Go')), 'button')
    expect(off.attrs).toMatch(/\sdisabled(=|\s|$)/)
    expect(attr(off, 'type')).toBe('button')
    expect(only(render(btn({}, 'Go')), 'button').attrs).not.toContain('disabled')
    const link = only(render(btn({ href: 'https://l.example/', disabled: true }, 'Go')), 'a')
    expect(link.attrs).not.toContain('disabled')
  })

  it('SH-03 a Button with href is a link, without href a button', () => {
    const link = render(btn({ href: 'https://l.example/?demo' }, 'Book'))
    const a = only(link, 'a')
    expect(attr(a, 'href')).toBe('https://l.example/?demo')
    expect(attr(a, 'type')).toBeUndefined()
    expect(link.some((t) => t.name === 'button')).toBe(false)

    const plain = render(btn({ style: { width: '100%' } }, 'Book'))
    expect(attr(only(plain, 'button'), 'type')).toBe('button')
    expect(plain.some((t) => t.name === 'a')).toBe(false)
    expect(style(only(plain, 'button')).width).toBe('100%')
  })

  it('SH-04 arrow puts arrow-right after the label', () => {
    const html = renderToStaticMarkup(btn({ arrow: true }, 'Take the tour'))
    expect(html.indexOf('Take the tour')).toBeGreaterThan(-1)
    expect(html.indexOf('Take the tour')).toBeLessThan(html.indexOf('<svg'))
    const ts = parse(html)
    const svg = only(ts, 'svg')
    expect([attr(svg, 'width'), attr(svg, 'height')]).toEqual(['16', '16'])
    expect(ts.filter((t) => t.name === 'path').map((p) => attr(p, 'd'))).toEqual(['M5 12h14', 'm12 5 7 7-7 7'])
    expect(renderToStaticMarkup(btn({}, 'Take the tour'))).not.toContain('<svg')
  })

  it('SH-05 Logo is the DS dark lockup at 28', () => {
    const ts = render(createElement(Logo))
    const img = only(ts, 'img')
    expect(['width', 'height', 'alt', 'aria-hidden'].map((a) => attr(img, a))).toEqual(['28', '28', '', 'true'])
    expect(style(img)['border-radius']).toBe('var(--radius-md)')
    expect(style(withText(ts, 'ASComply'))).toMatchObject({
      'font-size': '16px',
      color: 'var(--surface-foreground)',
      'font-family': 'var(--font-display)',
    })
    expect(style(withText(ts, 'AFRICA'))).toMatchObject({
      'font-size': '8px',
      color: 'var(--eyebrow-on-dark)',
      'text-transform': 'uppercase',
    })
  })

  it('SH-06 the Coming soon pill has the DS Badge shape in both tones', () => {
    const shape = {
      height: '26px',
      padding: '0 10px',
      'border-radius': 'var(--radius-sm)',
      'font-size': 'var(--fs-xs)',
      'white-space': 'nowrap',
    }
    const light = style(withText(render(createElement(ComingSoonPill, { tone: 'light' })), 'Coming soon'))
    expect(light).toMatchObject({
      ...shape,
      background: 'var(--status-progress-bg)',
      color: 'var(--status-progress-fg)',
      border: '1px solid var(--status-progress-border)',
    })
    const dark = style(withText(render(createElement(ComingSoonPill, { tone: 'dark' })), 'Coming soon'))
    expect(dark).toMatchObject({
      ...shape,
      background: 'var(--on-dark-10)',
      color: 'var(--surface-foreground)',
      border: '1px solid var(--on-dark-20)',
    })
  })

  const noop = () => {}
  const sidebar = (route: Route, demoHref: string | null = null) =>
    render(createElement(Sidebar, { route, demoHref, onHome: noop, onGroup: noop, onFeature: noop, onTour: noop, onCookieChoices: noop }))
  const stepper = (route: Route) => render(createElement(JourneyStepper, { route, onGroup: noop }))
  const home: Route = { view: 'home' }
  const esc = (t: string) => t.replace(/&/g, '&amp;')
  const byId = (ts: Tag[], id: string) => {
    const hit = ts.filter((t) => attr(t, 'id') === id)
    expect(hit, `exactly one #${id}`).toHaveLength(1)
    return hit[0]
  }
  const after = (ts: Tag[], t: Tag, name: string, n = 1) => ts.slice(ts.indexOf(t) + 1).filter((x) => x.name === name)[n - 1]
  // The feature button holding `title`, for a shipped (text in button) or Coming soon (text in span) row.
  const featBtn = (ts: Tag[], title: string) => {
    const t = withText(ts, esc(title))
    return t.name === 'button' ? t : ts.slice(0, ts.indexOf(t)).reverse().find((x) => x.name === 'button')!
  }
  const buttons = (ts: Tag[]) => ts.filter((t) => t.name === 'button')

  it('SH-07 the sidebar head holds the logo, the LIBRARY tag and the tour button', () => {
    const ts = sidebar(home, 'https://l.example/?demo')
    const aside = only(ts, 'aside')
    expect(attr(aside, 'class')).toBe('asc-dark')
    expect(style(aside)).toMatchObject({ width: '288px', background: 'var(--surface)' })
    expect(style(withText(ts, 'LIBRARY'))).toMatchObject({ 'font-size': '9px', border: '1px solid var(--action)' })
    const tour = withText(ts, 'Take the tour')
    expect(tour.name).toBe('span')
    const tourBtn = ts.slice(0, ts.indexOf(tour)).reverse().find((t) => t.name === 'button')!
    expect(style(tourBtn)).toMatchObject({ height: '42px', background: 'var(--accent)', color: 'var(--accent-foreground)' })
    const svg = after(ts, tourBtn, 'svg')
    expect([attr(svg, 'width'), attr(svg, 'height')]).toEqual(['15', '15'])
    expect(ts.filter((t) => t.text === 'Take the tour')).toHaveLength(1)
    const head = ts[ts.indexOf(aside) + 1]
    expect(style(head)).toMatchObject({
      padding: '18px 16px 16px',
      gap: '16px',
      'border-bottom': '1px solid var(--line-1)',
    })
    const foot = ts.filter((t) => t.name === 'div' && style(t)['border-top'] === '1px solid var(--line-1)')
    expect(foot).toHaveLength(1)
    expect(style(foot[0]).padding).toBe('14px 16px 16px')
  })

  it('SH-08 the nav lists the 11 groups in order with their counts', () => {
    const ts = sidebar(home)
    expect(style(withText(ts, 'FEATURE GROUPS'))).toMatchObject({
      padding: '16px 10px 8px',
      'font-size': '10px',
      'letter-spacing': '0.1em',
      color: 'var(--fg-4)',
    })
    expect(style(only(ts, 'nav'))).toMatchObject({ padding: '12px 10px', gap: '2px', 'overflow-y': 'auto' })
    const ids = ts.filter((t) => attr(t, 'id')?.startsWith('nav-')).map((t) => attr(t, 'id'))
    expect(ids).toEqual(GROUPS.map((g) => `nav-${g.id}`))
    expect(GROUPS.map((g) => g.feats.length)).toEqual([3, 3, 3, 2, 2, 2, 2, 3, 2, 2, 2])
    const rowStyle = { padding: '9px 10px', 'border-radius': '6px', 'font-size': '13.5px', gap: '11px' }
    const overview = ts.slice(0, ts.indexOf(withText(ts, 'Overview'))).reverse().find((t) => t.name === 'button')!
    expect(style(overview)).toMatchObject({ ...rowStyle, background: 'var(--surface-panel)' })
    expect(attr(after(ts, overview, 'svg'), 'width')).toBe('17')
    for (const g of GROUPS) {
      const b = byId(ts, `nav-${g.id}`)
      expect(style(b), g.id).toMatchObject(rowStyle)
      expect(attr(after(ts, b, 'svg'), 'width')).toBe('17')
      expect(after(ts, b, 'span').text).toBe(esc(g.name))
      const count = after(ts, b, 'span', 2)
      expect(count.text).toBe(String(g.feats.length))
      expect(attr(count, 'class')).toBe('mono')
      expect(style(count)).toMatchObject({ 'font-size': '11px', color: 'var(--fg-4)' })
    }
    for (const g of GROUPS) for (const f of g.feats) expect(ts.some((t) => t.text === esc(f.title)), f.title).toBe(false)
  })

  it("SH-09 the route's group is active and expanded, the others are not", () => {
    const g = GROUPS.find((x) => x.id === 'recognition')!
    const ts = sidebar(parseLibraryPath('/recognition'))
    expect(style(byId(ts, 'nav-recognition'))).toMatchObject({
      background: 'var(--surface-panel)',
      color: 'var(--fg-1)',
      'font-weight': '700',
    })
    expect(style(byId(ts, 'nav-invoices'))).toMatchObject({
      background: 'transparent',
      color: 'var(--fg-3)',
      'font-weight': '600',
    })
    const list = ts.filter((t) => t.name === 'div' && style(t)['border-left'] === '1px solid var(--line-2)')
    expect(list).toHaveLength(1)
    expect(style(list[0])).toMatchObject({ margin: '2px 0 6px 20px', 'padding-left': '12px', gap: '1px' })
    for (const f of g.feats) {
      const b = featBtn(ts, f.title)
      expect(style(b), f.title).toMatchObject({
        padding: '6px 9px',
        'font-size': '12.5px',
        'line-height': '1.35',
        'border-radius': '5px',
      })
    }
    for (const other of GROUPS.filter((x) => x.id !== 'recognition'))
      for (const f of other.feats) expect(ts.some((t) => t.text === esc(f.title)), f.title).toBe(false)
    const overview = ts.slice(0, ts.indexOf(withText(ts, 'Overview'))).reverse().find((t) => t.name === 'button')!
    expect(style(overview).background).toBe('transparent')
  })

  it("SH-10 the route's feature is marked inside its group", () => {
    const ts = sidebar(parseLibraryPath('/recognition/review-fields'))
    expect(style(featBtn(ts, 'Review low-confidence fields'))).toMatchObject({
      background: 'var(--on-dark-10)',
      color: 'var(--accent)',
      'font-weight': '700',
    })
    expect(style(featBtn(ts, 'Read PDFs and scans'))).toMatchObject({
      background: 'transparent',
      color: 'var(--fg-3)',
      'font-weight': '500',
    })
    expect(style(byId(ts, 'nav-recognition'))).toMatchObject({ background: 'var(--surface-panel)', 'font-weight': '700' })
  })

  it('SH-09b aria-current marks only the active group and feature in the sidebar', () => {
    const ts = sidebar(parseLibraryPath('/recognition/review-fields'))
    const current = ts.filter((t) => attr(t, 'aria-current') !== undefined)
    expect(current.map((t) => [attr(t, 'aria-current'), attr(t, 'id') ?? t.text])).toEqual([
      ['true', 'nav-recognition'],
      ['true', 'Review low-confidence fields'],
    ])
    const onHome = sidebar(home).filter((t) => attr(t, 'aria-current') !== undefined)
    expect(onHome.map((t) => [attr(t, 'aria-current'), t.text])).toEqual([['true', '']])
    expect(onHome[0].attrs).toContain('lib-nav')
  })

  it('SH-11 a Coming soon feature carries the pill in the sidebar', () => {
    const pills = (path: string) => sidebar(parseLibraryPath(path)).filter((t) => t.text === 'Coming soon')
    const notif = sidebar(parseLibraryPath('/notifications'))
    const soon = GROUPS.find((g) => g.id === 'notifications')!.feats
    expect(soon.every((f) => f.status === 'soon')).toBe(true)
    expect(notif.filter((t) => t.text === 'Coming soon')).toHaveLength(2)
    expect(pills('/invoices')).toHaveLength(0)

    const rec = sidebar(parseLibraryPath('/recognition'))
    expect(rec.filter((t) => t.text === 'Coming soon')).toHaveLength(1)
    const title = withText(rec, 'Corrections teach the reader')
    const pill = rec[rec.indexOf(title) + 1]
    expect(pill.text).toBe('Coming soon')
    expect(style(pill)).toMatchObject({ background: 'var(--on-dark-10)', flex: 'none' })
    expect(style(title)).toMatchObject({ flex: '1 1 auto', 'min-width': '0' })
    expect(style(featBtn(rec, 'Corrections teach the reader'))).toMatchObject({
      display: 'flex',
      'align-items': 'center',
      'justify-content': 'space-between',
      gap: '8px',
    })
    expect(style(featBtn(rec, 'Read PDFs and scans'))).not.toHaveProperty('display')
  })

  it('SH-12 Book the Demo is the outlineDark link, or absent', () => {
    const ts = sidebar(home, 'https://l.example/?demo')
    const a = withText(ts, 'Book the Demo')
    expect(a.name).toBe('a')
    expect(attr(a, 'class')).toContain('ds-btn--outlineDark')
    expect(attr(a, 'class')).toContain('ds-btn--sm')
    expect(attr(a, 'href')).toBe('https://l.example/?demo')
    expect(style(a).width).toBe('100%')
    expect(ts.filter((t) => style(t)['border-top'] === '1px solid var(--line-1)')).toHaveLength(1)

    const none = sidebar(home, null)
    expect(none.some((t) => t.text === 'Book the Demo')).toBe(false)
    expect(none.some((t) => t.name === 'a')).toBe(false)
    expect(none.filter((t) => style(t)['border-top'] === '1px solid var(--line-1)')).toHaveLength(1)
    expect(withText(none, 'Cookie choices').name).toBe('button')
  })

  it('SH-20 the footer holds Cookie choices whether or not Book the Demo shows', () => {
    const footerOf = (ts: Tag[]) => {
      const i = ts.findIndex((t) => style(t)['border-top'] === '1px solid var(--line-1)' && attr(t, 'class') === undefined)
      expect(i, 'the footer block').toBeGreaterThan(-1)
      return ts.slice(i + 1)
    }
    const withDemo = footerOf(sidebar(home, 'https://l.example/?demo'))
    const kids = withDemo.filter((t) => t.name === 'a' || t.name === 'button')
    expect(kids.map((t) => t.text)).toEqual(['Book the Demo', 'Cookie choices'])
    const cc = kids[1]
    expect(cc.name).toBe('button')
    expect(attr(cc, 'class')).toBe('lib-cookie-choices')
    expect(style(cc)).toMatchObject({ 'font-size': '13px', color: 'var(--fg-3)' })

    const bare = footerOf(sidebar(home, null)).filter((t) => t.name === 'a' || t.name === 'button')
    expect(bare.map((t) => t.text)).toEqual(['Cookie choices'])
    expect(attr(bare[0], 'class')).toBe('lib-cookie-choices')
  })

  it('SH-13 the stepper shows the 6 stages with a chevron between each', () => {
    const ts = stepper(home)
    withText(ts, 'INVOICE JOURNEY')
    const stages = buttons(ts)
    expect(stages).toHaveLength(6)
    const label = (b: Tag) => `${after(ts, b, 'span').text} ${after(ts, b, 'span', 2).text}`
    expect(stages.map(label)).toEqual(STAGES.map(([l], i) => `0${i + 1} ${l}`))
    expect(stages.map(label)).toEqual(['01 Import', '02 Extract', '03 Validate', '04 Approve', '05 Clear', '06 Archive'])
    for (const b of stages) {
      expect(style(b)).toMatchObject({
        border: '1px solid transparent',
        color: 'var(--muted-foreground)',
        gap: '7px',
        padding: '6px 10px',
        'font-size': '12.5px',
        'font-weight': '700',
        'border-radius': '6px',
      })
      expect(style(after(ts, b, 'span'))).toMatchObject({ 'font-size': '10px', opacity: '0.7' })
      expect(attr(after(ts, b, 'span'), 'class')).toBe('mono')
    }
    const svgs = ts.filter((t) => t.name === 'svg')
    expect(svgs).toHaveLength(5)
    for (const s of svgs) expect(attr(s, 'width')).toBe('14')
    expect(ts.filter((t) => t.name === 'path' && attr(t, 'd') === 'm9 18 6-6-6-6')).toHaveLength(5)
    const wrappers = ts.filter((t) => t.name === 'span' && style(t).color === 'var(--input)')
    expect(wrappers).toHaveLength(5)
    expect(style(withText(ts, 'INVOICE JOURNEY'))).toMatchObject({ 'margin-right': '10px', 'font-size': '10px' })
  })

  it("SH-14 the active stage follows the route's group", () => {
    const active = (route: Route) =>
      buttons(stepper(route)).map((b) => style(b).background === 'var(--sage-panel)')
    STAGES.forEach(([, gid], i) => {
      const ts = stepper(parseLibraryPath(`/${gid}`))
      const bs = buttons(ts)
      expect(active(parseLibraryPath(`/${gid}`)), gid).toEqual(STAGES.map((_, j) => j === i))
      expect(style(bs[i])).toMatchObject({
        border: '1px solid var(--tab-active-border)',
        color: 'var(--tab-active-text)',
      })
      for (const [j, b] of bs.entries())
        if (j !== i) expect(style(b)).toMatchObject({ border: '1px solid transparent', color: 'var(--muted-foreground)' })
    })
    expect(active(parseLibraryPath('/clearance/submit-clear'))).toEqual([false, false, false, false, true, false])
    expect(active(parseLibraryPath('/reports'))).toEqual(Array(6).fill(false))
    expect(active(home)).toEqual(Array(6).fill(false))
  })

  it('SH-14b aria-current marks only the active stage', () => {
    const marked = (route: Route) => buttons(stepper(route)).map((b) => attr(b, 'aria-current'))
    const i = STAGES.findIndex(([, g]) => g === 'rules')
    expect(marked(parseLibraryPath('/rules'))).toEqual(STAGES.map((_, j) => (j === i ? 'true' : undefined)))
    expect(marked(home)).toEqual(Array(6).fill(undefined))
  })

  it('SH-15 the stepper bar is sticky over a blurred header', () => {
    const bar = stepper(home)[0]
    expect(style(bar)).toMatchObject({
      position: 'sticky',
      top: '0',
      'z-index': '20',
      'min-height': '64px',
      padding: '8px 40px',
      background: 'var(--header-bg)',
      'backdrop-filter': 'blur(18px)',
      '-webkit-backdrop-filter': 'blur(18px)',
      'border-bottom': '1px solid var(--header-border)',
    })
  })

  it('SH-16 each sidebar and stepper button calls its handler with the right target', () => {
    type El = { type: unknown; props: { type?: string; children?: unknown; onClick?: () => void; id?: string; className?: string } }
    const walk = (n: unknown, out: El[] = []): El[] => {
      if (Array.isArray(n)) n.forEach((c) => walk(c, out))
      else if (n && typeof n === 'object' && 'props' in n) {
        out.push(n as El)
        walk((n as El).props.children, out)
      }
      return out
    }
    const buttonsOf = (tree: unknown) => walk(tree).filter((e) => e.type === 'button')
    const calls: string[] = []
    const props = {
      route: parseLibraryPath('/recognition'),
      demoHref: null,
      onHome: () => calls.push('home'),
      onGroup: (g: { id: string }) => calls.push(`group:${g.id}`),
      onFeature: (f: { id: string }) => calls.push(`feature:${f.id}`),
      onTour: () => calls.push('tour'),
      onCookieChoices: () => calls.push('cookies'),
    }
    const bs = buttonsOf(Sidebar(props))
    const click = (b: El) => b.props.onClick!()
    expect(bs.length).toBeGreaterThan(12)
    for (const b of bs) expect(b.props.type).toBe('button')
    click(bs[0])
    click(bs.find((b) => b.props.className === 'lib-tour')!)
    click(bs.filter((b) => b.props.className === 'lib-nav')[0])
    for (const g of GROUPS) click(bs.find((b) => b.props.id === `nav-${g.id}`)!)
    const recognition = GROUPS.find((g) => g.id === 'recognition')!
    const featBtns = bs.filter((b) => b.props.className === 'lib-nav' && !b.props.id).slice(1)
    expect(featBtns).toHaveLength(recognition.feats.length)
    for (const b of featBtns) click(b)
    click(bs.find((b) => b.props.className === 'lib-cookie-choices')!)
    expect(calls).toEqual([
      'home',
      'tour',
      'home',
      ...GROUPS.map((g) => `group:${g.id}`),
      ...recognition.feats.map((f) => `feature:${f.id}`),
      'cookies',
    ])

    const stageCalls: string[] = []
    const stages = buttonsOf(JourneyStepper({ route: home, onGroup: (g) => stageCalls.push(g.id) }))
    expect(stages).toHaveLength(6)
    for (const b of stages) expect(b.props.type).toBe('button')
    for (const b of stages) click(b)
    expect(stageCalls).toEqual(STAGES.map(([, gid]) => gid))
  })

  it('SH-17 the sidebar tour label is a prop', () => {
    const tourSpan = (props: object) => {
      const ts = render(createElement(Sidebar, { route: home, demoHref: null, onHome: noop, onGroup: noop, onFeature: noop, onTour: noop, ...props }))
      const btnTag = ts.find((t) => attr(t, 'class') === 'lib-tour')!
      const inside = ts.slice(ts.indexOf(btnTag) + 1)
      return { span: inside.find((t) => t.name === 'span')!, svgFirst: inside.findIndex((t) => t.name === 'svg') < inside.findIndex((t) => t.name === 'span') }
    }
    const dflt = tourSpan({})
    expect(dflt.span.text).toBe('Take the tour')
    expect(dflt.svgFirst).toBe(true)
    const set = tourSpan({ tourLabel: 'Tour 3 of 14 · exit' })
    expect(set.span.text).toBe('Tour 3 of 14 · exit')
    expect(set.svgFirst).toBe(true)
  })

  it('SH-18 a null onTour removes the sidebar tour button and keeps its siblings', () => {
    const demoHref = 'https://l.example/?demo'
    const tourBtns = (ts: Tag[]) => ts.filter((t) => attr(t, 'class') === 'lib-tour')
    const off = render(createElement(Sidebar, { route: home, demoHref, onHome: noop, onGroup: noop, onFeature: noop, onTour: null }))
    expect(tourBtns(off)).toHaveLength(0)
    expect(off.some((t) => t.text === 'LIBRARY')).toBe(true)
    expect(only(off, 'nav')).toBeDefined()
    expect(withText(off, 'Book the Demo')).toBeDefined()
    expect(tourBtns(sidebar(home, demoHref))).toHaveLength(1)
  })

  it('SH-19 tourStage overrides the route in the stepper', () => {
    const audit = parseLibraryPath('/audit')
    const marked = (tourStage?: number) => {
      const ts = render(createElement(JourneyStepper, { route: audit, onGroup: noop, tourStage }))
      return buttons(ts)
        .filter((b) => attr(b, 'aria-current') !== undefined)
        .map((b) => after(ts, b, 'span', 2).text)
    }
    expect(marked()).toEqual(['Archive'])
    expect(marked(2)).toEqual(['Validate'])
    expect(marked(-1)).toEqual([])
  })
})
