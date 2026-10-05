import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { Audit } from './components/Audit'
import { Health } from './components/Health'
import { Rules } from './components/Rules'
import { Submissions } from './components/Submissions'
import { Tenants } from './components/Tenants'
import { AUDIT_ENTRIES, AUDIT_FILTERS, JOB_FILTERS, LEARNED_RULES, RECON_ROWS, RULE_SET_VERSIONS, SEED_JOBS, SEED_RULES, TENANTS } from './data'
import type { Job } from './types'

// SSR markup is the oracle for inline values; the resolved look is SUP-03 (deploy gate).
const noop = () => {}

type Tag = { name: string; attrs: string; text: string }
const attr = (t: { attrs: string }, name: string) => t.attrs.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? ''
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
    text: decode(m[2].replace(/<[^>]*>/g, '')).trim(),
  }))
const buttonsByText = (html: string, text: string) => {
  const hit = buttonsOf(html).filter((b) => b.text === text)
  expect(hit.length, `at least one button "${text}"`).toBeGreaterThan(0)
  return hit
}
const buttonByText = (html: string, text: string) => {
  const hit = buttonsByText(html, text)
  expect(hit, `exactly one button "${text}"`).toHaveLength(1)
  return hit[0]
}
// The tag before a label tag holds the figure of its tile.
const figureBefore = (ts: Tag[], label: string) => ts[ts.indexOf(withText(ts, label)) - 1]

const submissions = (subTab: 'jobs' | 'recon', jobs: Job[] = SEED_JOBS) =>
  renderToStaticMarkup(
    createElement(Submissions, { jobs, filter: 'all', subTab, onFilterChange: noop, onSubTabChange: noop, onOpenJob: noop, onReDriveAll: noop, onReconcile: noop, onRunSweep: noop }),
  )
const audit = (query = '') =>
  renderToStaticMarkup(createElement(Audit, { query, filter: 'all', onQueryChange: noop, onFilterChange: noop, onOpen: noop }))
const tenants = (query = '') =>
  renderToStaticMarkup(createElement(Tenants, { query, tenantId: 't1', onQueryChange: noop, onSelect: noop, onViewJobs: noop, onViewAs: noop }))

const SCREENS = {
  submissions: submissions('jobs'),
  rules: renderToStaticMarkup(createElement(Rules, { rules: SEED_RULES, onOpenRule: noop, onToggleRule: noop, onPublish: noop, onPromote: noop })),
  audit: audit(),
  tenants: tenants(),
  health: renderToStaticMarkup(createElement(Health, { deadLetterCount: 2 })),
}
const RECON = submissions('recon')
type Screen = keyof typeof SCREENS
const SCREEN_KEYS = Object.keys(SCREENS) as Screen[]
const tagsOf = (screen: Screen) => {
  const ts = parse(SCREENS[screen])
  expect(ts.length, `${screen}: markup parsed`).toBeGreaterThan(10)
  return ts
}
const reconTags = () => {
  const ts = parse(RECON)
  expect(ts.length, 'recon: markup parsed').toBeGreaterThan(10)
  return ts
}

const expectFigure = (t: Tag, size: string, at: string) => {
  const s = style(t)
  expect.soft(classes(t), `${at}: class list`).toContain('money')
  expect.soft(classes(t), `${at}: no mono`).not.toContain('mono')
  expect.soft([s['font-size'], s['font-weight']], `${at}: size and weight`).toEqual([size, '700'])
  expect.soft(Object.keys(s), `${at}: the .money class sets the tracking`).not.toContain('letter-spacing')
}

describe('v2 screens', () => {
  it('SC-01 every screen opens on a 20 / 8 clearance header with a 24px h1 and no inline weight', () => {
    for (const screen of SCREEN_KEYS) {
      const ts = tagsOf(screen)
      expect(classes(ts[0]), `${screen}: root`).toContain('ops-screen-pad')
      expect.soft(style(ts[1])['margin-bottom'], `${screen}: header row marginBottom`).toBe('20px')

      const h1s = ts.filter((t) => t.name === 'h1')
      expect(h1s, `${screen}: one h1`).toHaveLength(1)
      const at = ts.indexOf(h1s[0])
      expect(at, `${screen}: the h1 follows the eyebrow`).toBeGreaterThan(1)
      expect.soft(classes(ts[at - 1]), `${screen}: tag before the h1`).toContain('eyebrow')
      expect.soft(style(ts[at - 1])['margin-bottom'], `${screen}: eyebrow marginBottom`).toBe('8px')

      const h1 = style(h1s[0])
      expect.soft([h1['font-size'], h1.margin], `${screen}: h1 size and margin`).toEqual(['24px', '0'])
      expect.soft(Object.keys(h1), `${screen}: h1 inline weight and tracking (the layer sets them)`).not.toContain('font-weight')
      expect.soft(Object.keys(h1), `${screen}: h1 inline tracking`).not.toContain('letter-spacing')
    }

    const h2s = tagsOf('tenants').filter((t) => t.name === 'h2')
    expect(h2s, 'tenant detail h2').toHaveLength(1)
    expect(h2s[0].text).toBe(TENANTS[0].name)
    expect.soft(style(h2s[0])['font-size'], 'h2 size').toBe('19px')
    expect.soft(Object.keys(style(h2s[0])), 'h2 inline weight and tracking').not.toContain('font-weight')
    expect.soft(Object.keys(style(h2s[0])), 'h2 inline tracking').not.toContain('letter-spacing')
  })

  it('SC-02 titles and figures', () => {
    const titles: [string, Tag][] = [
      ['Rules', withText(tagsOf('rules'), 'Rules')],
      ['State mismatches', withText(reconTags(), 'State mismatches · internal vs APP')],
    ]
    for (const [name, t] of titles) {
      const s = style(t)
      expect.soft(s['font-family'], `${name}: family`).toBe('var(--font-display)')
      expect.soft(s['font-weight'], `${name}: weight`).toBe('700')
      expect.soft(s['letter-spacing'], `${name}: tracking`).toBe('var(--tracking-card)')
    }

    const subStats = tagsOf('submissions')
    for (const label of ['In flight', 'Accepted 24h', 'Rejected', 'Dead-letter']) expectFigure(figureBefore(subStats, label), '20px', `sub-stat ${label}`)

    const kpis = tagsOf('tenants')
    expect(TENANTS[0].kpis, 'tenant KPIs').toHaveLength(4)
    for (const k of TENANTS[0].kpis) expectFigure(figureBefore(kpis, k.label), '20px', `KPI ${k.label}`)

    const health = tagsOf('health').filter((t) => style(t)['font-size'] === '30px')
    expect(health, 'health figures').toHaveLength(6)
    health.forEach((t, i) => expectFigure(t, '30px', `health figure #${i}`))
    expectFigure(withText(reconTags(), '82'), '30px', 'rate figure')

    const tins = tagsOf('tenants').filter((t) => TENANTS.some((x) => x.tin === t.text))
    expect(tins.length, 'tenant TIN spans').toBeGreaterThanOrEqual(TENANTS.length)
    for (const t of tins) expect.soft(classes(t), `TIN ${t.text} keeps mono`).toContain('mono')
    const ids = subStats.filter((t) => SEED_JOBS.some((j) => j.id === t.text))
    expect(ids, 'one job ID span per job').toHaveLength(SEED_JOBS.length)
    for (const t of ids) expect.soft(classes(t), `job ID ${t.text} keeps mono`).toContain('mono')
  })

  it('SC-03 screen corners', () => {
    const sub = tagsOf('submissions')
    const chips = [...withClass(sub, 'ops-chip'), ...withClass(tagsOf('audit'), 'ops-chip')]
    expect(withClass(sub, 'ops-chip'), 'Submissions chips').toHaveLength(JOB_FILTERS.length)
    expect(withClass(tagsOf('audit'), 'ops-chip'), 'Audit chips').toHaveLength(AUDIT_FILTERS.length)
    expect(chips.every((c) => c.name === 'button')).toBe(true)
    for (const c of chips) expect.soft(style(c)['border-radius'], 'chip corner').toBe('var(--radius-sm)')

    const rules = tagsOf('rules')
    const pills = rules.flatMap((t, i) => (['DRAFT', 'ACTIVE', 'ARCHIVED'].includes(t.text) && classes(t).includes('mono') ? [rules[i - 1]] : []))
    expect(pills, 'version tags').toHaveLength(RULE_SET_VERSIONS.length)
    for (const p of pills) expect.soft(style(p)['border-radius'], 'version tag corner').toBe('var(--radius-sm)')
    const learned = rules.filter((t) => t.text === String(LEARNED_RULES.length) && style(t)['margin-left'] === 'auto')
    expect(learned, 'learned count').toHaveLength(1)
    expect.soft(style(learned[0])['border-radius'], 'learned count corner').toBe('var(--radius-sm)')
    const draft = rules.filter((t) => t.text.startsWith('EDITING DRAFT'))
    expect(draft, 'EDITING DRAFT tag').toHaveLength(1)
    expect.soft(style(draft[0])['border-radius'], 'EDITING DRAFT corner').toBe('var(--radius-sm)')

    const toggles = withClass(rules, 'ops-toggle')
    const knobs = withClass(rules, 'ops-knob')
    expect(toggles, 'one toggle per rule').toHaveLength(SEED_RULES.length)
    expect(knobs, 'one knob per rule').toHaveLength(SEED_RULES.length)
    for (const t of toggles) expect.soft(style(t)['border-radius'], 'toggle track keeps 99').toBe('99px')
    for (const k of knobs) {
      expect.soft(style(k)['border-radius'], 'knob is a circle').toBe('50%')
      expect.soft(Object.keys(style(k)), 'knob has no shadow').not.toContain('box-shadow')
    }

    const avatars = [...tagsOf('audit'), ...tagsOf('tenants')].filter((t) => style(t).background === 'var(--slate-800)')
    expect(avatars, 'audit and member avatars').toHaveLength(AUDIT_ENTRIES.length + TENANTS[0].members.length)
    for (const a of avatars) expect.soft(style(a)['border-radius'], 'avatar corner').toBe('50%')
    const dots = [...tagsOf('tenants'), ...tagsOf('health')].filter((t) => style(t).width === '7px' && style(t).height === '7px')
    expect(dots, 'tenant and health status dots').toHaveLength(TENANTS.length + 6)
    for (const d of dots) expect.soft(style(d)['border-radius'], 'status dot corner').toBe('50%')

    const ten = tagsOf('tenants')
    const rowTiles = ten.filter((t) => style(t).width === '30px' && style(t).height === '30px')
    expect(rowTiles, 'tenant row initials tiles').toHaveLength(TENANTS.length)
    const detailTiles = ten.filter((t) => style(t).width === '48px' && style(t).height === '48px')
    expect(detailTiles, 'tenant detail initials tile').toHaveLength(1)
    for (const t of [...rowTiles, ...detailTiles]) expect.soft(style(t)['border-radius'], 'initials tile corner').toBe('var(--radius-md)')

    const tracks = reconTags().flatMap((t, i, all) => (style(t).height === '6px' && style(t).overflow === 'hidden' ? [[t, all[i + 1]]] : []))
    expect(tracks, 'meter track and fill').toHaveLength(1)
    for (const [track, fill] of tracks) {
      expect.soft(style(track)['border-radius'], 'meter track corner').toBe('2px')
      expect.soft([style(fill).width, style(fill)['border-radius']], 'meter fill (the next tag) width and corner').toEqual(['82%', '2px'])
    }
  })

  it('SC-04 screen buttons, rows and fields', () => {
    const ten = SCREENS.tenants
    expect.soft(classes(buttonByText(ten, 'View jobs')), 'View jobs classes').toEqual(['ops-btn', 'v2-btn', 'v2-btn-ghost'])
    expect.soft(classes(buttonByText(ten, 'View-as (read-only)')), 'View-as classes').toEqual(['ops-btn', 'v2-btn', 'v2-btn-primary'])
    for (const text of ['View jobs', 'View-as (read-only)']) {
      expect.soft(Object.keys(style(buttonByText(ten, text))), `${text}: the v2-btn class owns the corner`).not.toContain('border-radius')
    }
    expect.soft(classes(buttonByText(RECON, 'Run sweep now')), 'Run sweep now classes').toEqual(['ops-btn', 'v2-btn', 'v2-btn-ghost'])

    const redrive = style(buttonByText(SCREENS.submissions, 'Re-drive all'))
    expect.soft([redrive.color, redrive['border-radius']], 'Re-drive all').toEqual(['var(--primary-foreground)', 'var(--radius-btn)'])

    const reconcile = buttonsByText(RECON, 'Reconcile')
    expect(reconcile, 'one Reconcile per mismatch').toHaveLength(RECON_ROWS.length)
    const promote = buttonsByText(SCREENS.rules, 'Promote to draft')
    expect(promote, 'one Promote per learned rule').toHaveLength(LEARNED_RULES.length)
    for (const b of [...reconcile, ...promote]) {
      const s = style(b)
      expect.soft([s.border, s['border-radius']], `${b.text}: outline border and corner`).toEqual(['1px solid var(--button-outline-border)', 'var(--radius-btn)'])
    }

    for (const [html, text] of [[SCREENS.submissions, 'Jobs'], [RECON, 'Reconciliation']] as const) {
      expect.soft(classes(buttonByText(html, text)), `sub-tab ${text}`).toContain('ops-tab')
    }

    const rows = buttonsOf(ten).filter((b) => classes(b).includes('ops-nav'))
    expect(rows, 'tenant row buttons').toHaveLength(TENANTS.length)
    for (const b of rows) {
      const s = style(b)
      expect.soft([s['font-family'], s.color], 'tenant row font and colour').toEqual(['var(--font-sans)', 'var(--fg-1)'])
    }

    for (const screen of ['audit', 'tenants'] as const) {
      const fields = withClass(tagsOf(screen), 'ops-input')
      expect(fields, `${screen}: one search wrapper`).toHaveLength(1)
      expect.soft(classes(fields[0]), `${screen}: search wrapper classes`).toEqual(['ops-input', 'ops-field'])
    }
  })

  it('SC-05 enabled text meets Q3', () => {
    const empty: [string, string][] = [
      [submissions('jobs', []), 'No jobs in this state.'],
      [audit('zz-no-match'), 'No audit entries match this filter.'],
      [tenants('zz-no-match'), 'No tenant matches.'],
    ]
    for (const [html, text] of empty) {
      expect.soft(style(withText(parse(html), text)).color, `"${text}" colour`).toBe('var(--fg-3)')
    }

    const ts = tagsOf('submissions')
    const counts = ts.flatMap((t, i) => (classes(t).includes('ops-chip') ? [ts[i + 1]] : []))
    expect(counts, 'one count span per chip').toHaveLength(JOB_FILTERS.length)
    for (const c of counts) expect.soft(Object.keys(style(c)), `chip count ${c.text}: opacity`).not.toContain('opacity')
  })

  it('SC-06 the smoke locators keep their classes', () => {
    const sub = tagsOf('submissions')
    expect(withClass(sub, 'ops-sub-stats'), 'sub-stats row').toHaveLength(1)
    expect(withClass(sub, 'ops-chip'), 'Submissions chips').toHaveLength(8)
    expect(withClass(sub, 'ops-row').length, 'job rows').toBeGreaterThanOrEqual(1)
    expect(withClass(tagsOf('health'), 'ops-health-grid'), 'health grid').toHaveLength(1)
    const rows = buttonsOf(SCREENS.tenants).filter((b) => classes(b).includes('ops-nav'))
    expect(rows.length, 'tenant rows').toBeGreaterThanOrEqual(1)
    const tins = tagsOf('tenants').filter((t) => t.text === TENANTS[0].tin)
    expect(tins.length, 'TIN spans').toBeGreaterThanOrEqual(1)
    for (const t of tins) expect.soft(classes(t), 'TIN span is mono').toContain('mono')

    // The smoke spec reads `.ops-sub-stats > div` filtered by "Dead-letter", then `.money`, in strict mode.
    const label = sub.indexOf(withText(sub, 'Dead-letter'))
    const tile = sub.slice(label - 2, label + 1)
    expect(tile.map((t) => t.name), 'tile, figure, label').toEqual(['div', 'div', 'div'])
    expect(withClass(tile, 'money'), 'the Dead-letter tile holds exactly one .money').toHaveLength(1)
    expect(classes(tile[1]), 'the figure is the .money').toContain('money')
  })
})
