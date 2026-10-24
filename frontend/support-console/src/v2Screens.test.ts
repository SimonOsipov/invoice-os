import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { readFileSync } from 'node:fs'

import { AiUsage } from './components/AiUsage'
import { Health } from './components/Health'
import { Rules } from './components/Rules'
import { TimeChart } from './components/TimeChart'
import { Tenants } from './components/Tenants'
import type { AiCall } from './aiUsageApi'
import type { FleetHealth, JobTotals, JobView, ServiceHealth, StaffHealth, TrendPoint } from './healthApi'
import { sentryServiceUrl } from './helpers'
import { DASHBOARDS, METRIC_SERVICES, type PanelKey, type Point, type StaffMetrics } from './metricsApi'
import { chipStyle } from './components/Submissions'
import { SEED_RULES } from './rulesFixture'
import type { RuleVersion } from './rulesApi'
import type { StaffInvoice, StaffMember, StaffTenant, StaffTenantDetail } from './tenantsApi'
import type { CodeListLatest } from './types'

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

const ver = (version: number, state: RuleVersion['state'], effective_from: string | null = '2026-01-01'): RuleVersion => ({ rule_set_version_id: `id${version}`, version, state, effective_from, opened_at: null, rule_count: 2 })
const VERSIONS: RuleVersion[] = [ver(9, 'draft', null), ver(8, 'in_force'), ver(7, 'scheduled'), ver(6, 'superseded'), ver(3, 'retired', null)]

const latest = (list: string, status: string, over: Partial<CodeListLatest> = {}): CodeListLatest => ({
  list,
  synced_at: '2026-10-09T08:15:00Z',
  entry_count: 5612,
  added_count: 3,
  removed_count: 1,
  changed_count: 0,
  status,
  ...over,
})
const LISTS = [latest('hs-codes', 'applied'), latest('currencies', 'applied', { synced_at: '2026-10-08T07:00:00Z', added_count: 0, removed_count: 0, changed_count: 2 })]

const RULES_PROPS: Parameters<typeof Rules>[0] = {
  codeLists: LISTS,
  codeListsNote: 'Loading…',
  onOpenCodeList: noop,
  rules: SEED_RULES,
  status: 'ready',
  version: 4,
  state: 'in_force',
  versions: VERSIONS,
  versionsNote: 'Loading versions…',
  selected: 8,
  onSelectVersion: noop,
  busy: false,
  onRetry: noop,
  onOpenRule: noop,
  onToggleRule: noop,
  onPublish: noop,
  draftVersion: 9,
  draftBusy: false,
  onNewDraft: noop,
  onAddRule: noop,
  onTestDraft: noop,
}
const rulesWith = (over: Partial<Parameters<typeof Rules>[0]>) => renderToStaticMarkup(createElement(Rules, { ...RULES_PROPS, ...over }))

const tenant = (id: string, name: string, over: Partial<StaffTenant> = {}): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 0, invoice_count: 0, last_activity_at: null, ...over,
})
const T1 = tenant('t1', 'Okafor & Partners', { demo: true, active_members: 5, invoice_count: 31, last_activity_at: '2026-09-23T11:35:00Z' })
const T2 = tenant('t2', 'In-house Co', { kind: 'in_house' })
const member = (user_id: string, role: string, status: StaffMember['status'], display_name: string | null, email: string | null): StaffMember => ({ user_id, role, status, display_name, email })
const MEMBERS = [
  member('u1', 'admin', 'active', 'Ada Okafor', 'ada@x.test'),
  member('u2', 'reviewer', 'invited', null, 'rev@x.test'),
  member('u3', 'preparer', 'suspended', 'Pat Preparer', null),
]
const invoice = (n: string): StaffInvoice => ({ id: n, invoice_number: n, status: 'validated', created_at: '2026-10-13T08:00:00Z' })
const DETAIL: StaffTenantDetail = { tenant: T2, members: MEMBERS, invoices: [invoice('INV-1009')] }
const TENANTS_PROPS: Parameters<typeof Tenants>[0] = {
  status: 'ready',
  tenants: [T1, T2],
  query: '',
  onQueryChange: noop,
  selectedId: 't2',
  onSelect: noop,
  onRetry: noop,
  detailStatus: 'ready',
  detail: DETAIL,
  onRetryDetail: noop,
  onViewJobs: noop,
}
const tenantsWith = (over: Partial<Parameters<typeof Tenants>[0]>) => renderToStaticMarkup(createElement(Tenants, { ...TENANTS_PROPS, ...over }))

const svc = (name: string, status: ServiceHealth['status'], build?: string): ServiceHealth => ({ name, status, build })
const point = (n: number, services_down: number | null, queue_depth: number, dead_lettered: number, river_jobs: JobView | null = null): TrendPoint => ({ taken_at: `2026-10-14T09:${String(n).padStart(2, '0')}:00Z`, services_down, queue_depth, dead_lettered, river_jobs })
const jt = (queue_depth: number, dead_lettered: number): JobTotals => ({ queue_depth, dead_lettered })
// services keys are systemhealth.JobOwners values plus JobRunner
const jobView = (total: JobTotals, services: Record<string, JobTotals> = {}): JobView => ({ total, services: { submission: jt(0, 0), notifications: jt(0, 0), worker: total, ...services } })
// A sample whose river_jobs total equals its queue and dead values.
const kp = (n: number, services_down: number | null, q: number, d: number): TrendPoint => point(n, services_down, q, d, jobView(jt(q, d)))
const DEGRADED: FleetHealth = { status: 'degraded', services: [svc('gateway', 'up', '541ac755c7a1'), svc('docling', 'down', ''), svc('auth', 'down', 'abcdef1234')] }
const HEALTH: StaffHealth = {
  checked_at: '2026-10-14T10:05:31Z',
  fleet: DEGRADED,
  jobs: { queued: 1, submitting: 1, pending: 1, dead_lettered: 2 },
  queue_depth: 3,
  river_jobs: jobView(jt(3, 2)),
  trend: [kp(0, null, 1, 0), kp(5, 0, 2, 1), kp(10, 1, 3, 2)],
}
const PANEL_UNITS: Record<PanelKey, string> = {
  req_per_min: 'req/min',
  error_pct: '%',
  latency_p50_ms: 'ms',
  latency_p95_ms: 'ms',
  cpu_pct: '% of limit',
  ram_pct: '% of limit',
  instances: 'count',
  cold_starts_per_min: '/min',
  sql_cpu_pct: '%',
  sql_ram_pct: '%',
  sql_disk_pct: '%',
  sql_connections: 'count',
}
type PanelSeries = Partial<Record<PanelKey, Record<string, Point[]>>>
// Defaults: two services; fleet req 100, 5xx 1.00 %, p95 1200 ms, 23 connections of 80.
const metricsWith = (over: PanelSeries = {}, available = true): StaffMetrics => {
  const base: PanelSeries = {
    req_per_min: { a: [[0, 10]], b: [[0, 90]] },
    error_pct: { a: [[0, 10]], b: [[0, 0]] },
    latency_p95_ms: { a: [[0, 300]], b: [[0, 1200]] },
    sql_connections: { database: [[0, 23]] },
  }
  const by = { ...base, ...over }
  return {
    range: '1h',
    service: '',
    start: '2026-10-14T10:00:00Z',
    end: '2026-10-14T11:00:00Z',
    step_seconds: 60,
    connection_budget: 80,
    panels: (Object.keys(PANEL_UNITS) as PanelKey[]).map((key) => ({
      key,
      unit: PANEL_UNITS[key],
      available,
      series: Object.entries(by[key] ?? {}).map(([name, points]) => ({ name, points })),
    })),
    limits: { available: true, services: [] },
    deploys: { available: true, revisions: [] },
  }
}
const METRICS = metricsWith()
const HEALTH_PROPS: Parameters<typeof Health>[0] = {
  status: 'ready',
  data: HEALTH,
  healthStale: false,
  metricsStale: false,
  onRetry: noop,
  metricsStatus: 'ready',
  metrics: METRICS,
  onRetryMetrics: noop,
  range: '1h',
  onRange: noop,
  service: '',
  onService: noop,
}
const healthWith = (over: Partial<Parameters<typeof Health>[0]>) => renderToStaticMarkup(createElement(Health, { ...HEALTH_PROPS, ...over }))
const healthData = (over: Partial<StaffHealth>) => healthWith({ data: { ...HEALTH, ...over } })

const aiCall = (id: string, over: Partial<AiCall> = {}): AiCall => ({
  id, tenant_id: 't1', tenant_name: 'Okafor & Partners', created_at: '2026-10-14T09:12:03.123456Z', client: 'ai', purpose: 'document', model: 'x/y',
  input_tokens: 3447, output_tokens: 120, cost: '0.0014741', latency_ms: 1200, attempts: 1, outcome: 'ok', ...over,
})
const AI_PROPS: Parameters<typeof AiUsage>[0] = {
  view: 'calls',
  onView: noop,
  filters: {},
  onFilters: noop,
  tenants: [],
  status: 'ready',
  calls: [aiCall('c1'), aiCall('c2', { outcome: 'unavailable' })],
  hasMore: false,
  moreBusy: false,
  onMore: noop,
  onRetry: noop,
  totalsStatus: 'ready',
  totals: [],
  totalsHasMore: false,
  totalsMoreBusy: false,
  onMoreTotals: noop,
  onRetryTotals: noop,
}
const aiWith = (over: Partial<Parameters<typeof AiUsage>[0]>) => renderToStaticMarkup(createElement(AiUsage, { ...AI_PROPS, ...over }))

const SCREENS = { rules: renderToStaticMarkup(createElement(Rules, { ...RULES_PROPS })), tenants: tenantsWith({}), health: healthWith({}), ai: aiWith({}) }
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
  })

  it('SC-02 titles and figures', () => {
    const s = style(withText(tagsOf('rules'), 'Rules'))
    expect.soft(s['font-family'], 'Rules: family').toBe('var(--font-display)')
    expect.soft(s['font-weight'], 'Rules: weight').toBe('700')
    expect.soft(s['letter-spacing'], 'Rules: tracking').toBe('var(--tracking-card)')
  })

  it('SC-03 screen corners', () => {
    const rules = tagsOf('rules')
    const pills = rules.flatMap((t, i) => (['DRAFT', 'IN FORCE', 'SCHEDULED', 'SUPERSEDED', 'RETIRED'].includes(t.text) && classes(t).includes('mono') ? [rules[i - 1]] : []))
    expect(pills, 'version tags').toHaveLength(VERSIONS.length)
    for (const p of pills) expect.soft(style(p)['border-radius'], 'version tag corner').toBe('var(--radius-sm)')
    const inForce = rules.filter((t) => t.text.startsWith('IN FORCE v'))
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

    const typeChips = rules.filter((t) => classes(t).includes('mono') && style(t)['justify-self'] === 'start')
    expect(typeChips, 'rule type chips').toHaveLength(SEED_RULES.length)
    for (const c of typeChips) expect.soft(style(c)['border-radius'], `type chip ${c.text} corner`).toBe('var(--radius-sm)')

    // Cards: v2 corner, no shadow.
    const ts = parsed(SCREENS.rules, 'rules')
    const cards = ts.filter((t) => style(t).border === '1px solid var(--line-1)' && style(t).background === 'var(--bg-2)')
    expect(cards.length, 'rules: cards found').toBeGreaterThanOrEqual(2)
    for (const c of cards) expect.soft(style(c)['border-radius'], 'rules: card corner').toBe('var(--radius-md)')
    expect.soft(ts.filter((t) => 'box-shadow' in style(t)).map((t) => t.name), 'rules: no inline shadow').toEqual([])
  })

  it('Rules renders its non-ready states', () => {
    const forbidden = rulesWith({ status: 'forbidden', rules: [], version: null })
    expect(forbidden).toContain('Your account is not ASComply staff.')
    expect(forbidden).not.toContain('role="switch"')
    expect(forbidden).toContain('Rules admin')
    const loading = rulesWith({ status: 'loading', rules: [], version: null })
    expect(loading).toContain('Loading rules…')
    expect(loading).toContain('Rules admin')
    const failed = rulesWith({ status: 'error', errorText: 'no rule set in force', rules: [], version: null })
    expect(failed).toContain('no rule set in force')
    expect(buttonByText(failed, 'Retry').attrs).toContain('v2-btn-ghost')
  })

  it('Rules rail renders one row per version with its tone', () => {
    const html = rulesWith({})
    const rows = [...html.matchAll(/<button\b([^>]*aria-pressed[^>]*)>([\s\S]*?)<\/button>/g)]
    expect(rows).toHaveLength(VERSIONS.length)
    const chipOf = (row: RegExpMatchArray) => style({ attrs: ` style="${row[2].match(/<span style="([^"]*border-radius[^"]*)"/)![1]}"` })
    const wantBg = ['var(--status-amber-bg)', 'var(--status-green-bg)', 'var(--action-tint)', 'var(--status-muted-bg)', 'var(--status-muted-bg)']
    rows.forEach((row, i) => {
      expect(chipOf(row).background, `row ${i} chip`).toBe(wantBg[i])
      expect(row[2]).toContain(`v${VERSIONS[i].version}`)
    })
    expect(attr({ attrs: ` ${rows[1][1]}` }, 'aria-pressed')).toBe('true')
    expect(style({ attrs: ` ${rows[1][1]}` }).background).toBe('var(--action-tint)')
    expect(style({ attrs: ` ${rows[0][1]}` }).background).toBe('var(--bg-2)')
    expect(html).toContain('editing · 2 rules')
    expect(html).toContain('eff. 2026-01-01 · 2 rules')
    expect(html).not.toContain('--fg-4')
    for (const gone of ['v9 · draft', 'ACTIVE', 'ARCHIVED']) expect(html).not.toContain(gone)
  })

  it('switches are disabled off the version in force', () => {
    const switchesOf = (html: string) => [...html.matchAll(/<button\b([^>]*role="switch"[^>]*)>/g)].map((m) => ` ${m[1]}`)
    const locked = switchesOf(rulesWith({ state: 'retired', version: 3 }))
    expect(locked).toHaveLength(SEED_RULES.length)
    for (const t of locked) {
      expect(t).toContain('disabled=""')
      expect(t).toContain('title="Only the version in force is switched"')
      expect(style({ attrs: t })).toMatchObject({ opacity: '0.45', cursor: 'not-allowed' })
    }
    expect(rulesWith({ state: 'retired', version: 3 })).toContain('RETIRED v3')
    for (const t of switchesOf(rulesWith({ state: 'in_force' }))) expect(t).not.toContain('disabled=""')
  })

  it('Rules screen has no learned rules and no Promote', () => {
    const html = rulesWith({})
    expect(html).not.toContain('Learned rules')
    expect(html).not.toContain('Promote to draft')
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
    const idle = [...rulesWith({ busy: false }).matchAll(/<button\b([^>]*role="switch"[^>]*)>/g)]
    expect(idle).toHaveLength(SEED_RULES.length)
    for (const m of idle) expect(m[1]).not.toContain('disabled=""')
  })

  it('the header buttons follow the draft: New draft needs none, Publish draft needs one', () => {
    const btn = (html: string, name: string) => {
      const hit = [...html.matchAll(/<button\b([^>]*)>([^<]*(?:<svg[\s\S]*?<\/svg>)?[^<]*)<\/button>/g)].filter((m) => m[2].replace(/<svg[\s\S]*?<\/svg>/, '').trim() === name)
      expect(hit, `one "${name}" button`).toHaveLength(1)
      return ` ${hit[0][1]}`
    }
    const withDraft = rulesWith({ draftVersion: 9 })
    expect(btn(withDraft, 'New draft')).toContain('disabled=""')
    expect(btn(withDraft, 'Publish draft v9')).not.toContain('disabled=""')
    const none = rulesWith({ draftVersion: null })
    expect(btn(none, 'New draft')).not.toContain('disabled=""')
    expect(btn(none, 'Publish draft')).toContain('disabled=""')
    for (const html of [withDraft, none]) {
      const t = btn(html, html === withDraft ? 'New draft' : 'Publish draft')
      expect(style({ attrs: t })).toMatchObject({ opacity: '0.45', cursor: 'not-allowed' })
    }
    expect(btn(rulesWith({ draftVersion: 9, draftBusy: true }), 'Publish draft v9')).toContain('disabled=""')
    expect(btn(rulesWith({ draftVersion: null }), 'New draft')).toContain('class="ops-btn v2-btn v2-btn-ghost"')
  })

  it('Add rule and Test draft show on the draft only, in the sibling ghost paint at height 30', () => {
    const on = rulesWith({ state: 'draft', version: 9 })
    for (const name of ['Add rule', 'Test draft']) {
      const m = on.match(new RegExp(`<button([^>]*)>${name}</button>`))
      expect(m, `${name} on the draft`).not.toBeNull()
      expect(m![1]).toContain('v2-btn-ghost')
      expect(style({ attrs: ` ${m![1]}` }).height).toBe('30px')
      expect(rulesWith({ state: 'in_force' }), `${name} off the draft`).not.toContain(`>${name}<`)
    }
    expect(rulesWith({ state: 'draft', version: 9, draftBusy: true })).toMatch(/<button[^>]*disabled=""[^>]*>Add rule/)
  })

  // Panel rows are the buttons that carry no aria-pressed and no role.
  const panelRows = (html: string) => [...html.matchAll(/<button\b([^>]*)>([\s\S]*?)<\/button>/g)].filter((m) => !/aria-pressed|role=|v2-btn/.test(m[1]))

  it('Code-list panel renders rows and the empty state', () => {
    const html = rulesWith({})
    const rows = panelRows(html)
    expect(rows).toHaveLength(2)
    expect(rows[0][2]).toContain('hs-codes')
    expect(rows[0][2]).toContain('synced 2026-10-09')
    expect(rows[0][2]).toContain('+3 −1 ~0')
    expect(rows[1][2]).toContain('+0 −0 ~2')
    expect(html).toContain('Code-list syncs')
    expect(html).not.toContain('--fg-4')
    const empty = rulesWith({ codeLists: [] })
    expect(panelRows(empty)).toHaveLength(0)
    expect(empty).toContain('No sync recorded yet.')
    const loading = rulesWith({ codeLists: null, codeListsNote: 'Your account is not ASComply staff.' })
    expect(loading).toContain('Your account is not ASComply staff.')
    expect(loading).not.toContain('No sync recorded yet.')
  })

  it('a panel row wears the rail row paint: padding, background and chip radius (D31)', () => {
    const html = rulesWith({ selected: 8 })
    const rail = [...html.matchAll(/<button\b([^>]*aria-pressed="false"[^>]*)>([\s\S]*?)<\/button>/g)][0]
    const row = panelRows(html)[0]
    const chip = (inner: string) => style({ attrs: ` style="${inner.match(/<span style="([^"]*border-radius[^"]*)"/)![1]}"` })
    const a = style({ attrs: ` ${rail[1]}` })
    const b = style({ attrs: ` ${row[1]}` })
    expect([b.padding, b.background]).toEqual([a.padding, a.background])
    expect(chip(row[2])['border-radius']).toBe(chip(rail[2])['border-radius'])
  })

  it('a held sync says it is held and would remove, in the panel (D32)', () => {
    const rows = panelRows(rulesWith({ codeLists: [latest('hs-codes', 'held')] }))
    expect(rows[0][2]).toContain('HELD')
    expect(rows[0][2]).toContain('+3 would remove 1 ~0')
    expect(rows[0][2]).not.toContain('−1')
  })

  it('a held row with zero removed says would remove 0; a released row reads as removed', () => {
    const rows = panelRows(rulesWith({ codeLists: [latest('a', 'held', { removed_count: 0 }), latest('b', 'released', { removed_count: 4 })] }))
    expect(rows).toHaveLength(2)
    expect(rows[0][2]).toContain('+3 would remove 0 ~0')
    expect(rows[1][2]).toContain('RELEASED')
    expect(rows[1][2]).toContain('+3 −4 ~0')
    expect(rows[1][2]).not.toContain('would remove')
  })

  it('panel rows render in the served order', () => {
    const rows = panelRows(rulesWith({ codeLists: [latest('zz', 'applied'), latest('aa', 'applied')] }))
    expect(rows).toHaveLength(2)
    expect(rows[0][2]).toContain('>zz<')
    expect(rows[1][2]).toContain('>aa<')
  })

  it('a status named like an Object.prototype key renders as a muted chip', () => {
    const rows = panelRows(rulesWith({ codeLists: [latest('hs-codes', 'constructor')] }))
    expect(rows[0][2]).toContain('CONSTRUCTOR')
    expect(rows[0][2]).toContain('var(--status-muted-bg)')
  })

  it('an unknown sync status still renders as a muted chip (D32)', () => {
    const rows = panelRows(rulesWith({ codeLists: [latest('hs-codes', 'quarantined')] }))
    expect(rows[0][2]).toContain('QUARANTINED')
    expect(rows[0][2]).toContain('var(--status-muted-bg)')
    expect(rows[0][2]).toContain('+3 −1 ~0')
  })

  // Tenants: rows are the buttons that carry data-tenant-id.
  const tenantRows = (html: string) => [...html.matchAll(/<button\b([^>]*data-tenant-id[^>]*)>([\s\S]*?)<\/button>/g)]
  const rowText = (inner: string) => decode(inner.replace(/<[^>]*>/g, '|')).split('|').filter(Boolean)

  it('Tenants rows show kind, counts, last activity and the demo badge', () => {
    const rows = tenantRows(tenantsWith({}))
    expect(rows).toHaveLength(2)
    expect(rows[0][1]).toContain('data-tenant-id="t1"')
    expect(rowText(rows[0][2])).toEqual(expect.arrayContaining(['DEMO', 'FIRM · 5 active · 31 invoices', 'last activity 2026-09-23 11:35 UTC']))
    expect(rowText(rows[1][2])).not.toContain('DEMO')
    expect(rowText(rows[1][2])).toEqual(expect.arrayContaining(['IN-HOUSE · 0 active · 0 invoices', 'no activity']))
  })

  it('the DEMO badge and the cards wear v2 corners', () => {
    const ts = parsed(SCREENS.tenants, 'tenants')
    const demo = ts.filter((t) => t.text === 'DEMO')
    expect(demo.length).toBeGreaterThan(0)
    for (const d of demo) expect.soft(style(ts[ts.indexOf(d) - 1])['border-radius'], 'DEMO badge corner').toBe('var(--radius-sm)')
    const cards = ts.filter((t) => style(t).border === '1px solid var(--line-1)' && style(t).background === 'var(--bg-2)')
    expect(cards, 'list and detail cards').toHaveLength(2)
    for (const c of cards) expect.soft(style(c)['border-radius'], 'card corner').toBe('var(--radius-md)')
    expect(ts.filter((t) => 'box-shadow' in style(t)).map((t) => t.name), 'no inline shadow').toEqual([])
  })

  it('the selected row wears the prototype selection paint', () => {
    const rows = tenantRows(tenantsWith({ selectedId: 't2' }))
    const at = (i: number) => style({ attrs: ` ${rows[i][1]}` })
    expect(attr({ attrs: ` ${rows[1][1]}` }, 'aria-pressed')).toBe('true')
    expect(attr({ attrs: ` ${rows[0][1]}` }, 'aria-pressed')).toBe('false')
    expect([at(1).background, at(0).background]).toEqual(['var(--bg-3)', 'var(--bg-2)'])
    expect(classes({ attrs: ` ${rows[0][1]}` })).toContain('ops-nav')
    for (const r of rows) {
      const tile = r[2].match(/<span style="([^"]*)">([^<]*)<\/span>/)!
      expect([style({ attrs: ` style="${tile[1]}"` }).width, style({ attrs: ` style="${tile[1]}"` }).height]).toEqual(['30px', '30px'])
    }
  })

  it('member rows carry an initials circle and the role tone', () => {
    const html = tenantsWith({})
    const circles = [...html.matchAll(/<span style="([^"]*width:26px[^"]*)">([^<]*)<\/span>/g)]
    expect(circles.map((c) => c[2])).toEqual(['AO', 'R', 'PP'])
    for (const c of circles) expect(style({ attrs: ` style="${c[1]}"` })).toMatchObject({ width: '26px', height: '26px', 'border-radius': '50%' })
    const chip = (label: string) => style({ attrs: ` style="${html.match(new RegExp(`<span class="mono" style="([^"]*)">${label}</span>`))![1]}"` }).background
    expect([chip('ADMIN'), chip('REVIEWER'), chip('PREPARER')]).toEqual(['var(--action-tint)', 'var(--status-amber-bg)', 'var(--status-muted-bg)'])
  })

  it('member status badges use the app member tones', () => {
    const html = tenantsWith({})
    const bg = (label: string) => html.match(new RegExp(`<span style="[^"]*background:([^;"]*)[^"]*"><span class="mono"[^>]*>${label}</span>`))![1]
    expect([bg('ACTIVE'), bg('INVITED'), bg('SUSPENDED')]).toEqual(['var(--status-muted-bg)', 'var(--status-amber-bg)', 'var(--status-red-bg)'])
  })

  it('Tenants renders every list and detail state', () => {
    const lists = {
      loading: 'Loading tenants…',
      empty: 'No tenants yet.',
      forbidden: 'Your account is not ASComply staff.',
      error: 'boom',
    } as const
    for (const [status, copy] of Object.entries(lists) as [keyof typeof lists, string][]) {
      const html = tenantsWith({ status, tenants: [], errorText: 'boom' })
      expect(html, `list ${status}`).toContain(copy)
      expect(html.includes('>Retry<'), `list ${status} Retry`).toBe(status === 'error')
    }
    const details = {
      none: 'Select a tenant to see its members and recent invoices.',
      loading: 'Loading tenant…',
      'not-found': 'Tenant not found.',
      forbidden: 'Your account is not ASComply staff.',
      error: 'down',
    } as const
    for (const [detailStatus, copy] of Object.entries(details) as [keyof typeof details, string][]) {
      const html = tenantsWith({ detailStatus, detail: null, detailErrorText: 'down' })
      expect(html, `detail ${detailStatus}`).toContain(copy)
      expect(html.includes('>Retry<'), `detail ${detailStatus} Retry`).toBe(detailStatus === 'error')
    }
    const bare = tenantsWith({ detail: { tenant: T2, members: [], invoices: [] } })
    expect(bare).toContain('No members.')
    expect(bare).toContain('No invoices yet.')
    expect(tenantsWith({ status: 'ready', tenants: [T1], query: 'zzz' })).toContain('No tenant matches.')
  })

  it('the tenant detail offers View jobs as its only action', () => {
    const detail = SCREENS.tenants.slice(SCREENS.tenants.indexOf('<h2'))
    expect(detail.match(/<button\b/g)).toHaveLength(1)
    expect(detail).toMatch(/<button\b[^>]*>View jobs<\/button>/)
  })

  it('the tenants grid holds two columns', () => {
    const grid = withClass(tagsOf('tenants'), 'ops-tenants-grid')
    expect(grid).toHaveLength(1)
    expect(style(grid[0])['grid-template-columns']).toBe('320px minmax(0,1fr)')
  })

  it('long names, emails and invoice numbers stay on one line inside their rows', () => {
    const long = 'x'.repeat(120)
    const html = tenantsWith({
      tenants: [tenant('t2', long, { demo: true })],
      detail: { tenant: tenant('t2', long, { demo: true }), members: [member('u1', 'admin', 'active', long, `${long}@x.test`)], invoices: [invoice(long)] },
    })
    const ts = parse(html)
    const oneLine = { overflow: 'hidden', 'white-space': 'nowrap', 'text-overflow': 'ellipsis' }
    const hits = (name: string, text: string) => ts.filter((t) => t.name === name && t.text === text)
    const h2 = hits('h2', long)
    const memberName = hits('div', long)
    const email = hits('div', `${long}@x.test`)
    const number = ts.filter((t) => t.name === 'span' && t.text === long && style(t)['font-size'] === '11.5px')
    for (const [what, found] of [['h2', h2], ['member name', memberName], ['email', email], ['invoice number', number]] as const) {
      expect(found, `${what} found`).toHaveLength(1)
      expect.soft(style(found[0]), `${what} one line`).toMatchObject(oneLine)
    }
    // Each ellipsis element sits in a flex block that can shrink.
    const before = (tag: Tag) => ts[ts.indexOf(tag) - 1]
    expect.soft(style(before(h2[0]))['min-width'], 'h2 name row').toBe('0')
    expect.soft(style(before(memberName[0]))['min-width'], 'member name block').toBe('0')
    expect.soft(html, 'email shares the name block').toMatch(new RegExp(`min-width:0"><div[^>]*>${long}</div><div[^>]*>${long}@x.test</div>`))
    expect.soft(style(before(number[0]))['min-width'], 'invoice row').toBe('0')
    // Chips and badges: every wrapper of a label holds flex:none.
    for (const label of ['DEMO', 'ADMIN', 'ACTIVE', 'VALIDATED']) {
      const labels = ts.filter((t) => t.text === label)
      expect(labels.length, label).toBeGreaterThan(0)
      for (const l of labels) {
        const i = ts.indexOf(l)
        const own = style(l)
        const wrap = own.flex === 'none' ? own : style(ts[i - 1].name === 'span' && style(ts[i - 1])['border-radius'] ? ts[i - 2] : ts[i - 1])
        expect.soft(wrap.flex, `${label} wrapper`).toBe('none')
      }
    }
  })
})

describe('System health markup', () => {
  const tileOf = (ts: Tag[], key: string) => {
    const at = ts.findIndex((t) => attr(t, 'data-tile') === key)
    expect(at, `tile ${key}`).toBeGreaterThan(-1)
    const next = ts.findIndex((t, i) => i > at && (attr(t, 'data-tile') !== '' || attr(t, 'data-panels') !== '' || t.text === 'Services'))
    return ts.slice(at, next === -1 ? undefined : next)
  }
  const tilesOf = (html: string) => {
    const ts = parse(html)
    return { all: ts, fleet: tileOf(ts, 'fleet'), queue: tileOf(ts, 'queue'), dead: tileOf(ts, 'dead-lettered') }
  }
  const texts = (ts: Tag[]) => ts.map((t) => t.text)

  it('Health tiles show fleet, queue depth and dead-lettered', () => {
    const html = healthData({ jobs: { dead_lettered: 9 }, queue_depth: 8, river_jobs: jobView(jt(3, 2)) })
    const { all, fleet, queue, dead } = tilesOf(html)
    expect(all.filter((t) => attr(t, 'data-tile')).map((t) => attr(t, 'data-tile'))).toEqual(['fleet', 'req', 'errors', 'p95', 'connections', 'queue', 'dead-lettered'])
    expect(texts(fleet)).toEqual(expect.arrayContaining(['Fleet', '1/3', 'services up', 'DEGRADED', 'Down: docling, auth']))
    expect(texts(queue)).toEqual(expect.arrayContaining(['Queue depth', '3', 'jobs']))
    expect(texts(queue).filter((x) => /^[A-Z]+$/.test(x)), 'queue has no status word').toEqual([])
    expect(texts(dead)).toEqual(expect.arrayContaining(['Dead-lettered', '2', 'jobs', 'ATTENTION']))
    for (const gone of ['APP latency', 'APP error rate', 'throughput', 'Recon']) expect(html).not.toContain(gone)
  })

  it('the All services jobs trend draws only kind samples', () => {
    const rj = (q: number) => jobView(jt(q, 0))
    const two = tilesOf(healthData({ trend: [point(0, 0, 1, 0, null), point(5, 0, 2, 0, rj(1)), point(10, 0, 3, 0, rj(4))] }))
    expect(two.queue.filter((x) => x.name === 'svg')).toHaveLength(1)
    const one = tilesOf(healthData({ trend: [point(0, 0, 1, 0, null), point(5, 0, 2, 0, null), point(10, 0, 3, 0, rj(4))] }))
    expect(one.queue.some((x) => x.name === 'svg')).toBe(false)
  })

  it('a picked service shows its own job totals', () => {
    const rj = jobView(jt(9, 3), { submission: jt(2, 0), notifications: jt(0, 3) })
    const sub = tilesOf(healthWith({ data: { ...HEALTH, river_jobs: rj }, service: 'submission' }))
    expect(texts(sub.queue)).toEqual(expect.arrayContaining(['2']))
    expect(texts(sub.dead)).toEqual(expect.arrayContaining(['0', 'CLEAR']))
    const not = tilesOf(healthWith({ data: { ...HEALTH, river_jobs: rj }, service: 'notifications' }))
    expect(texts(not.queue)).toEqual(expect.arrayContaining(['0']))
    expect(texts(not.dead)).toEqual(expect.arrayContaining(['3', 'ATTENTION']))
  })

  it('a service without jobs shows one note in place of the jobs tiles', () => {
    const html = healthWith({ service: 'app' })
    const note = 'app owns no background jobs. Pick submission, notifications, worker or All services to see them.'
    expect(html.split(note)).toHaveLength(2)
    expect(html).not.toContain('data-tile="queue"')
    expect(html).not.toContain('data-tile="dead-lettered"')
    expect(html).not.toContain('ops-health-jobs')
  })

  it('a picked service shows only its ping row', () => {
    const rows = (service: string) => parse(healthWith({ service })).filter((t) => attr(t, 'data-service'))
    expect(rows('gateway').map((r) => attr(r, 'data-service'))).toEqual(['gateway'])
    expect(healthWith({ service: 'gateway' })).toContain('aria-label="Open gateway in Sentry"')
    expect(rows('auth').map((r) => attr(r, 'data-service'))).toEqual(['auth'])
    expect(healthWith({ service: 'auth' })).not.toContain('in Sentry')
  })

  it('a service without a live ping reads the no-ping note', () => {
    const html = healthWith({ service: 'app' })
    expect(parse(html).filter((t) => attr(t, 'data-service'))).toHaveLength(0)
    expect(html.split('app has no live ping. The live ping covers the backend services.')).toHaveLength(2)
  })

  it('an unknown roll-up keeps its copy whatever the pick', () => {
    const html = healthWith({ service: 'gateway', data: { ...HEALTH, fleet: { status: 'unknown', services: [] } } })
    expect(html.split('The gateway roll-up is unavailable.')).toHaveLength(2)
    expect(html).not.toContain('has no live ping')
  })

  it('the fleet-wide tiles carry All services at every pick', () => {
    for (const service of ['', 'submission']) {
      const ts = parse(healthWith({ service }))
      const tag = (key: string) => tileOf(ts, key).filter((t) => t.attrs.includes('data-scope'))
      const sqlTag = parse(healthWith({ service })).filter((t) => t.text === 'All services' && t.name === 'span' && !t.attrs.includes('data-scope'))[0]
      for (const key of ['fleet', 'connections']) {
        expect(tag(key), `${key} at ${service}`).toHaveLength(1)
        expect(tag(key)[0].text).toBe('All services')
        expect(classes(tag(key)[0])).toContain('mono')
        expect(style(tag(key)[0])).toEqual(style(sqlTag))
      }
      for (const key of ['req', 'errors', 'p95', 'queue', 'dead-lettered']) expect(tag(key), `${key} at ${service}`).toHaveLength(0)
    }
  })

  it('Health tiles name the healthy and unknown states', () => {
    const ok = tilesOf(healthData({ fleet: { status: 'ok', services: [svc('gateway', 'up'), svc('auth', 'up')] }, jobs: { dead_lettered: 0 }, river_jobs: jobView(jt(3, 0)) }))
    expect(texts(ok.fleet)).toEqual(expect.arrayContaining(['2/2', 'HEALTHY']))
    expect(texts(ok.fleet).some((x) => x.startsWith('Down:'))).toBe(false)
    expect(texts(ok.dead)).toContain('CLEAR')
    const unknown = tilesOf(healthData({ fleet: { status: 'unknown', services: [] } }))
    expect(texts(unknown.fleet)).toEqual(expect.arrayContaining(['UNKNOWN', '—']))
  })

  it('a tile draws a sparkline only from two points', () => {
    const one = tilesOf(healthData({ trend: [kp(0, 0, 1, 0)] }))
    for (const t of [one.fleet, one.queue, one.dead]) expect(t.some((x) => x.name === 'svg')).toBe(false)
    expect(texts(one.queue)).toContain('3')
    const three = tilesOf(healthData({ trend: [kp(0, null, 1, 0), kp(5, 0, 2, 1), kp(10, 1, 3, 2)] }))
    for (const t of [three.fleet, three.queue, three.dead]) expect(t.filter((x) => x.name === 'svg')).toHaveLength(1)
    // services_down has 2 non-null points of 3; one non-null point of 2 draws nothing.
    const sparse = tilesOf(healthData({ trend: [kp(0, null, 1, 0), kp(5, 0, 2, 1)] }))
    expect(sparse.fleet.some((x) => x.name === 'svg')).toBe(false)
    expect(sparse.queue.filter((x) => x.name === 'svg')).toHaveLength(1)
  })

  it('the Services card lists every service with its build', () => {
    const ts = parse(healthWith({}))
    const rows = ts.filter((t) => attr(t, 'data-service'))
    expect(rows.map((r) => attr(r, 'data-service'))).toEqual(['gateway', 'docling', 'auth'])
    const after = (name: string) => ts.slice(ts.indexOf(rows.find((r) => attr(r, 'data-service') === name)!))
    const badgeOf = (name: string, word: string) => {
      const rowTs = after(name)
      const label = rowTs.find((t) => t.text === word)!
      return style(rowTs[rowTs.indexOf(label) - 1])
    }
    expect(badgeOf('gateway', 'UP').background).toBe('var(--status-muted-bg)')
    expect(badgeOf('docling', 'DOWN').background).toBe('var(--status-red-bg)')
    expect(badgeOf('auth', 'DOWN').background).toBe('var(--status-red-bg)')
    const builds = ts.filter((t) => t.name === 'span' && ['541ac75', '—', 'abcdef1'].includes(t.text) && style(t).flex === 'none')
    expect(builds.map((b) => [b.text, attr(b, 'title')])).toEqual([['541ac75', '541ac755c7a1'], ['—', ''], ['abcdef1', 'abcdef1234']])
    const unknown = healthData({ fleet: { status: 'unknown', services: [] } })
    expect(unknown).toContain('The gateway roll-up is unavailable.')
    expect(unknown).not.toContain('data-service')
  })

  it('the health header carries the check time', () => {
    const ts = parse(healthWith({}))
    withText(ts, 'INFRASTRUCTURE')
    withText(ts, 'System health')
    withText(ts, 'CHECKED 2026-10-14 10:05 UTC')
  })

  it('Health renders every state', () => {
    const loading = healthWith({ status: 'loading', data: null })
    expect(loading).toContain('Loading system health…')
    expect(loading).toContain('System health')
    expect(loading).not.toContain('CHECKED')
    const forbidden = healthWith({ status: 'forbidden', data: null })
    expect(forbidden).toContain('Your account is not ASComply staff.')
    expect(forbidden).not.toContain('Retry')
    const failed = healthWith({ status: 'error', errorText: 'boom', data: null })
    expect(failed).toContain('boom')
    expect(buttonByText(failed, 'Retry').attrs).toContain('v2-btn-ghost')
    const metricsFailed = healthWith({ metricsStatus: 'error', metricsErrorText: 'metrics boom', metrics: null })
    const ts = parse(metricsFailed)
    for (const key of ['fleet', 'queue', 'dead-lettered']) tileOf(ts, key)
    expect(ts.filter((t) => attr(t, 'data-service'))).toHaveLength(3)
    expect(metricsFailed).toContain('metrics boom')
    expect(buttonByText(metricsFailed, 'Retry').attrs).toContain('v2-btn-ghost')
    expect(healthWith({ metricsStatus: 'forbidden', metrics: null })).toContain('Your account is not ASComply staff.')
  })

  it('the stat and jobs grids hold their tracks and collapse', () => {
    const ts = parse(healthWith({}))
    const stats = withClass(ts, 'ops-health-stats')
    const jobs = withClass(ts, 'ops-health-jobs')
    expect(stats).toHaveLength(1)
    expect(jobs).toHaveLength(1)
    expect(withClass(ts, 'ops-health-grid')).toHaveLength(0)
    expect(style(stats[0])['grid-template-columns']).toBe('repeat(5, minmax(0, 1fr))')
    expect(style(jobs[0])['grid-template-columns']).toBe('repeat(2, minmax(0, 1fr))')
    const css = readFileSync(new URL('./styles/support.css', import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
    const rule = (width: number) => css.match(new RegExp(`@media \\(max-width: ${width}px\\) \\{([\\s\\S]*?)\\n\\}`, 'i'))![1]
    const cols = (width: number, cls: string) => rule(width).match(new RegExp(`\\.${cls}[^{]*\\{\\s*grid-template-columns:\\s*([^;]*);`, 'i'))?.[1]
    expect(cols(1024, 'ops-health-stats')).toBe('repeat(3, minmax(0, 1fr)) !important')
    expect(cols(768, 'ops-health-stats')).toBe('repeat(2, minmax(0, 1fr)) !important')
    expect(cols(480, 'ops-health-stats')).toBe('minmax(0, 1fr) !important')
    expect(rule(480)).toMatch(/\.ops-health-jobs\s*\{\s*grid-template-columns:\s*minmax\(0, 1fr\) !important;/)
    expect(rule(1024)).not.toContain('ops-health-jobs')
    expect(rule(768)).not.toContain('ops-health-jobs')
  })

  it('the range bar holds the five ranges with 1h pressed', () => {
    const chips = withClass(parse(healthWith({})), 'ops-chip')
    expect(chips.map((c) => c.text)).toEqual(['15m', '1h', '6h', '24h', '7d'])
    expect(chips.map((c) => attr(c, 'aria-pressed'))).toEqual(['false', 'true', 'false', 'false', 'false'])
    const painted = (on: boolean) => style(parse(renderToStaticMarkup(createElement('i', { style: chipStyle(on, null) })))[0])
    expect(chips.map((c) => style(c))).toEqual([painted(false), painted(true), painted(false), painted(false), painted(false)])
  })

  it('the service select lists All services then every metric service', () => {
    const html = healthWith({})
    const sel = html.match(/<select\b[^>]*aria-label="Service"[^>]*>([\s\S]*?)<\/select>/)![1]
    const opts = [...sel.matchAll(/<option value="([^"]*)"[^>]*>([^<]*)</g)]
    expect(opts.map((o) => o[1])).toEqual(['', ...METRIC_SERVICES])
    expect(opts[0][2]).toBe('All services')
  })

  it('the stat row shows fleet, requests, 5xx, p95 and connections', () => {
    const ts = parse(healthWith({}))
    expect(ts.filter((t) => attr(t, 'data-tile')).map((t) => attr(t, 'data-tile')).slice(0, 5)).toEqual(['fleet', 'req', 'errors', 'p95', 'connections'])
    expect(texts(tileOf(ts, 'req'))).toEqual(expect.arrayContaining(['Requests', '100', 'req/min']))
    expect(texts(tileOf(ts, 'req')).filter((x) => /^[A-Z ]+$/.test(x))).toEqual([])
    expect(texts(tileOf(ts, 'errors'))).toEqual(expect.arrayContaining(['1.00', '%', 'OK']))
    expect(texts(tileOf(ts, 'p95'))).toEqual(expect.arrayContaining(['1.20', 's', 'HIGH']))
    expect(texts(tileOf(ts, 'connections'))).toEqual(expect.arrayContaining(['23', 'of 80', 'OK']))
  })

  const dotOf = (ts: Tag[]) => ts.map((t) => style(t).background).find((b) => b?.includes('--status-'))

  it('a stat over its threshold reads HIGH in red', () => {
    const ts = parse(healthWith({ metrics: metricsWith({ error_pct: { a: [[0, 1.5]] }, req_per_min: { a: [[0, 10]] }, sql_connections: { database: [[0, 70]] } }) }))
    for (const key of ['errors', 'connections']) {
      const t = tileOf(ts, key)
      expect(texts(t), key).toContain('HIGH')
      expect(dotOf(t), key).toBe('var(--status-red-text)')
    }
  })

  it('a stat that peaked over its threshold reads PEAKED in amber', () => {
    const ts = parse(healthWith({ metrics: metricsWith({ error_pct: { a: [[0, 3], [60, 0.2]] }, req_per_min: { a: [[0, 10], [60, 10]] } }) }))
    const t = tileOf(ts, 'errors')
    expect(texts(t)).toEqual(expect.arrayContaining(['0.20', 'PEAKED']))
    expect(dotOf(t)).toBe('var(--status-amber-text)')
  })

  it('a stat with no data reads NO DATA in the muted tone', () => {
    const ts = parse(healthWith({ metrics: metricsWith({}, false) }))
    for (const key of ['req', 'errors', 'p95', 'connections']) {
      const t = tileOf(ts, key)
      expect(texts(t), key).toEqual(expect.arrayContaining(['—', 'NO DATA']))
      expect(dotOf(t), key).toBe('var(--status-muted-text)')
    }
  })

  it('a failed metrics read reads UNAVAILABLE on the tiles, not NO DATA', () => {
    for (const metricsStatus of ['error', 'forbidden'] as const) {
      const html = healthWith({ metricsStatus, metrics: null })
      const ts = parse(html)
      for (const key of ['req', 'errors', 'p95', 'connections']) {
        const t = tileOf(ts, key)
        expect(texts(t), `${metricsStatus} ${key}`).toEqual(expect.arrayContaining(['—', 'UNAVAILABLE']))
        expect(texts(t), `${metricsStatus} ${key}`).not.toContain('NO DATA')
      }
    }
  })

  it('metric tiles carry no word while the metrics read loads', () => {
    const html = healthWith({ metricsStatus: 'loading', metrics: null })
    const ts = parse(html)
    for (const key of ['req', 'errors', 'p95', 'connections']) {
      const t = tileOf(ts, key)
      expect(texts(t), key).toContain('—')
      expect(texts(t).filter((x) => /^[A-Z ]+$/.test(x)), key).toEqual([])
    }
    expect(html).toContain('Loading metrics…')
  })

  it('the jobs row keeps queue depth and dead-lettered', () => {
    const ts = parse(healthWith({}))
    for (const [key, value, word] of [['queue', '3', ''], ['dead-lettered', '2', 'ATTENTION']]) {
      const t = tileOf(ts, key)
      expect(texts(t), key).toContain(value)
      if (word) expect(texts(t)).toContain(word)
      expect(t.filter((x) => x.name === 'svg'), key).toHaveLength(1)
    }
  })

  it('the header links open both Cloud Monitoring dashboards in a new tab', () => {
    const links = [...healthWith({}).matchAll(/<a\b([^>]*)>(?:<svg[\s\S]*?<\/svg>)?([^<]*)<\/a>/g)]
      .map((m) => ({ attrs: ` ${m[1]}`, text: m[2] }))
      .filter((l) => l.text.endsWith('dashboard'))
    expect(links.map((l) => l.text)).toEqual(['Services dashboard', 'Database and edge dashboard'])
    expect(links.map((l) => decode(attr(l, 'href')))).toEqual([DASHBOARDS.services, DASHBOARDS.database_edge])
    for (const l of links) {
      expect(attr(l, 'target')).toBe('_blank')
      expect(attr(l, 'rel')).toBe('noopener noreferrer')
    }
  })

  it('each service row links to its Sentry issues', () => {
    const ts = parse(healthData({ fleet: { status: 'ok', services: [svc('gateway', 'up'), svc('docling', 'up')] } }))
    for (const name of ['gateway', 'docling']) {
      const row = ts.indexOf(ts.find((t) => attr(t, 'data-service') === name)!)
      const link = ts.slice(row).find((t) => t.name === 'a')!
      expect(decode(attr(link, 'href'))).toBe(sentryServiceUrl(name))
      expect(attr(link, 'aria-label')).toBe(`Open ${name} in Sentry`)
      expect(attr(link, 'target')).toBe('_blank')
      expect(attr(link, 'rel')).toBe('noopener noreferrer')
    }
  })

  it('the auth row has no Sentry link', () => {
    const html = healthData({ fleet: { status: 'ok', services: [svc('gateway', 'up'), svc('auth', 'up')] } })
    const rows = [...html.matchAll(/<div data-service="([^"]*)"[^>]*>([\s\S]*?)(?=<div data-service=|$)/g)]
    const [gateway, auth] = rows.map((r) => r[2])
    expect(gateway).toContain('aria-label="Open gateway in Sentry"')
    expect(auth).not.toContain('<a ')
    expect(auth).not.toContain('Sentry')
  })

  it('long names stay on one line', () => {
    const long = 'x'.repeat(120)
    const html = healthData({ fleet: { status: 'degraded', services: [svc(long, 'down', 'abcdef1234')] } })
    const ts = parse(html)
    const oneLine = { overflow: 'hidden', 'white-space': 'nowrap', 'text-overflow': 'ellipsis' }
    const name = ts.filter((t) => t.name === 'div' && t.text === long)
    expect(name).toHaveLength(1)
    expect(style(name[0])).toMatchObject(oneLine)
    expect(style(ts[ts.indexOf(name[0]) - 1])['min-width']).toBe('0')
    const down = ts.filter((t) => t.text === `Down: ${long}`)
    expect(down).toHaveLength(1)
    expect(style(down[0])).toMatchObject(oneLine)
    const tile = ts.find((t) => attr(t, 'data-tile') === 'fleet')!
    expect(style(tile)['min-width']).toBe('0')
    const badge = ts.find((t) => t.text === 'DOWN')!
    expect(style(ts[ts.indexOf(badge) - 2]).flex, 'badge wrapper').toBe('none')
    const build = ts.find((t) => t.text === 'abcdef1')!
    expect(style(build).flex).toBe('none')
  })
})

describe('System health panels', () => {
  const ORDER = ['req_per_min', 'error_pct', 'latency_p50_ms', 'latency_p95_ms', 'cpu_pct', 'ram_pct', 'instances', 'cold_starts_per_min', 'sql_util', 'sql_connections']
  const TITLES = ['Requests / min', '5xx %', 'Latency p50 (ms)', 'Latency p95 (ms)', 'CPU % of limit', 'RAM % of limit', 'Instances', 'Cold starts / min', 'Cloud SQL CPU, RAM, disk (%)', 'Cloud SQL connections']
  const FLAT: [number, number][] = [[0, 5], [60, 6]]
  const db = (v: [number, number][] = FLAT) => ({ database: v })
  const svcs = (names: string[], f: (i: number) => [number, number][] = () => FLAT) => Object.fromEntries(names.map((n, i) => [n, f(i)]))
  const ALL: PanelSeries = {
    req_per_min: svcs(['a', 'b']),
    error_pct: svcs(['a', 'b']),
    latency_p50_ms: svcs(['a', 'b']),
    latency_p95_ms: svcs(['a', 'b']),
    cpu_pct: svcs(['a', 'b']),
    ram_pct: svcs(['a', 'b']),
    instances: svcs(['a', 'b']),
    cold_starts_per_min: svcs(['a', 'b']),
    sql_cpu_pct: db(),
    sql_ram_pct: db(),
    sql_disk_pct: db(),
    sql_connections: db(),
  }
  const full = (over: PanelSeries = {}) => metricsWith({ ...ALL, ...over })
  const renderPanels = (m: StaffMetrics, service = '') => parse(healthWith({ metrics: m, service }))
  const panelTags = (ts: Tag[], key: string) => {
    const at = ts.findIndex((t) => attr(t, 'data-panel') === key)
    expect(at, `panel ${key}`).toBeGreaterThan(-1)
    const next = ts.findIndex((t, i) => i > at && (attr(t, 'data-panel') !== '' || t.text === 'Services'))
    return ts.slice(at, next === -1 ? undefined : next)
  }
  const lines = (p: Tag[]) => p.filter((t) => attr(t, 'data-line'))
  const guides = (p: Tag[]) => p.filter((t) => t.name === 'line' && t.attrs.includes('data-guide'))
  const words = (p: Tag[]) => p.filter((t) => t.attrs.includes('data-word'))
  const entries = (p: Tag[]) => p.filter((t) => attr(t, 'data-series'))
  const patch = (m: StaffMetrics, key: PanelKey, f: (p: StaffMetrics['panels'][number]) => void) => {
    f(m.panels.find((p) => p.key === key)!)
    return m
  }

  it('the panels grid holds the ten panels in order', () => {
    const html = healthWith({ metrics: full() })
    const ts = parse(html)
    const panels = ts.filter((t) => attr(t, 'data-panel'))
    expect(panels.map((t) => attr(t, 'data-panel'))).toEqual(ORDER)
    expect(ORDER.map((k) => panelTags(ts, k).find((t) => classes(t).includes('label'))!.text)).toEqual(TITLES)
    expect(html).not.toContain('throughput')
    expect(withClass(ts, 'ops-health-panels')).toHaveLength(1)
  })

  it('a panel draws one line per service in its page colour', () => {
    const names = ['s1', 's2', 's3', 's4', 's5', 's6', 's7', 's8', 's9']
    const rising = (i: number): [number, number][] => [[0, (i + 1) * 10], [60, (i + 1) * 10]]
    const ts = renderPanels(full({ req_per_min: svcs(names, rising), cpu_pct: svcs(names, rising) }))
    const strokes = (key: string) => Object.fromEntries(lines(panelTags(ts, key)).map((t) => [attr(t, 'data-line'), attr(t, 'stroke')]))
    const req = strokes('req_per_min')
    expect(Object.keys(req)).toHaveLength(9)
    expect(Object.values(req).filter((c) => c.startsWith('var(--series-'))).toHaveLength(7)
    expect(Object.entries(req).filter(([, c]) => c === 'var(--fg-4)').map(([n]) => n).sort()).toEqual(['s1', 's2'])
    expect(strokes('cpu_pct')).toEqual(req)
    const cpu = panelTags(ts, 'cpu_pct')
    expect(entries(cpu)).toHaveLength(7)
    expect(cpu.some((t) => t.text === '+2 other services')).toBe(true)
    const eight = renderPanels(full({ req_per_min: svcs(names.slice(0, 8), rising) }))
    expect(panelTags(eight, 'req_per_min').some((t) => t.text === '+1 other service')).toBe(true)
  })

  it('every panel marks each deploy', () => {
    const m = full()
    m.deploys.revisions = [
      { service: 'a', revision: 'rev-1', created_at: '2026-10-14T10:05:00Z' },
      { service: 'b', revision: 'rev-2', created_at: '2026-10-14T10:40:00Z' },
    ]
    const ts = renderPanels(m)
    for (const key of ORDER) {
      const p = panelTags(ts, key)
      const marks = p.map((t, i) => (t.name === 'line' && t.attrs.includes('data-deploy') ? p[i + 1].text : null)).filter(Boolean)
      expect(marks, key).toEqual(['rev-1 · 10:05 UTC', 'rev-2 · 10:40 UTC'])
    }
    expect(ts.some((t) => t.text === 'Deploy markers unavailable')).toBe(false)
  })

  it('unavailable deploys draw no marker and say so', () => {
    const m = full()
    m.deploys = { available: false, revisions: [] }
    const ts = renderPanels(m)
    expect(ts.filter((t) => t.attrs.includes('data-deploy'))).toHaveLength(0)
    withText(ts, 'Deploy markers unavailable')
    expect(parse(healthWith({ metrics: full() })).some((t) => t.text === 'Deploy markers unavailable')).toBe(false)
  })

  it('threshold panels draw the guide and flag HIGH', () => {
    const ts = renderPanels(full({ cpu_pct: svcs(['a'], () => [[0, 30], [60, 85]]), error_pct: svcs(['a'], () => [[0, 0.5], [60, 0.5]]) }))
    const cpu = panelTags(ts, 'cpu_pct')
    expect(guides(cpu).map((t) => attr(t, 'data-guide'))).toEqual(['80 %'])
    expect(attr(guides(cpu)[0], 'stroke-dasharray')).not.toBe('')
    expect(words(cpu).map((t) => t.text)).toEqual(['HIGH'])
    expect(style(words(cpu)[0]).color).toBe('var(--status-red-text)')
    const err = panelTags(ts, 'error_pct')
    expect(guides(err).map((t) => attr(t, 'data-guide'))).toEqual(['1 %'])
    expect(words(err)).toHaveLength(0)
    expect(guides(panelTags(ts, 'latency_p95_ms')).map((t) => attr(t, 'data-guide'))).toEqual(['1 s'])
    expect(guides(panelTags(ts, 'req_per_min'))).toHaveLength(0)
  })

  it('a panel that peaked over its threshold reads PEAKED', () => {
    const ts = renderPanels(full({ ram_pct: svcs(['a'], () => [[0, 90], [60, 40]]) }))
    const ram = panelTags(ts, 'ram_pct')
    expect(words(ram).map((t) => t.text)).toEqual(['PEAKED'])
    expect(style(words(ram)[0]).color).toBe('var(--status-amber-text)')
  })

  it('the instances panel reads each service\'s max', () => {
    const m = full({ req_per_min: svcs(['submission']), instances: svcs(['submission'], () => [[0, 1], [60, 2]]) })
    m.limits.services = [{ name: 'submission', cpu: '1', memory: '512Mi', max_instances: 2 }]
    const one = panelTags(renderPanels(m, 'submission'), 'instances')
    expect(entries(one).map((t) => attr(t, 'data-series'))).toEqual(['submission'])
    expect(one.filter((t) => t.text === '· max 2')).toHaveLength(1)
    expect(guides(one).map((t) => attr(t, 'data-guide'))).toEqual(['max 2'])
    expect(attr(guides(one)[0], 'stroke-dasharray')).not.toBe('')
    const all = panelTags(renderPanels(m, ''), 'instances')
    expect(guides(all)).toHaveLength(0)
    expect(all.filter((t) => t.text === '· max 2')).toHaveLength(1)
  })

  it('unavailable limits drop the instances max', () => {
    const m = full({ req_per_min: svcs(['submission']), instances: svcs(['submission']) })
    m.limits = { available: false, services: [{ name: 'submission', cpu: '1', memory: '512Mi', max_instances: 2 }] }
    const p = panelTags(renderPanels(m, 'submission'), 'instances')
    expect(p.some((t) => t.text.includes('max'))).toBe(false)
    expect(guides(p)).toHaveLength(0)
  })

  it('the Cloud SQL panels draw utilisation and connections against the budget', () => {
    const ts = renderPanels(full({ sql_connections: db([[0, 60], [60, 70]]) }))
    const util = panelTags(ts, 'sql_util')
    expect(lines(util).map((t) => attr(t, 'data-line'))).toEqual(['CPU', 'RAM', 'Disk'])
    expect(lines(util).map((t) => attr(t, 'stroke'))).toEqual(['var(--series-1)', 'var(--series-2)', 'var(--series-3)'])
    const conn = panelTags(ts, 'sql_connections')
    expect(lines(conn).map((t) => attr(t, 'data-line'))).toEqual(['connections'])
    expect(guides(conn).map((t) => attr(t, 'data-guide'))).toEqual(['budget 80'])
    expect(attr(guides(conn)[0], 'stroke-dasharray')).toBe('')
    expect(words(conn).map((t) => t.text)).toEqual(['HIGH'])
    for (const p of [util, conn]) expect(p.filter((t) => t.text === 'All services')).toHaveLength(1)
    const narrowed = renderPanels(full({ sql_connections: db([[0, 60], [60, 70]]) }), 'a')
    expect(lines(panelTags(narrowed, 'sql_util')).map((t) => attr(t, 'data-line'))).toEqual(['CPU', 'RAM', 'Disk'])
    expect(lines(panelTags(narrowed, 'sql_connections'))).toHaveLength(1)
    expect(panelTags(ts, 'cpu_pct').some((t) => t.text === 'All services')).toBe(false)
  })

  it('the Cloud SQL panels keep All services while a service is picked', () => {
    const ts = renderPanels(full(), 'submission')
    for (const key of ['sql_util', 'sql_connections']) expect(panelTags(ts, key).filter((t) => t.text === 'All services'), key).toHaveLength(1)
    expect(panelTags(ts, 'cpu_pct').some((t) => t.text === 'All services')).toBe(false)
  })

  it('an unavailable panel shows No data and the others draw', () => {
    const m = full()
    patch(m, 'cpu_pct', (p) => (p.available = false))
    patch(m, 'ram_pct', (p) => (p.series = []))
    const ts = renderPanels(m)
    for (const key of ['cpu_pct', 'ram_pct']) {
      const p = panelTags(ts, key)
      expect(p.filter((t) => t.attrs.includes('data-no-data')).map((t) => t.text), key).toEqual(['No data'])
      expect(p.some((t) => t.name === 'svg'), key).toBe(false)
    }
    expect(lines(panelTags(ts, 'req_per_min'))).toHaveLength(2)
  })

  it('a missing SQL source leaves the other two lines', () => {
    const m = full()
    patch(m, 'sql_disk_pct', (p) => (p.available = false))
    const util = panelTags(renderPanels(m), 'sql_util')
    expect(lines(util).map((t) => attr(t, 'data-line'))).toEqual(['CPU', 'RAM'])
    expect(entries(util).map((t) => attr(t, 'data-series'))).toEqual(['CPU', 'RAM', 'Disk'])
    const disk = util.slice(util.findIndex((t) => attr(t, 'data-series') === 'Disk'))
    expect(disk.some((t) => t.text === 'No data')).toBe(true)
    expect(util.some((t) => t.attrs.includes('data-no-data'))).toBe(false)
    const none = full()
    for (const k of ['sql_cpu_pct', 'sql_ram_pct', 'sql_disk_pct'] as const) patch(none, k, (p) => (p.available = false))
    expect(panelTags(renderPanels(none), 'sql_util').some((t) => t.attrs.includes('data-no-data'))).toBe(true)
  })

  it('the panels grid holds two tracks and collapses', () => {
    const grid = withClass(parse(healthWith({ metrics: full() })), 'ops-health-panels')
    expect(grid).toHaveLength(1)
    expect(style(grid[0])['grid-template-columns']).toBe('repeat(2, minmax(0, 1fr))')
    const css = readFileSync(new URL('./styles/support.css', import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
    const rule = css.match(/@media \(max-width: 1024px\) \{([\s\S]*?)\n\}/i)![1]
    expect(rule.match(/\.ops-health-panels[^{]*\{\s*grid-template-columns:\s*([^;]*);/i)?.[1]).toBe('minmax(0, 1fr) !important')
  })

  it('the panels sit between the Jobs row and the Services card', () => {
    const ts = parse(healthWith({ metrics: full() }))
    const at = (f: (t: Tag) => boolean) => ts.findIndex(f)
    const jobs = at((t) => attr(t, 'data-tile') === 'dead-lettered')
    const grid = at((t) => classes(t).includes('ops-health-panels'))
    const card = at((t) => t.text === 'Services')
    expect(jobs).toBeLessThan(grid)
    expect(grid).toBeLessThan(card)
  })
})

describe('TimeChart', () => {
  const base = { start: 0, end: 600, step: 60 }
  const lines = [
    { name: 'api', color: 'var(--series-1)', points: [[0, 1], [60, 2]] as [number, number][] },
    { name: 'web', color: 'var(--series-2)', points: [[0, 3], [60, 5]] as [number, number][] },
  ]
  it('TimeChart prints dated, distinct end labels for a 24 h range and clock labels for 6 h', () => {
    const labels = (start: number, end: number) => [...renderToStaticMarkup(createElement(TimeChart, { lines, start, end, step: 60 })).matchAll(/<span[^>]*>(\d\d[-:]\d\d[^<]*)<\/span>/g)].map((m) => m[1])
    const t0 = Date.UTC(2026, 9, 14, 10, 0) / 1000
    expect(labels(t0 - 86400, t0)).toEqual(['10-13 10:00', '10-14 10:00'])
    expect(labels(t0 - 6 * 3600, t0)).toEqual(['04:00 UTC', '10:00 UTC'])
  })

  it('TimeChart draws each line, guide and deploy marker', () => {
    const ts = parse(renderToStaticMarkup(createElement(TimeChart, { ...base, lines, guides: [{ value: 80, label: 'limit', dashed: true }], markers: [{ t: 120, title: 'rev-1 · 10:00 UTC' }, { t: 240, title: 'rev-2 · 11:00 UTC' }] })))
    const paths = ts.filter((t) => attr(t, 'data-line'))
    expect(paths.map((t) => [attr(t, 'data-line'), attr(t, 'stroke')])).toEqual([['api', 'var(--series-1)'], ['web', 'var(--series-2)']])
    const guides = ts.filter((t) => t.name === 'line' && t.attrs.includes('data-guide'))
    expect(guides).toHaveLength(1)
    expect(attr(guides[0], 'stroke-dasharray')).not.toBe('')
    const deploys = ts.map((t, i) => (t.name === 'line' && t.attrs.includes('data-deploy') ? ts[i + 1].text : null)).filter(Boolean)
    expect(deploys).toEqual(['rev-1 · 10:00 UTC', 'rev-2 · 11:00 UTC'])
  })
  it('TimeChart scales to cover the guide', () => {
    const html = renderToStaticMarkup(createElement(TimeChart, { ...base, lines, guides: [{ value: 80, label: 'limit', dashed: false }] }))
    const labels = parse(html).filter((t) => t.name === 'span' && t.text).map((t) => t.text)
    expect(labels[0]).toBe('100')
    expect(labels[1]).toBe('0')
  })
  it('TimeChart scales to cover the highest point', () => {
    const tall = [{ name: 'api', color: 'var(--series-1)', points: [[0, 130]] as [number, number][] }]
    const html = renderToStaticMarkup(createElement(TimeChart, { ...base, lines: tall, guides: [{ value: 80, label: 'limit', dashed: false }] }))
    expect(parse(html).filter((t) => t.name === 'span' && t.text)[0].text).toBe('200')
  })
  it('TimeChart handles no lines, all-zero values and a deploy after the end', () => {
    const empty = renderToStaticMarkup(createElement(TimeChart, { ...base, lines: [] }))
    expect(parse(empty).filter((t) => t.name === 'span' && t.text).map((t) => t.text)).toEqual(['1', '0', '00:00 UTC', '00:10 UTC'])
    expect(parse(empty).filter((t) => attr(t, 'data-line'))).toHaveLength(0)
    const zero = [{ name: 'api', color: 'var(--series-1)', points: [[0, 0], [60, 0]] as [number, number][] }]
    const html = renderToStaticMarkup(createElement(TimeChart, { ...base, lines: zero, markers: [{ t: 9999, title: 'late' }] }))
    const ts = parse(html)
    expect(attr(ts.filter((t) => attr(t, 'data-line'))[0], 'd')).toBe('M0.0 160.0 L60.0 160.0')
    const dep = ts.filter((t) => t.name === 'line' && t.attrs.includes('data-deploy'))
    expect(dep).toHaveLength(1)
    expect(attr(dep[0], 'x1')).toBe('600')
  })
  it('TimeChart lines have round caps', () => {
    const ts = parse(renderToStaticMarkup(createElement(TimeChart, { ...base, lines })))
    expect(ts.filter((t) => attr(t, 'data-line')).map((t) => attr(t, 'stroke-linecap'))).toEqual(['round', 'round'])
  })
})
