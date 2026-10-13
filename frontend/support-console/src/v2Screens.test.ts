import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { Rules } from './components/Rules'
import { SEED_RULES } from './rulesFixture'
import type { RuleVersion } from './rulesApi'
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

const SCREENS = { rules: renderToStaticMarkup(createElement(Rules, { ...RULES_PROPS })) }
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
    const loading = rulesWith({ codeLists: null, codeListsNote: 'Your account has no rules role.' })
    expect(loading).toContain('Your account has no rules role.')
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
})
