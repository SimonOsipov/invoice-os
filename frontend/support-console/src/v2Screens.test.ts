import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { readFileSync } from 'node:fs'

import { Health } from './components/Health'
import { Rules } from './components/Rules'
import { Tenants } from './components/Tenants'
import type { FleetHealth, ServiceHealth, StaffHealth, TrendPoint } from './healthApi'
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
}
const tenantsWith = (over: Partial<Parameters<typeof Tenants>[0]>) => renderToStaticMarkup(createElement(Tenants, { ...TENANTS_PROPS, ...over }))

const svc = (name: string, status: ServiceHealth['status'], build?: string): ServiceHealth => ({ name, status, build })
const point = (n: number, services_down: number | null, queue_depth: number, dead_lettered: number): TrendPoint => ({ taken_at: `2026-10-14T09:${String(n).padStart(2, '0')}:00Z`, services_down, queue_depth, dead_lettered })
const DEGRADED: FleetHealth = { status: 'degraded', services: [svc('gateway', 'up', '541ac755c7a1'), svc('docling', 'down', ''), svc('auth', 'down', 'abcdef1234')] }
const HEALTH: StaffHealth = {
  checked_at: '2026-10-14T10:05:31Z',
  fleet: DEGRADED,
  jobs: { queued: 1, submitting: 1, pending: 1, dead_lettered: 2 },
  queue_depth: 3,
  trend: [point(0, null, 1, 0), point(5, 0, 2, 1), point(10, 1, 3, 2)],
}
const HEALTH_PROPS: Parameters<typeof Health>[0] = { status: 'ready', data: HEALTH, onRetry: noop }
const healthWith = (over: Partial<Parameters<typeof Health>[0]>) => renderToStaticMarkup(createElement(Health, { ...HEALTH_PROPS, ...over }))
const healthData = (over: Partial<StaffHealth>) => healthWith({ data: { ...HEALTH, ...over } })

const SCREENS = { rules: renderToStaticMarkup(createElement(Rules, { ...RULES_PROPS })), tenants: tenantsWith({}), health: healthWith({}) }
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

  it('the tenant detail offers no action', () => {
    const html = SCREENS.tenants
    expect(html).not.toContain('View jobs')
    const detail = html.slice(html.indexOf('<h2'))
    expect(detail).not.toContain('<button')
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
    const next = ts.findIndex((t, i) => i > at && attr(t, 'data-tile') !== '')
    return ts.slice(at, next === -1 ? undefined : next)
  }
  const tilesOf = (html: string) => {
    const ts = parse(html)
    return { all: ts, fleet: tileOf(ts, 'fleet'), queue: tileOf(ts, 'queue'), dead: tileOf(ts, 'dead-lettered') }
  }
  const texts = (ts: Tag[]) => ts.map((t) => t.text)

  it('Health tiles show fleet, queue depth and dead-lettered', () => {
    const html = healthWith({})
    const { all, fleet, queue, dead } = tilesOf(html)
    expect(all.filter((t) => attr(t, 'data-tile')).map((t) => attr(t, 'data-tile'))).toEqual(['fleet', 'queue', 'dead-lettered'])
    expect(texts(fleet)).toEqual(expect.arrayContaining(['Fleet', '1/3', 'services up', 'DEGRADED', 'Down: docling, auth']))
    expect(texts(queue)).toEqual(expect.arrayContaining(['Queue depth', '3', 'jobs']))
    expect(texts(queue).filter((x) => /^[A-Z]+$/.test(x)), 'queue has no status word').toEqual([])
    expect(texts(dead)).toEqual(expect.arrayContaining(['Dead-lettered', '2', 'jobs', 'ATTENTION']))
    for (const gone of ['APP latency', 'APP error rate', 'throughput', 'Recon']) expect(html).not.toContain(gone)
  })

  it('Health tiles name the healthy and unknown states', () => {
    const ok = tilesOf(healthData({ fleet: { status: 'ok', services: [svc('gateway', 'up'), svc('auth', 'up')] }, jobs: { dead_lettered: 0 } }))
    expect(texts(ok.fleet)).toEqual(expect.arrayContaining(['2/2', 'HEALTHY']))
    expect(texts(ok.fleet).some((x) => x.startsWith('Down:'))).toBe(false)
    expect(texts(ok.dead)).toContain('CLEAR')
    const unknown = tilesOf(healthData({ fleet: { status: 'unknown', services: [] } }))
    expect(texts(unknown.fleet)).toEqual(expect.arrayContaining(['UNKNOWN', '—']))
  })

  it('a tile draws a sparkline only from two points', () => {
    const one = tilesOf(healthData({ trend: [point(0, 0, 1, 0)] }))
    for (const t of [one.fleet, one.queue, one.dead]) expect(t.some((x) => x.name === 'svg')).toBe(false)
    expect(texts(one.queue)).toContain('3')
    const three = tilesOf(healthData({ trend: [point(0, null, 1, 0), point(5, 0, 2, 1), point(10, 1, 3, 2)] }))
    for (const t of [three.fleet, three.queue, three.dead]) expect(t.filter((x) => x.name === 'svg')).toHaveLength(1)
    // services_down has 2 non-null points of 3; one non-null point of 2 draws nothing.
    const sparse = tilesOf(healthData({ trend: [point(0, null, 1, 0), point(5, 0, 2, 1)] }))
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
  })

  it('the health grid holds three columns and collapses', () => {
    const grids = withClass(parse(healthWith({})), 'ops-health-grid')
    expect(grids).toHaveLength(1)
    expect(style(grids[0])['grid-template-columns']).toBe('repeat(3, minmax(0, 1fr))')
    const css = readFileSync(new URL('./styles/support.css', import.meta.url), 'utf8')
    const rule = (width: number) => css.match(new RegExp(`@media \\(max-width: ${width}px\\) \\{([\\s\\S]*?)\\n\\}`))![1]
    const cols = (width: number) => rule(width).match(/\.ops-health-grid\s*\{\s*grid-template-columns:\s*([^;]*);/)?.[1]
    expect(cols(1024)).toBe('repeat(2, minmax(0, 1fr)) !important')
    expect(cols(768)).toBe('repeat(2, minmax(0, 1fr)) !important')
    expect(cols(480)).toBe('minmax(0, 1fr) !important')
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
