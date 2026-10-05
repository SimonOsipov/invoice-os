import { createElement, type ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

import { EvidenceDrawer } from './components/EvidenceDrawer'
import { JobDrawer } from './components/JobDrawer'
import { RotateConfirm } from './components/RotateConfirm'
import { Sidebar } from './components/Sidebar'
import { Toast } from './components/Toast'
import { TopBar } from './components/TopBar'
import { DEV_ORGS, EVIDENCE_DATA, NAV_ITEMS, SEED_SUBMISSIONS } from './data'
import type { Env, Screen, ToastTone } from './types'

// The org menu opens on a click and SSR has no clicks: force the first useState(false) open.
const forced = vi.hoisted(() => ({ open: false }))
vi.mock('react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react')>()
  const useState = ((init: unknown) => (forced.open && init === false ? [true, () => {}] : actual.useState(init))) as typeof actual.useState
  return { ...actual, useState }
})

// jsdom drops backdrop-filter and Chromium aliases the prefixed one, so SSR markup is the oracle.
const openTags = (html: string) => html.match(/<[a-z][^>]*>/g) ?? []
const attr = (tag: string, name: string) => tag.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? ''
const css = (tag: string): Record<string, string> =>
  Object.fromEntries(
    attr(tag, 'style')
      .split(';')
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)]),
  )
const classes = (tag: string) => attr(tag, 'class').split(/\s+/).filter(Boolean)
const render = <P extends object>(c: (p: P) => ReactElement, p: P) => renderToStaticMarkup(createElement(c, p))
const find = (html: string, pred: (tag: string) => boolean) => openTags(html).filter(pred)
const noop = () => {}

type El = { type: unknown; props: { children?: unknown; onClick?: (e?: unknown) => void } }
const isEl = (n: unknown): n is El => !!n && typeof n === 'object' && 'props' in n
const walk = (n: unknown, out: El[] = []): El[] => {
  if (Array.isArray(n)) n.forEach((c) => walk(c, out))
  else if (isEl(n)) {
    out.push(n)
    walk(n.props.children, out)
  }
  return out
}
const textOf = (n: unknown): string =>
  typeof n === 'string' || typeof n === 'number' ? String(n) : Array.isArray(n) ? n.map(textOf).join('') : isEl(n) ? textOf(n.props.children) : ''
const buttons = (n: unknown) => walk(n).filter((e) => e.type === 'button')
const button = (n: unknown, label: string) => {
  const hit = buttons(n).filter((b) => textOf(b.props.children).trim() === label)
  expect(hit, `button "${label}"`).toHaveLength(1)
  return hit[0]
}
const SCREENS = NAV_ITEMS.map((n) => n.key) as Screen[]

describe('v2 shell', () => {
  it('SH-01 the header declares the blur token on both filter properties', () => {
    const html = renderToStaticMarkup(createElement(TopBar, { screen: 'overview', env: 'sandbox', onSetEnv: () => {} }))
    const header = openTags(html).find((t) => t.startsWith('<header'))
    expect(header, `no <header in ${html}`).toBeDefined()
    const style = attr(header!, 'style')
    expect(style.length).toBeGreaterThan(0)

    // Parsed, not substring: `-webkit-backdrop-filter:X` contains `backdrop-filter:X`.
    expect.soft(css(header!).background, 'header background').toBe('var(--header-bg)')
    expect.soft(css(header!)['backdrop-filter'], 'unprefixed filter').toBe('blur(var(--header-blur))')
    expect.soft(css(header!)['-webkit-backdrop-filter'], '-webkit-backdrop-filter missing or not the token').toBe('blur(var(--header-blur))')
    expect.soft(style, 'header holds oklch').not.toContain('oklch')
  })

  it('SH-02 the modal scrim mixes --surface and blurs on both properties', () => {
    const html = renderToStaticMarkup(createElement(RotateConfirm, { env: 'LIVE', onClose: () => {}, onConfirm: () => {} }))
    const [scrim = '', panel = ''] = openTags(html)
    expect(scrim, `no scrim in ${html}`).not.toBe('')
    expect(panel, `no panel in ${html}`).not.toBe('')
    const scrimStyle = attr(scrim, 'style')
    const panelStyle = attr(panel, 'style')
    expect(scrimStyle.length).toBeGreaterThan(0)
    expect(panelStyle.length).toBeGreaterThan(0)

    expect.soft(scrimStyle, 'scrim background').toContain('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect.soft(css(scrim)['backdrop-filter'], 'unprefixed filter').toBe('blur(6px)')
    expect.soft(css(scrim)['-webkit-backdrop-filter'], '-webkit-backdrop-filter missing').toBe('blur(6px)')
    expect.soft(panelStyle, 'panel radius').toContain('border-radius:var(--radius-lg)')
    expect.soft(panelStyle, 'panel shadow').not.toContain('box-shadow')
  })

  it('SH-03 the toast is a dark scope for both tones', () => {
    const cases = [
      { tone: 'ok', icon: 'var(--teal-300)' },
      { tone: 'red', icon: 'var(--status-red-text)' },
    ] as const
    for (const { tone, icon } of cases) {
      const html = renderToStaticMarkup(createElement(Toast, { toast: { msg: 'm', tag: '', tone } }))
      const [root = '', iconSpan = ''] = openTags(html)
      expect(root, `${tone}: no root in ${html}`).not.toBe('')
      expect(iconSpan, `${tone}: no icon span in ${html}`).not.toBe('')
      const rootStyle = attr(root, 'style')
      expect(rootStyle.length).toBeGreaterThan(0)

      expect.soft(attr(root, 'class').split(/\s+/), `${tone}: root class`).toContain('asc-dark')
      expect.soft(rootStyle, `${tone}: root background`).toContain('background:var(--surface)')
      expect.soft(rootStyle, `${tone}: root shadow`).toContain('box-shadow:var(--shadow-card)')
      expect.soft(attr(iconSpan, 'style'), `${tone}: icon colour`).toContain(`color:${icon}`)
    }
  })

  const sidebar = (screen: Screen, deadLetterCount = 0, open = false) => {
    forced.open = open
    try {
      return render(Sidebar, { screen, onNavigate: noop, deadLetterCount })
    } finally {
      forced.open = false
    }
  }
  const navBlocks = (html: string) => html.match(/<button[^>]*class="ops-nav"[\s\S]*?<\/button>/g) ?? []

  it('SH-04 the sidebar is an asc-dark scope on --surface with a panel-border edge, mark 22, circle avatar', () => {
    const html = sidebar('overview')
    const [aside] = find(html, (t) => t.startsWith('<aside'))
    expect(aside, `no <aside in ${html}`).toBeDefined()
    expect(classes(aside)).toEqual(expect.arrayContaining(['ops-sidebar', 'asc-dark']))
    expect(css(aside).background).toBe('var(--surface)')
    expect(css(aside)['border-right']).toBe('1px solid var(--surface-panel-border)')

    const marks = find(html, (t) => t.startsWith('<img'))
    expect(marks).toHaveLength(1)
    expect([attr(marks[0], 'width'), attr(marks[0], 'height')]).toEqual(['22', '22'])
    expect(css(marks[0])['border-radius']).toBeUndefined()

    const avatars = find(html, (t) => css(t).width === '30px' && css(t).height === '30px')
    expect(avatars).toHaveLength(1)
    expect(css(avatars[0])['border-radius']).toBe('50%')
    expect(css(avatars[0]).color).toBe('var(--surface-foreground)')
  })

  it('SH-05 the dark-sidebar labels carry the on-dark eyebrow colour (D-3)', () => {
    const html = sidebar('overview')
    const labels = find(html, (t) => classes(t).includes('label'))
    expect(labels.length).toBeGreaterThan(0)
    for (const l of labels) expect(css(l).color, `label ${l}`).toBe('var(--eyebrow-on-dark)')
    expect(html).toContain('>Console<')
    expect(html).toContain('>Requests this month<')
  })

  it('SH-06 exactly the active nav row is --bg-3 with a --action bar and icon; the rest are idle', () => {
    expect(SCREENS.length).toBeGreaterThan(1)
    for (const screen of SCREENS) {
      const blocks = navBlocks(sidebar(screen))
      expect(blocks, screen).toHaveLength(NAV_ITEMS.length)
      blocks.forEach((block, i) => {
        const active = NAV_ITEMS[i].key === screen
        const [btn = '', bar = '', icon = ''] = openTags(block)
        const at = `${screen} row ${NAV_ITEMS[i].key}`
        expect(css(btn).background, `${at} background`).toBe(active ? 'var(--bg-3)' : 'transparent')
        expect(css(btn).color, `${at} colour`).toBe(active ? 'var(--fg-1)' : 'var(--fg-2)')
        expect(css(btn)['border-radius'], `${at} radius`).toBe('var(--radius-md)')
        expect(css(bar).background, `${at} bar`).toBe(active ? 'var(--action)' : 'transparent')
        expect(css(bar)['border-radius'], `${at} bar radius`).toBe('2px')
        expect(css(icon).color, `${at} icon`).toBe(active ? 'var(--action)' : 'var(--fg-3)')
      })
    }
  })

  it('SH-07 the dead-letter badge is a 4px .mono chip on Submissions only, and absent at zero', () => {
    const withCount = navBlocks(sidebar('overview', 3))
    const badged = withCount.filter((b) => classes(openTags(b).at(-1) ?? '').includes('mono'))
    expect(badged).toHaveLength(1)
    expect(badged[0]).toContain('Submissions')
    const badge = openTags(badged[0]).at(-1) ?? ''
    expect(classes(badge)).toEqual(expect.arrayContaining(['mono', 'ops-nav-label']))
    expect(css(badge)['border-radius']).toBe('var(--radius-sm)')
    expect(css(badge).background).toBe('var(--status-red-bg)')
    expect(badged[0]).toMatch(/>3<\/span>/)

    const none = navBlocks(sidebar('overview', 0))
    expect(none).toHaveLength(NAV_ITEMS.length)
    expect(none.filter((b) => b.includes('mono'))).toEqual([])
  })

  it('SH-08 the org menu is an asc-light --radius-md card; its button and items declare --fg-1', () => {
    const closed = sidebar('overview')
    expect(closed).toContain('aria-expanded="false"')
    expect(find(closed, (t) => attr(t, 'role') === 'menu')).toEqual([])

    const html = sidebar('overview', 0, true)
    const [toggle] = find(html, (t) => attr(t, 'aria-haspopup') === 'menu')
    expect(attr(toggle, 'aria-expanded')).toBe('true')
    expect(css(toggle).color).toBe('var(--fg-1)')
    expect(css(toggle).border).toBe('1px solid var(--action)')
    expect(css(toggle)['border-radius']).toBe('var(--radius-btn)')

    const menus = find(html, (t) => attr(t, 'role') === 'menu')
    expect(menus).toHaveLength(1)
    expect(classes(menus[0])).toContain('asc-light')
    expect(css(menus[0])['border-radius']).toBe('var(--radius-md)')
    expect(css(menus[0])['box-shadow']).toBe('var(--shadow-card)')
    expect(css(menus[0]).border).toBe('1px solid var(--line-2)')

    const items = find(html, (t) => attr(t, 'role') === 'menuitem')
    expect(items).toHaveLength(DEV_ORGS.length)
    for (const it of items) expect(css(it).color).toBe('var(--fg-1)')
  })

  it('SH-09 sign out keeps its label and is a --radius-btn button without ops-btn', () => {
    const [out] = find(sidebar('overview'), (t) => attr(t, 'aria-label') === 'Sign out')
    expect(out).toBeDefined()
    expect(attr(out, 'title')).toBe('Sign out')
    expect(css(out)['border-radius']).toBe('var(--radius-btn)')
  })

  const topBar = (env: Env) => render(TopBar, { screen: 'overview', env, onSetEnv: noop })
  const ENV_CASES = [
    { env: 'sandbox', track: 'var(--status-amber-border)', dots: ['var(--accent)', 'var(--status-green-text)'], banner: 'var(--status-amber-bg)' },
    { env: 'live', track: 'var(--status-green-border)', dots: ['var(--status-amber-text)', '#8fdcaa'], banner: 'var(--action-tint)' },
  ] as const

  it('SH-10 the env switch is a sage track of ops-btn-free 4px segments; dots follow D-5; border and banner keep the env colour', () => {
    for (const c of ENV_CASES) {
      const html = topBar(c.env)
      const tracks = find(html, (t) => css(t).background === 'var(--sage)')
      expect(tracks, c.env).toHaveLength(1)
      expect(css(tracks[0]).border, `${c.env} track border`).toBe(`1px solid ${c.track}`)
      expect(css(tracks[0])['border-radius']).toBe('var(--radius-btn)')
      expect(css(tracks[0]).gap).toBe('2px')
      expect(css(tracks[0]).padding).toBe('3px')

      const segs = find(html, (t) => t.startsWith('<button'))
      expect(segs, `${c.env} segments`).toHaveLength(2)
      segs.forEach((seg, i) => {
        const active = (c.env === 'sandbox') === (i === 0)
        expect(classes(seg), `${c.env} seg ${i} class`).toEqual([])
        expect(css(seg)['border-radius']).toBe('var(--radius-sm)')
        expect(css(seg).height).toBe('28px')
        expect(css(seg).padding).toBe('0 13px')
        expect(css(seg).transition, `${c.env} seg ${i} transition`).toContain('background var(--dur-fast) var(--ease-out)')
        expect(css(seg).background, `${c.env} seg ${i} bg`).toBe(active ? 'var(--primary)' : 'transparent')
        expect(css(seg).color, `${c.env} seg ${i} fg`).toBe(active ? 'var(--primary-foreground)' : 'var(--fg-3)')
      })

      const dots = find(html, (t) => css(t).width === '6px' && css(t).height === '6px')
      expect(dots.map((d) => css(d).background), `${c.env} dots`).toEqual([...c.dots])
      for (const d of dots) expect(css(d)['border-radius']).toBe('50%')

      const banners = find(html, (t) => css(t).padding === '7px 22px')
      expect(banners, `${c.env} banner`).toHaveLength(1)
      expect(css(banners[0]).background).toBe(c.banner)
    }
  })

  it('SH-11 the live banner text is --primary and the banner tag has no opacity', () => {
    const live = topBar('live')
    const msg = find(live, (t) => css(t)['font-size'] === '12.5px')
    expect(msg).toHaveLength(1)
    expect(css(msg[0]).color).toBe('var(--primary)')
    const sandbox = topBar('sandbox')
    expect(css(find(sandbox, (t) => css(t)['font-size'] === '12.5px')[0]).color).toBe('var(--status-amber-text)')
    for (const html of [live, sandbox]) {
      const tag = find(html, (t) => css(t)['margin-left'] === 'auto' && css(t)['font-size'] === '10px' && css(t)['letter-spacing'] === '0.05em')
      expect(tag).toHaveLength(1)
      expect(css(tag[0]).opacity).toBeUndefined()
      expect(html).not.toContain('oklch')
    }
  })

  const job = SEED_SUBMISSIONS[0]
  const jobDrawer = (j = job) =>
    render(JobDrawer, { job: j, env: 'sandbox', reqOpen: true, resOpen: true, onToggleReq: noop, onToggleRes: noop, onClose: noop, onReDrive: noop, onRePoll: noop, onCancel: noop })
  const evidenceDrawer = (i = 0) => render(EvidenceDrawer, { evidence: EVIDENCE_DATA[i], env: 'sandbox', onClose: noop, onCopy: noop, onDownload: noop })

  it('SH-12 both drawers sit over a 32% --surface scrim with no filter and a border-only panel', () => {
    expect(SEED_SUBMISSIONS.length).toBeGreaterThan(1)
    expect(EVIDENCE_DATA.length).toBeGreaterThan(0)
    const markups = [...SEED_SUBMISSIONS.map((j) => jobDrawer(j)), evidenceDrawer()]
    for (const html of markups) {
      const [scrim] = find(html, (t) => css(t)['z-index'] === '80')
      expect(scrim, 'scrim').toBeDefined()
      expect(css(scrim).background).toBe('color-mix(in srgb, var(--surface) 32%, transparent)')
      const [panel] = find(html, (t) => classes(t).includes('ops-drawer'))
      expect(panel, 'panel').toBeDefined()
      expect(css(panel)['border-left']).toBe('1px solid var(--line-2)')
      expect(css(panel).background).toBe('var(--bg-1)')
      expect(html).not.toMatch(/box-shadow|backdrop-filter/)
    }
  })

  it('SH-13 the job drawer keeps its v2 id, badge, toggle and action looks; every dot is a circle', () => {
    for (const j of SEED_SUBMISSIONS) {
      const html = jobDrawer(j)
      const [id] = find(html, (t) => css(t)['font-size'] === '15px' && css(t)['font-weight'] === '700')
      expect(css(id).color, `${j.id} id`).toBe('var(--ink)')
      const badges = find(html, (t) => css(t).padding === '2px 7px')
      expect(badges, `${j.id} state badge`).toHaveLength(1)
      expect(css(badges[0])['border-radius']).toBe('var(--radius-sm)')
      const dots = find(html, (t) => css(t).width === css(t).height && ['6px', '11px'].includes(css(t).width))
      expect(dots.length, `${j.id} dots`).toBeGreaterThanOrEqual(2)
      for (const d of dots) expect(css(d)['border-radius'], `${j.id} dot`).toBe('50%')
    }
    const html = jobDrawer()
    const toggles = find(html, (t) => css(t).color === 'var(--link)')
    expect(toggles).toHaveLength(2)
    const [cancel] = find(html, (t) => css(t).color === 'var(--status-red-text)' && t.startsWith('<button'))
    expect(css(cancel)['border-radius']).toBe('var(--radius-btn)')
    expect(css(cancel)['font-weight']).toBe('600')
    const close = find(html, (t) => css(t).width === '30px' && css(t).height === '30px')
    expect(close).toHaveLength(1)
    expect(css(close[0])['border-radius']).toBe('var(--radius-btn)')
  })

  it('SH-14 the evidence drawer keeps its v2 title, strip icon, IRN link and QR tile looks', () => {
    const html = evidenceDrawer()
    const [title] = find(html, (t) => css(t)['font-size'] === '15px' && css(t)['font-weight'] === '700')
    expect(css(title).color).toBe('var(--ink)')
    const icons = find(html, (t) => css(t).color === 'var(--status-green-text)' && css(t).display === 'inline-flex')
    expect(icons).toHaveLength(1)
    const irn = find(html, (t) => css(t).color === 'var(--link)')
    expect(irn).toHaveLength(1)
    const qr = find(html, (t) => css(t).width === '92px' && css(t).height === '92px')
    expect(qr).toHaveLength(1)
    expect(css(qr[0])['border-radius']).toBe('var(--radius-md)')
    expect(css(qr[0]).background).toBe('var(--surface)')
  })

  it('SH-15 the rotate modal is a 440px --radius-lg card with a 18/700 heading and a --radius-md warning, shadowless', () => {
    for (const env of ['LIVE', 'SANDBOX']) {
      const html = render(RotateConfirm, { env, onClose: noop, onConfirm: noop })
      const [scrim] = find(html, (t) => css(t)['z-index'] === '90')
      expect(scrim, env).toBeDefined()
      const [h3] = find(html, (t) => t.startsWith('<h3'))
      expect([css(h3)['font-size'], css(h3)['font-weight'], css(h3)['letter-spacing']]).toEqual(['18px', '700', '-0.03em'])
      expect(html).toContain(`Rotate ${env} key?`)
      const warn = find(html, (t) => css(t).background === 'var(--status-amber-bg)' && css(t).padding === '10px 12px')
      expect(warn).toHaveLength(1)
      expect(css(warn[0])['border-radius']).toBe('var(--radius-md)')
      expect(html).not.toContain('box-shadow')
    }
  })

  it('SH-16 the toast keeps the D-6 colours, radius and tag rule for both tones', () => {
    const tones: ToastTone[] = ['ok', 'red']
    for (const tone of tones) {
      const html = render(Toast, { toast: { msg: 'Rotated', tag: 'NEW KEY', tone } })
      const [root = ''] = openTags(html)
      expect(css(root).color).toBe('var(--surface-foreground)')
      expect(css(root)['border-radius']).toBe('var(--radius-md)')
      expect(css(root)['z-index']).toBe('95')
      const tags = find(html, (t) => classes(t).includes('mono'))
      expect(tags, tone).toHaveLength(1)
      expect(css(tags[0]).color).toBe('var(--surface-body)')
      expect(css(tags[0])['border-left']).toBe('1px solid var(--surface-panel-border)')
      expect(html).toContain('>Rotated<')
      expect(html).toContain('>NEW KEY<')
      expect(find(render(Toast, { toast: { msg: 'm', tag: '', tone } }), (t) => classes(t).includes('mono'))).toEqual([])
    }
  })

  it('SH-17 the six components render no oklch, radius-pill or 99/999 radius', () => {
    const states = [
      ...SCREENS.map((s) => sidebar(s, 3)),
      sidebar('overview', 3, true),
      topBar('sandbox'),
      topBar('live'),
      ...SEED_SUBMISSIONS.map((j) => jobDrawer(j)),
      ...EVIDENCE_DATA.map((_, i) => evidenceDrawer(i)),
      render(RotateConfirm, { env: 'LIVE', onClose: noop, onConfirm: noop }),
      render(Toast, { toast: { msg: 'm', tag: 't', tone: 'ok' } }),
      render(Toast, { toast: { msg: 'm', tag: 't', tone: 'red' } }),
    ]
    expect(states.length).toBeGreaterThan(10)
    const all = states.join('\n')
    // control needles: the scan population holds the tokens the ban sits beside
    expect(all.match(/border-radius:50%/g)?.length ?? 0).toBeGreaterThan(10)
    expect(all).toContain('border-radius:var(--radius-sm)')
    expect(all).not.toMatch(/oklch|radius-pill|border-radius:\s*(99|999)(px)?\s*(;|")/)
  })

  it('SH-18 the env segments, rotate and drawer buttons keep their handlers', () => {
    const calls: string[] = []
    const bar = TopBar({ screen: 'overview', env: 'sandbox', onSetEnv: (e) => calls.push(`env:${e}`) })
    button(bar, 'SANDBOX').props.onClick?.()
    button(bar, 'LIVE').props.onClick?.()

    const rot = RotateConfirm({ env: 'LIVE', onClose: () => calls.push('rot:close'), onConfirm: () => calls.push('rot:confirm') })
    button(rot, 'Cancel').props.onClick?.()
    button(rot, 'Rotate key').props.onClick?.()
    rot.props.onClick?.()
    const stop = vi.fn()
    const panel = walk(rot.props.children).find((e) => e.type === 'div' && typeof e.props.onClick === 'function')
    expect(panel, 'panel').toBeDefined()
    panel!.props.onClick?.({ stopPropagation: stop })

    const jd = JobDrawer({
      job, env: 'sandbox', reqOpen: false, resOpen: false,
      onToggleReq: () => calls.push('job:req'), onToggleRes: () => calls.push('job:res'), onClose: () => calls.push('job:close'),
      onReDrive: () => calls.push('job:redrive'), onRePoll: () => calls.push('job:repoll'), onCancel: () => calls.push('job:cancel'),
    })
    for (const label of ['Re-drive', 'Re-poll status', 'Cancel']) button(jd, label).props.onClick?.()
    expect(buttons(jd).filter((b) => textOf(b.props.children).trim() === 'EXPAND')).toHaveLength(2)
    buttons(jd).filter((b) => textOf(b.props.children).trim() === 'EXPAND').forEach((b) => b.props.onClick?.())
    walk(jd).find((e) => e.type === 'div' && typeof e.props.onClick === 'function')!.props.onClick?.()

    const ev = EvidenceDrawer({ evidence: EVIDENCE_DATA[0], env: 'sandbox', onClose: () => calls.push('ev:close'), onCopy: () => calls.push('ev:copy'), onDownload: () => calls.push('ev:download') })
    button(ev, 'Copy JSON').props.onClick?.()
    button(ev, 'Download bundle').props.onClick?.()
    walk(ev).find((e) => e.type === 'div' && typeof e.props.onClick === 'function')!.props.onClick?.()

    expect(calls).toEqual([
      'env:sandbox', 'env:live',
      'rot:close', 'rot:confirm', 'rot:close',
      'job:redrive', 'job:repoll', 'job:cancel', 'job:req', 'job:res', 'job:close',
      'ev:copy', 'ev:download', 'ev:close',
    ])
    expect(stop).toHaveBeenCalledTimes(1)
  })
})
