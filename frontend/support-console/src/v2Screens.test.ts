import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { Audit } from './components/Audit'
import { Health } from './components/Health'
import { Rules } from './components/Rules'
import { Submissions } from './components/Submissions'
import { Tenants } from './components/Tenants'
import { AUDIT_ENTRIES, AUDIT_FILTERS, JOB_FILTERS, LEARNED_RULES, RECON_ROWS, RULE_SET_VERSIONS, SEED_JOBS, SEED_RULES, TENANTS, healthCards } from './data'
import type { AuditFilter, Job } from './types'

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
const audit = (query = '', filter: AuditFilter = 'all') =>
  renderToStaticMarkup(createElement(Audit, { query, filter, onQueryChange: noop, onFilterChange: noop, onOpen: noop }))
const tenants = (query = '', tenantId = 't1') =>
  renderToStaticMarkup(createElement(Tenants, { query, tenantId, onQueryChange: noop, onSelect: noop, onViewJobs: noop, onViewAs: noop }))
const healthHtml = (deadLetterCount: number) => renderToStaticMarkup(createElement(Health, { deadLetterCount }))
const submissionsFiltered = (filter: Parameters<typeof Submissions>[0]['filter'], jobs: Job[] = SEED_JOBS) =>
  renderToStaticMarkup(
    createElement(Submissions, { jobs, filter, subTab: 'jobs', onFilterChange: noop, onSubTabChange: noop, onOpenJob: noop, onReDriveAll: noop, onReconcile: noop, onRunSweep: noop }),
  )

const RULES_PROPS: Parameters<typeof Rules>[0] = {
  rules: SEED_RULES,
  status: 'ready',
  version: 4,
  busy: false,
  onRetry: noop,
  onOpenRule: noop,
  onToggleRule: noop,
  onPublish: noop,
  onPromote: noop,
}
const rulesWith = (over: Partial<Parameters<typeof Rules>[0]>) => renderToStaticMarkup(createElement(Rules, { ...RULES_PROPS, ...over }))

const SCREENS = {
  submissions: submissions('jobs'),
  rules: renderToStaticMarkup(createElement(Rules, { ...RULES_PROPS })),
  audit: audit(),
  tenants: tenants(),
  health: healthHtml(2),
}
const RECON = submissions('recon')
type Screen = keyof typeof SCREENS
const SCREEN_KEYS = Object.keys(SCREENS) as Screen[]
const tagsOf = (screen: Screen) => {
  const ts = parse(SCREENS[screen])
  expect(ts.length, `${screen}: markup parsed`).toBeGreaterThan(10)
  return ts
}
const parsed = (html: string, at: string) => {
  const ts = parse(html)
  expect(ts.length, `${at}: markup parsed`).toBeGreaterThan(10)
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

    for (const t of TENANTS) {
      const ts = parsed(tenants('', t.id), `tenant ${t.id}`)
      expect(t.kpis, `${t.id}: KPIs`).toHaveLength(4)
      for (const k of t.kpis) expectFigure(figureBefore(ts, k.label), '20px', `${t.id} KPI ${k.label}`)
      const h2 = ts.filter((x) => x.name === 'h2')
      expect(h2, `${t.id}: one h2`).toHaveLength(1)
      expect.soft([h2[0].text, style(h2[0])['font-size']], `${t.id}: h2 name and size`).toEqual([t.name, '19px'])
      expect.soft(Object.keys(style(h2[0])).filter((k) => k === 'font-weight' || k === 'letter-spacing'), `${t.id}: h2 inline weight and tracking`).toEqual([])
    }
    for (const n of [0, 1, 37]) {
      const figs = parsed(healthHtml(n), `health ${n}`).filter((t) => style(t)['font-size'] === '30px')
      expect(figs, `health ${n}: figures`).toHaveLength(6)
      figs.forEach((t, i) => expectFigure(t, '30px', `health ${n} figure #${i}`))
      expect(figs[healthCards(n).findIndex((c) => c.label === 'Dead-letter')].text, `health ${n}: the Dead-letter figure reads the count`).toBe(String(n))
    }

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
    const inForce = rules.filter((t) => t.text.startsWith('IN FORCE'))
    expect(inForce, 'IN FORCE tag').toHaveLength(1)
    expect.soft(style(inForce[0])['border-radius'], 'IN FORCE corner').toBe('var(--radius-sm)')

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
    // The active chip is read too: every filter value, every chip.
    for (const k of JOB_FILTERS) {
      const cs = withClass(parsed(submissionsFiltered(k), `submissions ${k}`), 'ops-chip')
      expect(cs, `${k}: chips`).toHaveLength(JOB_FILTERS.length)
      expect(cs.filter((c) => attr(c, 'aria-pressed') === 'true'), `${k}: one active chip`).toHaveLength(1)
      for (const c of cs) expect.soft(style(c)['border-radius'], `${k}: chip corner`).toBe('var(--radius-sm)')
    }
    for (const f of AUDIT_FILTERS) {
      const cs = withClass(parsed(audit('', f.key), `audit ${f.key}`), 'ops-chip')
      expect(cs, `${f.key}: audit chips`).toHaveLength(AUDIT_FILTERS.length)
      expect(cs.filter((c) => attr(c, 'aria-pressed') === 'true'), `${f.key}: one active audit chip`).toHaveLength(1)
      for (const c of cs) expect.soft(style(c)['border-radius'], `${f.key}: audit chip corner`).toBe('var(--radius-sm)')
    }

    // The remaining chips and badges of the Shared rules table.
    const typeChips = rules.filter((t) => classes(t).includes('mono') && style(t)['justify-self'] === 'start')
    expect(typeChips, 'rule type chips').toHaveLength(SEED_RULES.length)
    for (const c of typeChips) expect.soft(style(c)['border-radius'], `type chip ${c.text} corner`).toBe('var(--radius-sm)')
    const aud = tagsOf('audit')
    const appendOnly = aud.filter((t) => style(t).background === 'var(--status-muted-bg)')
    expect(appendOnly, 'APPEND-ONLY badge').toHaveLength(1)
    expect.soft([style(appendOnly[0])['border-radius'], style(appendOnly[0]).color], 'APPEND-ONLY corner and colour (D-16)').toEqual(['var(--radius-sm)', 'var(--fg-2)'])
    const glyphTiles = aud.filter((t) => style(t).width === '22px' && style(t).display === 'inline-flex')
    expect(glyphTiles, 'audit glyph tiles').toHaveLength(AUDIT_ENTRIES.length)
    for (const g of glyphTiles) expect.soft(style(g)['border-radius'], 'audit glyph tile corner').toBe('var(--radius-sm)')
    for (const t of TENANTS) {
      const roleTags = parsed(tenants('', t.id), `tenant ${t.id}`).filter((x) => classes(x).includes('mono') && x.attrs.includes('border-radius:var(--radius-sm)') && t.members.some((m) => m.role.toUpperCase() === x.text))
      expect(roleTags, `${t.id}: role tags`).toHaveLength(t.members.length)
    }

    // Cards, tables and tiles: v2 corner, no shadow, on every screen and tab.
    const surfaces: [string, string][] = [...SCREEN_KEYS.map((k) => [k, SCREENS[k]] as [string, string]), ['recon', RECON], ['tenant t3', tenants('', 't3')], ['health clear', healthHtml(0)]]
    const floors: Record<string, number> = { submissions: 5, rules: 3, audit: 1, tenants: 2, health: 6, recon: 4 }
    for (const [name, html] of surfaces) {
      const ts = parsed(html, name)
      const cards = ts.filter((t) => style(t).border === '1px solid var(--line-1)' && style(t).background === 'var(--bg-2)' && !classes(t).includes('ops-chip'))
      expect(cards.length, `${name}: cards found`).toBeGreaterThanOrEqual(floors[name.split(' ')[0]] ?? 1)
      for (const c of cards) expect.soft(style(c)['border-radius'], `${name}: card corner`).toBe('var(--radius-md)')
      expect.soft(ts.filter((t) => 'box-shadow' in style(t)).map((t) => t.name), `${name}: no inline shadow`).toEqual([])
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

    for (const t of TENANTS) {
      const navs = buttonsOf(tenants('', t.id)).filter((b) => classes(b).includes('ops-nav'))
      const pressed = navs.filter((b) => attr(b, 'aria-pressed') === 'true')
      expect(pressed, `${t.id}: one selected row`).toHaveLength(1)
      expect.soft([pressed[0].text.includes(t.name), style(pressed[0]).background], `${t.id}: the selected row and its fill`).toEqual([true, 'var(--bg-3)'])
    }

    for (const screen of ['audit', 'tenants'] as const) {
      const fields = withClass(tagsOf(screen), 'ops-input')
      expect(fields, `${screen}: one search wrapper`).toHaveLength(1)
      expect.soft(classes(fields[0]), `${screen}: search wrapper classes`).toEqual(['ops-input', 'ops-field'])
    }
    const noDeadLetter = SEED_JOBS.filter((j) => j.state !== 'dead-letter')
    expect(noDeadLetter.length, 'jobs without a dead-letter').toBeGreaterThan(0)
    expect(buttonsOf(submissionsFiltered('all', noDeadLetter)).some((b) => b.text === 'Re-drive all'), 'no dead-letter, no Re-drive all').toBe(false)
    expect(buttonsOf(SCREENS.submissions).some((b) => b.text === 'Re-drive all'), 'control: dead-letter jobs show Re-drive all').toBe(true)

    // Icon wrappers draw inline-flex, as the prototype does (D-16).
    for (const screen of ['audit', 'tenants'] as const) {
      const ts = tagsOf(screen)
      const at = ts.indexOf(withClass(ts, 'ops-input')[0])
      expect.soft([ts[at + 1].name, style(ts[at + 1]).display, style(ts[at + 1]).color, ts[at + 2].name], `${screen}: search icon wrapper`).toEqual(['span', 'inline-flex', 'var(--fg-3)', 'svg'])
    }
    const rulesTs = tagsOf('rules')
    const sparks = rulesTs.filter((t, i) => style(t).color === 'var(--action)' && rulesTs[i + 1]?.name === 'svg')
    expect(sparks, 'learned-rules spark wrapper').toHaveLength(1)
    expect.soft(style(sparks[0]).display, 'spark wrapper').toBe('inline-flex')
    for (const [screen, count] of [['submissions', SEED_JOBS.length], ['audit', AUDIT_ENTRIES.length]] as const) {
      const ts = tagsOf(screen)
      const chevrons = ts.filter((t, i) => style(t).color === 'var(--fg-4)' && ts[i + 1]?.name === 'svg')
      expect(chevrons, `${screen}: row chevrons`).toHaveLength(count)
      for (const c of chevrons) expect.soft(style(c).display, `${screen}: chevron wrapper`).toBe('inline-flex')
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
    const none = SEED_JOBS.filter((j) => j.state !== 'dead-letter')
    const filtered: [string, string][] = [
      [submissionsFiltered('dead-letter', none), 'No jobs in this state.'],
      [audit('zz-no-match', 'rule'), 'No audit entries match this filter.'],
    ]
    for (const [html, text] of filtered) expect.soft(style(withText(parse(html), text)).color, `"${text}" under a filter`).toBe('var(--fg-3)')
    expect(parse(SCREENS.submissions).some((t) => t.text === 'No jobs in this state.'), 'control: the seed jobs show no empty state').toBe(false)
    expect(parse(SCREENS.tenants).some((t) => t.text === 'No tenant matches.'), 'control: the seed tenants show no empty state').toBe(false)
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
    // Zero dead-letter jobs: the tile still holds exactly one .money, reading 0.
    const zero = parsed(submissionsFiltered('all', SEED_JOBS.filter((j) => j.state !== 'dead-letter')), 'no dead-letter')
    const zeroAt = zero.indexOf(withText(zero, 'Dead-letter'))
    const zeroTile = zero.slice(zeroAt - 2, zeroAt + 1)
    expect(withClass(zeroTile, 'money'), 'zero: one .money in the tile').toHaveLength(1)
    expect(zeroTile[1].text, 'zero: the figure reads 0').toBe('0')
  })

  it('SC-07 the recon title, header and every row hold the 800px floor; the jobs table keeps 1040px', () => {
    const floor = (html: string, cols: string, at: string) => {
      const rows = parsed(html, at).filter((t) => style(t)['grid-template-columns']?.startsWith(cols))
      expect(rows.length, `${at}: rows found`).toBeGreaterThan(0)
      return rows
    }
    const recon = floor(RECON, '140px minmax(120px,1fr)', 'recon')
    expect(recon, 'recon: the header row and one row per mismatch').toHaveLength(RECON_ROWS.length + 1)
    for (const r of recon) expect.soft(style(r)['min-width'], 'recon row floor').toBe('800px')

    const ts = reconTags()
    const titleRow = ts[ts.indexOf(withText(ts, 'State mismatches · internal vs APP')) - 1]
    expect.soft(style(titleRow)['min-width'], 'recon title row floor').toBe('800px')

    const jobs = floor(SCREENS.submissions, '150px minmax(220px,1.3fr)', 'jobs')
    expect(jobs, 'jobs: the header row and one row per job').toHaveLength(SEED_JOBS.length + 1)
    for (const r of jobs) expect.soft(style(r)['min-width'], 'jobs row floor').toBe('1040px')
  })

  it('Rules renders its non-ready states', () => {
    const forbidden = rulesWith({ status: 'forbidden', rules: [], version: null })
    expect(forbidden).toContain('Your account has no rules role.')
    expect(forbidden).not.toContain('role="switch"')
    expect(forbidden).toContain('Rules admin')
    const loading = rulesWith({ status: 'loading', rules: [], version: null })
    expect(loading).toContain('Loading rules…')
    expect(loading).toContain('Rules admin')
    const failed = rulesWith({ status: 'error', errorText: 'no rule set in force', rules: [], version: null })
    expect(failed).toContain('no rule set in force')
    expect(buttonByText(failed, 'Retry').attrs).toContain('v2-btn-ghost')
  })

  it('Rules renders the version badge and disables switches while busy', () => {
    const html = rulesWith({ busy: true })
    expect(html).toContain('IN FORCE v4')
    const switches = [...html.matchAll(/<button\b([^>]*role="switch"[^>]*)>/g)].map((m) => ` ${m[1]}`)
    expect(switches).toHaveLength(SEED_RULES.length)
    for (const t of switches) {
      expect(t).toContain('disabled=""')
      expect(style({ attrs: t })).toMatchObject({ opacity: '0.45', cursor: 'not-allowed' })
    }
    expect(html).not.toContain('--fg-4')
    expect(rulesWith({ busy: false })).not.toContain('disabled=""')
  })
})
