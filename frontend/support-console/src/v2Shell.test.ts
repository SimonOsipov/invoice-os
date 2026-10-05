import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { Drawer } from './components/Drawer'
import { JobDrawer } from './components/JobDrawer'
import { KillConfirm } from './components/KillConfirm'
import { Modal } from './components/Modal'
import { PublishModal } from './components/PublishModal'
import { RuleDrawer } from './components/RuleDrawer'
import { Sidebar } from './components/Sidebar'
import { Toast } from './components/Toast'
import { TopBar } from './components/TopBar'
import { NAV_ITEMS, SEED_JOBS, SEED_RULES } from './data'
import type { Env, Screen } from './types'

// SSR markup is the oracle: jsdom drops backdrop-filter and Chromium aliases the prefixed one.
// The resolved look is SUP-01, SUP-02 and SUP-04 (deploy gate).
const noop = () => {}

type Tag = { name: string; attrs: string; text: string }
const attr = (t: { attrs: string }, name: string) => t.attrs.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? ''
// Parsed, not substring: `-webkit-backdrop-filter:X` contains `backdrop-filter:X`.
const style = (t: { attrs: string }): Record<string, string> =>
  Object.fromEntries(
    attr(t, 'style')
      .split(';')
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)]),
  )
const classes = (t: { attrs: string }) => attr(t, 'class').split(/\s+/).filter(Boolean)
const decode = (s: string) => s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#x27;/g, "'")
const parse = (html: string): Tag[] =>
  [...html.matchAll(/<([a-z][a-z0-9]*)\b([^>]*)>([^<]*)/g)].map((m) => ({ name: m[1], attrs: ` ${m[2]}`, text: decode(m[3]).trim() }))
const withClass = (ts: Tag[], c: string) => ts.filter((t) => classes(t).includes(c))
const withText = (ts: Tag[], text: string) => {
  const hit = ts.filter((t) => t.text === text)
  expect(hit, `exactly one tag with text "${text}"`).toHaveLength(1)
  return hit[0]
}
const buttonsOf = (html: string) =>
  [...html.matchAll(/<button\b([^>]*)>([\s\S]*?)<\/button>/g)].map((m) => ({
    attrs: ` ${m[1]}`,
    inner: m[2],
    text: decode(m[2].replace(/<[^>]*>/g, '')).trim(),
  }))
const buttonByText = (html: string, text: string) => {
  const hit = buttonsOf(html).filter((b) => b.text === text)
  expect(hit, `exactly one button "${text}"`).toHaveLength(1)
  return hit[0]
}
const first = (ts: Tag[], what: string): Tag => {
  expect(ts.length, `${what}: markup parsed`).toBeGreaterThan(0)
  return ts[0]
}

const ENVS: Env[] = ['sandbox', 'live']
const topBar = (env: Env) => renderToStaticMarkup(createElement(TopBar, { screen: 'submissions', env, onSetEnv: noop }))
const sidebar = (screen: Screen) => renderToStaticMarkup(createElement(Sidebar, { screen, onNavigate: noop, deadLetterCount: 2 }))
const RADIUS_ALLOWED = ['var(--radius-md)', 'var(--radius-sm)', 'var(--radius-btn)', '50%', '2px']
const V1_CORNER = /radius-input|radius-xs|^99(px)?$|^999(px)?$/

describe('v2 shell', () => {
  it('SH-01 the header declares the v2 header tokens and the blur on both filter properties', () => {
    for (const env of ENVS) {
      const html = topBar(env)
      const headers = parse(html).filter((t) => t.name === 'header')
      expect(headers, `${env}: one <header`).toHaveLength(1)
      const s = style(headers[0])
      expect(Object.keys(s).length, `${env}: header style parsed`).toBeGreaterThan(0)

      expect.soft(s.background, `${env}: header background`).toBe('var(--header-bg)')
      expect.soft(s['border-bottom'], `${env}: header bottom edge`).toBe('1px solid var(--header-border)')
      expect.soft(s['backdrop-filter'], `${env}: backdrop-filter`).toBe('blur(var(--header-blur))')
      expect.soft(s['-webkit-backdrop-filter'], `${env}: -webkit-backdrop-filter missing or not the token`).toBe('blur(var(--header-blur))')
      expect.soft(html, `${env}: markup holds oklch`).not.toContain('oklch')
    }
  })

  it('SH-02 the env switch fills the active segment with --primary in both envs', () => {
    const cases = {
      sandbox: { track: 'var(--status-amber-border)', dots: ['var(--accent)', 'var(--status-green-text)'] },
      live: { track: 'var(--status-green-border)', dots: ['var(--status-amber-text)', '#8fdcaa'] },
    } as const
    for (const env of ENVS) {
      const html = topBar(env)
      const ts = parse(html)
      const segs = buttonsOf(html)
      expect(
        segs.map((b) => b.text),
        `${env}: the two segments`,
      ).toEqual(['SANDBOX', 'LIVE'])

      const track = ts[ts.findIndex((t) => t.name === 'button') - 1]
      expect(track?.name, `${env}: the track wraps the buttons`).toBe('div')
      expect.soft(style(track).background, `${env}: track background`).toBe('var(--sage)')
      expect.soft(style(track).gap, `${env}: track gap`).toBe('2px')
      expect.soft(style(track).border, `${env}: track border keeps the env colour (pin)`).toBe(`1px solid ${cases[env].track}`)

      segs.forEach((b, i) => {
        const active = (env === 'sandbox') === (i === 0)
        const at = `${env}: ${b.text}`
        const s = style(b)
        expect.soft(classes(b), `${at}: class list`).not.toContain('ops-btn')
        expect.soft(s['border-radius'], `${at}: radius`).toBe('var(--radius-sm)')
        expect.soft(s.background, `${at}: background`).toBe(active ? 'var(--primary)' : 'transparent')
        if (active) expect.soft(s.color, `${at}: active colour`).toBe('var(--primary-foreground)')

        const dot = first(parse(b.inner), `${at}: dot`)
        expect(style(dot).width, `${at}: first child is the 6px dot`).toBe('6px')
        expect.soft(style(dot).background, `${at}: dot colour`).toBe(cases[env].dots[i])
        expect.soft(style(dot)['border-radius'], `${at}: dot corner`).toBe('50%')
      })
    }
  })

  it('SH-03 the drawer scrim mixes --surface and blurs on both properties; the panel has no shadow', () => {
    const html = renderToStaticMarkup(createElement(Drawer, { header: 'h', onClose: noop, children: 'x' }))
    const ts = parse(html)
    expect(ts.length, 'drawer markup parsed').toBeGreaterThan(2)
    const [scrim, panel] = ts
    const sc = style(scrim)
    const pn = style(panel)

    expect(sc.position, 'the first root element is the fixed scrim').toBe('fixed')
    expect.soft(sc.background, 'scrim background').toContain('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect.soft(sc['backdrop-filter'], 'scrim backdrop-filter').toBe('blur(6px)')
    expect.soft(sc['-webkit-backdrop-filter'], 'scrim -webkit-backdrop-filter missing').toBe('blur(6px)')

    expect(classes(panel), 'the second root element is the panel').toContain('ops-drawer')
    expect(pn.position, 'the panel style parsed').toBe('fixed')
    expect.soft(pn['border-left'], 'panel left edge').toBe('1px solid var(--line-2)')
    expect.soft(Object.keys(pn), 'panel box-shadow').not.toContain('box-shadow')
    expect.soft(html, 'markup holds oklch').not.toContain('oklch')

    const close = buttonByText(html, '')
    expect.soft(attr(close, 'aria-label'), 'the empty-text button is Close').toBe('Close')
    expect.soft(style(close)['border-radius'], 'close button radius').toBe('var(--radius-btn)')
  })

  it('SH-04 the modal sits 10px over the --surface scrim with the card shadow', () => {
    const html = renderToStaticMarkup(createElement(Modal, { onClose: noop, children: 'x' }))
    const ts = parse(html)
    expect(ts.length, 'modal markup parsed').toBeGreaterThan(1)
    const [outer, panel] = ts
    const o = style(outer)
    const p = style(panel)

    expect(o.position, 'the outermost element is the fixed scrim').toBe('fixed')
    expect.soft(o.background, 'scrim background').toContain('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect.soft(o['backdrop-filter'], 'scrim backdrop-filter').toBe('blur(6px)')
    expect.soft(o['-webkit-backdrop-filter'], 'scrim -webkit-backdrop-filter missing').toBe('blur(6px)')

    expect(attr(panel, 'role'), 'the first child is the dialog panel').toBe('dialog')
    expect.soft(p['border-radius'], 'panel radius').toBe('var(--radius-lg)')
    expect.soft(p['box-shadow'], 'panel shadow').toBe('var(--shadow-card)')
    expect.soft(p.background, 'panel keeps bg-2 (pin)').toBe('var(--bg-2)')
    expect.soft(p.border, 'panel keeps its edge (pin)').toBe('1px solid var(--line-2)')
    expect.soft(p.overflow, 'panel keeps overflow hidden (pin)').toBe('hidden')
    expect.soft(html, 'markup holds oklch').not.toContain('oklch')
  })

  it('SH-05 the toast is a dark scope for both tones', () => {
    const cases = [
      { tone: 'ok', icon: 'var(--teal-300)' },
      { tone: 'red', icon: 'var(--status-red-text)' },
    ] as const
    for (const { tone, icon } of cases) {
      const html = renderToStaticMarkup(createElement(Toast, { toast: { msg: 'm', tag: 'AUDIT', tone } }))
      const ts = parse(html)
      expect(ts.length, `${tone}: toast markup parsed`).toBeGreaterThan(2)
      const [root, iconSpan] = ts
      expect(attr(root, 'role'), `${tone}: the first tag is the status root`).toBe('status')
      const r = style(root)

      expect.soft(classes(root), `${tone}: root class`).toContain('asc-dark')
      expect.soft(r.background, `${tone}: root background`).toBe('var(--surface)')
      expect.soft(r['border-radius'], `${tone}: root radius`).toBe('var(--radius-md)')
      expect.soft(r['box-shadow'], `${tone}: root shadow`).toBe('var(--shadow-card)')
      expect.soft(r.color, `${tone}: root colour stays (pin)`).toBe('var(--text-on-dark)')
      expect.soft(style(iconSpan).color, `${tone}: icon colour`).toBe(icon)

      const tag = withText(ts, 'AUDIT')
      expect.soft(style(tag).color, `${tone}: tag colour`).toBe('var(--surface-body)')
      expect.soft(style(tag)['border-left'], `${tone}: tag left edge`).toBe('1px solid var(--surface-panel-border)')
      expect.soft(html, `${tone}: markup holds oklch`).not.toContain('oklch')
    }
  })

  it('SH-06 the sidebar is a dark scope with v2 corners and readable labels', () => {
    for (const active of NAV_ITEMS.map((n) => n.key)) {
      const html = sidebar(active)
      const ts = parse(html)
      const aside = ts.filter((t) => t.name === 'aside')
      expect(aside, `${active}: one <aside`).toHaveLength(1)
      expect.soft(classes(aside[0]), `${active}: aside class`).toEqual(expect.arrayContaining(['ops-sidebar', 'asc-dark']))
      expect.soft(style(aside[0]).background, `${active}: aside background`).toBe('var(--surface)')
      expect.soft(style(aside[0])['border-right'], `${active}: aside edge`).toBe('1px solid var(--surface-panel-border)')

      const nav = buttonsOf(html).filter((b) => classes(b).includes('ops-nav'))
      expect(nav, `${active}: one nav button per item`).toHaveLength(NAV_ITEMS.length)
      nav.forEach((b, i) => {
        const on = NAV_ITEMS[i].key === active
        const at = `${active}: nav ${NAV_ITEMS[i].key}`
        const [bar, icon] = parse(b.inner)
        expect.soft(style(b).background, `${at}: background`).toBe(on ? 'var(--bg-3)' : 'transparent')
        expect.soft(style(b)['border-radius'], `${at}: radius`).toBe('var(--radius-md)')
        expect.soft(style(bar)['border-radius'], `${at}: bar radius`).toBe('2px')
        expect.soft(style(bar).background, `${at}: bar background`).toBe(on ? 'var(--action)' : 'transparent')
        expect.soft(style(icon).color, `${at}: icon colour`).toBe(on ? 'var(--action)' : 'var(--fg-3)')
      })
    }

    const html = sidebar('submissions')
    const ts = parse(html)
    const labels = withClass(ts, 'label')
    expect(
      labels.map((l) => l.text),
      'the two labels',
    ).toEqual(['Operations', 'APP backpressure'])
    for (const l of labels) expect.soft(style(l).color, `label "${l.text}"`).toBe('var(--eyebrow-on-dark)')

    const badges = withClass(ts, 'mono').filter((t) => /^\d+$/.test(t.text))
    expect(
      badges.map((t) => t.text),
      'the Submissions and Rules badges',
    ).toEqual(['2', '3'])
    for (const b of badges) {
      expect.soft(style(b)['border-radius'], `badge ${b.text} radius`).toBe('var(--radius-sm)')
      expect.soft(classes(b), `badge ${b.text} keeps .mono`).toContain('mono')
    }

    const imgs = ts.filter((t) => t.name === 'img')
    expect(imgs, 'one brand <img').toHaveLength(1)
    expect.soft(attr(imgs[0], 'width'), 'mark width').toBe('26')
    expect.soft(attr(imgs[0], 'height'), 'mark height').toBe('26')

    expect.soft(style(withText(ts, 'EI'))['border-radius'], 'avatar corner').toBe('50%')
    const out = buttonsOf(html).filter((b) => attr(b, 'aria-label') === 'Sign out')
    expect(out, 'one Sign out button').toHaveLength(1)
    expect.soft(style(out[0])['border-radius'], 'Sign out radius').toBe('var(--radius-btn)')

    const corners = ts.map((t) => style(t)['border-radius']).filter((v): v is string => v !== undefined)
    expect(corners.length, 'sidebar corners collected').toBeGreaterThan(8)
    expect(V1_CORNER.test('var(--radius-input)') && V1_CORNER.test('99px') && V1_CORNER.test('999px'), 'control: the needle matches v1 corners').toBe(true)
    expect(V1_CORNER.test('var(--radius-md)') || V1_CORNER.test('2px'), 'control: the needle skips v2 corners').toBe(false)
    expect.soft(
      corners.filter((v) => V1_CORNER.test(v)),
      'v1 corners left in the sidebar',
    ).toEqual([])
    expect.soft(
      corners.filter((v) => !RADIUS_ALLOWED.includes(v)),
      'corners outside the v2 set',
    ).toEqual([])
  })

  it('SH-07 the modal and drawer bodies carry v2 weights, corners and Q3 text', () => {
    const kill = renderToStaticMarkup(createElement(KillConfirm, { ruleKey: 'line.qty.range', env: 'sandbox', onClose: noop, onConfirm: noop }))
    const publish = renderToStaticMarkup(createElement(PublishModal, { onClose: noop, onConfirm: noop }))
    const rule = (testRan: boolean) =>
      renderToStaticMarkup(createElement(RuleDrawer, { rule: SEED_RULES[0], testRan, onRunTest: noop, onKill: noop, onClose: noop }))
    const idle = rule(false)
    const ran = rule(true)
    const job = renderToStaticMarkup(
      createElement(JobDrawer, {
        job: SEED_JOBS[0],
        env: 'sandbox',
        reqOpen: true,
        resOpen: true,
        onToggleReq: noop,
        onToggleRes: noop,
        onClose: noop,
        onReDrive: noop,
        onRePoll: noop,
        onCancel: noop,
      }),
    )

    for (const [name, html] of [['KillConfirm', kill], ['PublishModal', publish]] as const) {
      const h3 = parse(html).filter((t) => t.name === 'h3')
      expect(h3, `${name}: one <h3`).toHaveLength(1)
      const s = style(h3[0])
      expect(s['font-size'], `${name}: h3 keeps its size`).toBe('17px')
      expect.soft(Object.keys(s), `${name}: h3 inline font-weight`).not.toContain('font-weight')
      expect.soft(Object.keys(s), `${name}: h3 inline letter-spacing`).not.toContain('letter-spacing')
    }

    const killTags = parse(kill)
    const note = killTags.filter((t) => style(t).background === 'var(--status-amber-bg)')
    expect(note, 'one amber note').toHaveLength(1)
    expect.soft(style(note[0])['border-radius'], 'amber note radius').toBe('var(--radius-md)')
    const tile = killTags.filter((t) => style(t).width === '36px' && style(t).height === '36px')
    expect(tile, 'one kill icon tile').toHaveLength(1)
    expect.soft(style(tile[0])['border-radius'], 'kill icon tile radius').toBe('var(--radius-md)')

    expect(ran, 'testRan renders the passed text').toContain('Rule passed')
    const passed = parse(ran).filter((t) => style(t).background === 'var(--status-green-bg)')
    expect(passed, 'one passed box').toHaveLength(1)
    expect.soft(style(passed[0])['border-radius'], 'Rule passed box radius').toBe('var(--radius-md)')

    const disable = buttonByText(kill, 'Disable rule')
    expect.soft(style(disable).color, 'Disable rule colour').toBe('var(--primary-foreground)')
    expect.soft(style(disable)['border-radius'], 'Disable rule radius').toBe('var(--radius-btn)')
    expect.soft(style(buttonByText(idle, 'Kill-switch'))['border-radius'], 'Kill-switch radius').toBe('var(--radius-btn)')
    const cancel = buttonByText(job, 'Cancel')
    expect.soft(style(cancel)['border-radius'], 'Cancel radius').toBe('var(--radius-btn)')
    expect.soft(style(cancel)['font-weight'], 'Cancel weight').toBe('600')

    const run = buttonByText(idle, 'Run test')
    expect(style(run).height, 'Run test keeps its inline height').toBe('28px')
    expect.soft(classes(run), 'Run test classes').toEqual(expect.arrayContaining(['ops-btn', 'v2-btn', 'v2-btn-primary']))
    expect.soft(Object.keys(style(run)), 'Run test inline border-radius').not.toContain('border-radius')

    expect.soft(style(withText(parse(idle), 'No test run yet.')).color, 'No test run yet. colour').toBe('var(--fg-3)')

    const jobTags = parse(job)
    const steps = jobTags.filter((t) => style(t).width === '11px' && style(t).height === '11px')
    expect(steps, 'four timeline dots').toHaveLength(4)
    for (const d of steps) expect.soft(style(d)['border-radius'], 'timeline dot corner').toBe('50%')

    const badge = jobTags.filter((t) => style(t).padding === '2px 8px' && style(t).display === 'inline-flex')
    expect(badge, 'one state badge in the job header').toHaveLength(1)
    expect.soft(style(badge[0])['border-radius'], 'badge radius').toBe('var(--radius-sm)')
    const dots = jobTags.filter((t) => style(t).width === '6px' && style(t).height === '6px')
    expect(dots.length, 'badge dot present').toBeGreaterThan(0)
    for (const d of dots) expect.soft(style(d)['border-radius'], 'badge dot corner').toBe('50%')
  })

  it('SH-08 header text and the banner tag meet Q3', () => {
    const banner = { sandbox: 'CROSS-TENANT · ALL ENTITIES', live: 'CROSS-TENANT · PENDING ACCREDITATION' }
    for (const env of ENVS) {
      const ts = parse(topBar(env))
      expect.soft(style(withText(ts, 'Search IRN · invoice # · TIN · job ID · tenant')).color, `${env}: search text colour`).toBe('var(--fg-3)')
      expect.soft(style(withText(ts, '⌘K')).color, `${env}: ⌘K colour`).toBe('var(--fg-3)')

      const tag = style(withText(ts, banner[env]))
      expect(tag['font-size'], `${env}: banner tag found`).toBe('10px')
      expect.soft(tag['white-space'], `${env}: banner tag white-space`).toBe('nowrap')
      expect.soft(Object.keys(tag), `${env}: banner tag opacity`).not.toContain('opacity')

      const box = withClass(ts, 'ops-header-search')
      expect(box, `${env}: one search box`).toHaveLength(1)
      expect.soft(style(box[0]).border, `${env}: search box border`).toBe('1px solid var(--input)')
      expect.soft(style(box[0])['border-radius'], `${env}: search box radius`).toBe('var(--radius-btn)')
    }
  })
})
