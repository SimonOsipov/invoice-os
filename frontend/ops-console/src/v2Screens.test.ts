import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { ApiWebhooks } from './components/ApiWebhooks'
import { Billing } from './components/Billing'
import { Evidence } from './components/Evidence'
import { Overview } from './components/Overview'
import { Status } from './components/Status'
import { Submissions } from './components/Submissions'
import { API_KEYS, EVIDENCE_DATA, JOB_FILTER_KEYS, SEED_SUBMISSIONS, WEBHOOKS } from './data'
import type { Job } from './types'

// SSR markup is the oracle for inline values; the resolved look is OPS-03 (deploy gate).
const noop = () => {}

type Tag = { name: string; attrs: string; text: string }
const attr = (t: { attrs: string }, name: string) => t.attrs.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? ''
const style = (t: Tag): Record<string, string> =>
  Object.fromEntries(
    attr(t, 'style')
      .split(';')
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)]),
  )
const classes = (t: Tag) => attr(t, 'class').split(/\s+/).filter(Boolean)
const decode = (s: string) => s.replace(/&amp;/g, '&').replace(/&quot;/g, '"')
const parse = (html: string): Tag[] =>
  [...html.matchAll(/<([a-z][a-z0-9]*)\b([^>]*)>([^<]*)/g)].map((m) => ({ name: m[1], attrs: ` ${m[2]}`, text: decode(m[3]).trim() }))
const withClass = (ts: Tag[], c: string) => ts.filter((t) => classes(t).includes(c))
const withText = (ts: Tag[], text: string) => {
  const hit = ts.filter((t) => t.text === text)
  expect(hit, `exactly one tag with text "${text}"`).toHaveLength(1)
  return hit[0]
}
const indexOfText = (ts: Tag[], text: string) => ts.indexOf(withText(ts, text))
const buttonsOf = (html: string) =>
  [...html.matchAll(/<button\b([^>]*)>([\s\S]*?)<\/button>/g)].map((m) => ({
    attrs: ` ${m[1]}`,
    text: decode(m[2].replace(/<[^>]*>/g, '')).trim(),
  }))
const buttonStyle = (b: { attrs: string }) => style({ name: 'button', attrs: b.attrs, text: '' })
const buttonByText = (html: string, text: string) => {
  const hit = buttonsOf(html).filter((b) => b.text === text)
  expect(hit, `exactly one button "${text}"`).toHaveLength(1)
  return hit[0]
}
const figure = (t: Tag) => {
  const s = style(t)
  return `${s['font-size']}/${s['font-weight']}/${s.color}`
}

const jobsView = (jobs: Job[], query = '') =>
  renderToStaticMarkup(
    createElement(Submissions, { jobs, filter: 'all', query, onFilterChange: noop, onQueryChange: noop, onOpenJob: noop, onReDriveAll: noop }),
  )
const overview = (range: '7d' | '30d' | '90d' = '30d') => renderToStaticMarkup(createElement(Overview, { range, onRangeChange: noop }))

const SCREENS = {
  overview: overview(),
  submissions: jobsView(SEED_SUBMISSIONS),
  evidence: renderToStaticMarkup(createElement(Evidence, { query: '', onQueryChange: noop, onOpen: noop, onExportAll: noop })),
  api: renderToStaticMarkup(
    createElement(ApiWebhooks, { env: 'sandbox', reveal: {}, onToggleReveal: noop, onCopyKey: noop, onRotate: noop, onAddWebhook: noop }),
  ),
  billing: renderToStaticMarkup(createElement(Billing, { onManagePlan: noop, onDownloadInvoice: noop })),
  status: renderToStaticMarkup(createElement(Status)),
}
const tagsOf = (screen: keyof typeof SCREENS) => {
  const ts = parse(SCREENS[screen])
  expect(ts.length, `${screen}: markup parsed`).toBeGreaterThan(10)
  return ts
}

type Screen = keyof typeof SCREENS
const SCREEN_KEYS = Object.keys(SCREENS) as Screen[]

describe('v2 screens', () => {
  it('SC-01 every screen opens on a 22 / 10 clearance header with a 28 / -0.04em h1 and no inline weight', () => {
    for (const screen of SCREEN_KEYS) {
      const ts = tagsOf(screen)
      expect(classes(ts[0]), `${screen}: root`).toContain('ops-screen-pad')
      expect.soft(style(ts[1])['margin-bottom'], `${screen}: header row marginBottom`).toBe('22px')
      const eyebrows = withClass(ts, 'eyebrow')
      expect(eyebrows, `${screen}: one eyebrow`).toHaveLength(1)
      expect(ts.indexOf(eyebrows[0]), `${screen}: the eyebrow sits in the header row`).toBeLessThanOrEqual(3)
      expect.soft(style(eyebrows[0])['margin-bottom'], `${screen}: eyebrow marginBottom`).toBe('10px')

      const h1s = ts.filter((t) => t.name === 'h1')
      expect(h1s, `${screen}: one h1`).toHaveLength(1)
      expect(ts.indexOf(h1s[0]), `${screen}: the h1 follows the eyebrow`).toBe(ts.indexOf(eyebrows[0]) + 1)
      const h1 = style(h1s[0])
      expect.soft(h1['font-size'], `${screen}: h1 size`).toBe('28px')
      expect.soft(h1['letter-spacing'], `${screen}: h1 tracking`).toBe('-0.04em')
      expect.soft(Object.keys(h1), `${screen}: h1 inline weight (the layer's 700 applies)`).not.toContain('font-weight')
    }
  })

  it('SC-02 card and section titles are 16 / 700 / -0.02em on --ink; panel titles 14 / 700', () => {
    const cardTitles = ['API requests over time', 'Spend over time', 'Submission outcomes', 'Top rejection reasons', 'Clearance latency']
    const ov = tagsOf('overview')
    expect(withClass(ov, 'card-title'), 'the five card titles keep the class').toHaveLength(cardTitles.length)
    for (const text of cardTitles) {
      const t = withText(ov, text)
      const s = style(t)
      expect(classes(t), `${text}: class`).toContain('card-title')
      expect.soft(s['font-size'], `${text}: size`).toBe('16px')
      expect.soft(s['letter-spacing'], `${text}: tracking`).toBe('-0.02em')
      expect.soft(s['font-weight'] ?? '700', `${text}: weight (class carries 700)`).toBe('700')
      expect.soft(s.color ?? 'var(--ink)', `${text}: colour`).toBe('var(--ink)')
    }

    for (const text of cardTitles) expect.soft(style(withText(ov, text))['line-height'], `${text}: line-height`).toBe('normal')

    const plain: [Screen, string][] = [
      ['api', 'API keys'],
      ['api', 'Webhook endpoints'],
      ['billing', 'Itemized spend · July 2026'],
      ['billing', 'Invoices from ASComply'],
      ['status', 'Incident history'],
    ]
    for (const [screen, text] of plain) {
      const s = style(withText(tagsOf(screen), text))
      expect.soft([s['font-size'], s['font-weight'], s['letter-spacing'], s.color], `${screen} "${text}"`).toEqual(['16px', '700', '-0.02em', 'var(--ink)'])
    }

    for (const text of ['Recent deliveries', 'Recent API requests']) {
      const s = style(withText(tagsOf('api'), text))
      expect.soft([s['font-size'], s['font-weight'], s.color], `api panel "${text}"`).toEqual(['14px', '700', 'var(--ink)'])
    }
    const scale = style(withText(tagsOf('billing'), 'Scale'))
    expect.soft([scale['font-size'], scale['font-weight'], scale['letter-spacing'], scale.color], 'billing plan name').toEqual(['22px', '700', '-0.03em', 'var(--ink)'])
  })

  it('SC-03 stat and KPI figures are .money with the prototype size, weight and colour; no figure stays .mono', () => {
    const expected: Record<Screen, string[]> = {
      overview: [...Array(6).fill('24px/700/var(--ink)'), '22px/700/var(--ink)', '22px/700/var(--ink)', '16px/600/var(--fg-3)', '22px/700/var(--status-green-text)', '30px/700/var(--ink)'],
      submissions: ['20px/700/var(--fg-1)', '20px/700/var(--status-green-text)', '20px/700/var(--status-red-text)', '20px/700/var(--status-red-text)'],
      evidence: [],
      api: ['26px/700/var(--ink)'],
      billing: ['30px/700/var(--ink)', '18px/700/var(--ink)', '18px/700/var(--ink)', '18px/700/var(--action)'],
      status: ['22px/700/var(--status-amber-text)'],
    }
    let money = 0
    for (const screen of SCREEN_KEYS) {
      const ts = tagsOf(screen)
      const figures = withClass(ts, 'money')
      money += figures.length
      expect.soft(figures.map(figure).sort(), `${screen}: .money figures`).toEqual([...expected[screen]].sort())
      for (const f of figures) expect.soft(Object.keys(style(f)), `${screen}: .money sets no tracking (the class does)`).not.toContain('letter-spacing')
      const bigMono = withClass(ts, 'mono').filter((t) => parseFloat(style(t)['font-size'] ?? '0') >= 16)
      expect.soft(bigMono.map((t) => t.text), `${screen}: a figure of 16px or more left on .mono`).toEqual([])
    }
    expect(money, 'control: figures were collected').toBe(Object.values(expected).flat().length)
  })

  it('SC-04 mono stays for IDs, hashes and meta', () => {
    const rows = parse(SCREENS.submissions)
    const firstRow = rows.findIndex((t) => classes(t).includes('ops-row'))
    expect(firstRow, 'control: a job row exists').toBeGreaterThan(0)
    const jobId = rows.slice(firstRow).find((t) => classes(t).includes('mono'))
    expect(classes(jobId!), 'job id').toContain('mono')
    expect(jobId!.text, 'smoke reads the first .mono of a row as the job id').toBe(SEED_SUBMISSIONS[0].id)

    const ev = tagsOf('evidence')
    const irn = withText(ev, EVIDENCE_DATA[0].irn)
    expect(classes(irn), 'IRN').toContain('mono')
    expect(style(irn).color, 'IRN colour').toBe('var(--link)')
    expect(classes(withText(ev, EVIDENCE_DATA[0].invoice)), 'invoice number').toContain('mono')

    const ov = tagsOf('overview')
    const deltas = ov.flatMap((t, i) => (figure(t).startsWith('24px/700') ? [ov.slice(i + 1).find((n) => classes(n).includes('mono'))!] : []))
    expect(deltas, 'control: six KPI deltas').toHaveLength(6)
    for (const d of deltas) {
      expect.soft(style(d)['font-weight'], `KPI delta ${d.text}`).toBe('600')
      expect.soft(style(d)['font-size'], `KPI delta ${d.text} size`).toBe('11px')
    }
  })

  it('SC-05 Overview: KPI height, headers, rejection bars, ELEVATED dot, swatches and axis labels', () => {
    const ts = tagsOf('overview')
    const cards = ts.filter((t) => style(t)['min-height'] === '124px')
    expect(cards, 'KPI cards at minHeight 124').toHaveLength(6)

    expect.soft(style(withText(ts, 'API requests over time'))['margin-bottom'], 'requests title').toBe('4px')
    expect.soft(style(withText(ts, 'Submission outcomes'))['margin-bottom'], 'outcomes title').toBe('4px')
    expect.soft(style(ts[indexOfText(ts, 'Spend over time') - 1])['margin-bottom'], 'Spend header').toBe('6px')
    expect.soft(style(ts[indexOfText(ts, 'Top rejection reasons') - 1])['margin-bottom'], 'Rejections header').toBe('6px')
    expect.soft(style(ts[indexOfText(ts, 'Clearance latency') - 1])['margin-bottom'], 'Latency header stays 4').toBe('4px')
    expect.soft(style(ts.find((t) => t.text.startsWith('Errors ASComply caught'))!)['font-size'], 'rejections body copy').toBe('12.5px')

    const tracks = ts.flatMap((t, i) => (style(t).height === '7px' && style(t).overflow === 'hidden' ? [i] : []))
    expect(tracks.length, 'control: rejection bar tracks').toBeGreaterThanOrEqual(3)
    for (const i of tracks) {
      expect.soft(style(ts[i])['border-radius'], `rejection track #${i}`).toBe('2px')
      expect.soft(style(ts[i + 1]).height, `rejection fill #${i} is the next tag`).toBe('100%')
      expect.soft(Object.keys(style(ts[i + 1])), `rejection fill #${i} corners`).not.toContain('border-radius')
    }

    const dot = ts[indexOfText(ts, 'ELEVATED') - 1]
    expect.soft([style(dot).width, style(dot)['border-radius']], 'ELEVATED dot').toEqual(['7px', '50%'])

    const swatches = ts.filter((t) => ['9px', '10px'].includes(style(t).width ?? '') && style(t).width === style(t).height)
    expect(swatches.length, 'control: legend swatches').toBeGreaterThanOrEqual(6)
    for (const s of swatches) expect.soft(style(s)['border-radius'], 'legend swatch').toBe('2px')
    const columns = ts.filter((t) => style(t).overflow === 'hidden' && style(t)['flex-direction'] === 'column')
    expect(columns.length, 'control: outcome columns').toBeGreaterThanOrEqual(10)
    for (const c of columns) expect.soft(style(c)['border-radius'], 'outcome column').toBe('2px')

    const axes = ts.flatMap((t, i) => (style(t)['margin-top'] === '8px' && style(t)['justify-content'] === 'space-between' ? [i] : []))
    expect(axes, 'a request-axis row and a latency-range row').toHaveLength(2)
    const labels = [...ts.slice(axes[0] + 1, axes[0] + 6), ...ts.slice(axes[1] + 1, axes[1] + 3)]
    expect(withText(labels, '30d ago') && withText(labels, 'today'), 'control: the latency labels are in the slice').toBeDefined()
    expect(labels, 'five axis labels and two range labels').toHaveLength(7)
    for (const l of labels) expect.soft(style(l).color, `axis label "${l.text}"`).toBe('var(--fg-3)')
    expect(ts.filter((t) => attr(t, 'style').includes('--fg-4')), 'no --fg-4 on this screen').toEqual([])
  })

  it.each([
    ['7d', '7D'],
    ['30d', '30D'],
    ['90d', '90D'],
  ] as const)('SC-06 the %s range control is a DS segmented control: the active segment is --primary, 4px, no ops-btn', (range, active) => {
    const html = overview(range)
    const ts = parse(html)
    const segments = buttonsOf(html).filter((b) => ['7D', '30D', '90D'].includes(b.text))
    expect(segments.map((b) => b.text), 'three segments').toEqual(['7D', '30D', '90D'])
    for (const b of segments) {
      const s = buttonStyle(b)
      expect.soft(attr(b, 'class'), `${b.text}: ops-btn forces 7px`).not.toContain('ops-btn')
      expect.soft(s['border-radius'], `${b.text}: corner`).toBe('var(--radius-sm)')
      expect.soft(s.transition, `${b.text}: transition`).toContain('background var(--dur-fast) var(--ease-out)')
      const on = b.text === active
      expect.soft(s.background, `${b.text}: background`).toBe(on ? 'var(--primary)' : 'transparent')
      expect.soft(s.color, `${b.text}: colour`).toBe(on ? 'var(--primary-foreground)' : 'var(--fg-3)')
    }
    const first = ts.findIndex((t) => t.name === 'button' && t.attrs.includes('font-family:var(--font-mono)'))
    const track = style(ts[first - 1])
    expect.soft([track.gap, track.background, track.border, track['border-radius']], 'range track').toEqual(['2px', 'var(--sage)', '1px solid var(--sage-card-border)', 'var(--radius-btn)'])
  })

  it('SC-06 API: the requests panel header is a plain block, as in the prototype', () => {
    const ts = tagsOf('api')
    const header = ts[ts.indexOf(withText(ts, 'Recent API requests')) - 1]
    expect(header.name).toBe('div')
    expect(style(header).display, 'requests header display').toBeUndefined()
    const deliveries = ts[ts.indexOf(withText(ts, 'Recent deliveries')) - 1]
    expect(style(deliveries).display, 'deliveries header display').toBe('flex')
  })

  it('SC-07 Submissions: Re-drive all, chips, state pills, sub-stat tiles, head and the empty state', () => {
    const html = SCREENS.submissions
    const ts = tagsOf('submissions')
    const redrive = buttonStyle(buttonByText(html, 'Re-drive all'))
    expect.soft([redrive.background, redrive.color, redrive['border-radius']], 'Re-drive all').toEqual(['var(--destructive)', 'var(--destructive-foreground)', 'var(--radius-btn)'])

    const chips = withClass(ts, 'ops-chip')
    expect(chips, 'an All chip and one per state').toHaveLength(1 + JOB_FILTER_KEYS.length)
    for (const c of chips) {
      expect.soft(style(c)['border-radius'], 'chip corner').toBe('var(--radius-sm)')
      expect.soft(style(c).padding, 'chip padding').toBe('0 11px')
    }

    // Inactive counts inherit the chip's --fg-3 at full strength (4.5:1 floor); opacity drops them to ~3:1.
    chips.forEach((c, i) => {
      const count = ts[ts.indexOf(c) + 1]
      expect(count.text, `chip ${i} count text`).toMatch(/^\d+$/)
      expect.soft(Object.keys(style(count)), `chip ${i} count`).not.toContain('opacity')
    })
    for (const c of chips.slice(1)) expect.soft(style(c).color, 'inactive chip colour').toBe('var(--fg-3)')

    const pills = ts.filter((t) => style(t).padding === '2px 7px')
    expect(pills, 'one state pill per job').toHaveLength(SEED_SUBMISSIONS.length)
    for (const p of pills) {
      expect.soft(style(p)['border-radius'], 'state pill corner').toBe('var(--radius-sm)')
      const dot = ts[ts.indexOf(p) + 1]
      expect.soft([style(dot).width, style(dot)['border-radius']], 'state pill dot').toEqual(['6px', '50%'])
    }

    const tiles = ts.filter((t) => style(t)['min-width'] === '96px')
    expect(tiles, 'four sub-stat tiles').toHaveLength(4)
    for (const t of tiles) expect.soft([style(t)['border-radius'], Object.keys(style(t))], 'tile').toEqual(['var(--radius-md)', expect.not.arrayContaining(['box-shadow'])])

    const head = withClass(ts, 'ops-jobs-table').find((t) => !classes(t).includes('ops-row'))
    expect(head, 'table head').toBeDefined()
    expect.soft(style(head!).background, 'head background').toBe('var(--bg-3)')

    const emptyTs = parse(jobsView(SEED_SUBMISSIONS, 'zz-no-match'))
    const empty = emptyTs.find((t) => t.text.startsWith('No submissions match'))
    expect(empty, 'empty state renders').toBeDefined()
    expect.soft(style(empty!).color, 'empty state text').toBe('var(--fg-3)')
    expect.soft(withClass(emptyTs, 'ops-jobs-table'), 'the table frame stays').toHaveLength(1)
    expect(withClass(emptyTs, 'ops-row'), 'no row matches').toHaveLength(0)
  })

  it('SC-08 Evidence: header badge, intro, Export all, IRN, bundle badge and head', () => {
    const html = SCREENS.evidence
    const ts = tagsOf('evidence')
    const lock = ts.find((t) => style(t).background === 'var(--status-muted-bg)')
    expect(lock, 'header badge').toBeDefined()
    const badge = style(lock!)
    expect.soft([badge['border-radius'], badge.padding, badge.color], 'header badge').toEqual(['var(--radius-sm)', '6px 11px', 'var(--fg-2)'])
    const intro = style(ts.find((t) => t.text.startsWith('Every cleared invoice'))!)
    expect.soft([intro['font-size'], intro['line-height']], 'intro').toEqual(['13.5px', '1.55'])
    const exp = buttonByText(html, 'Export all')
    expect.soft([attr(exp, 'class'), buttonStyle(exp).padding, buttonStyle(exp)['font-size']], 'Export all').toEqual(['ops-btn v2-btn v2-btn-ghost', '0 14px', '13px'])
    const head = withClass(ts, 'ops-evidence-table')
    expect(head, 'table head').toHaveLength(1)
    expect.soft(style(head[0]).background, 'head background').toBe('var(--bg-3)')

    const bundles = ts.filter((t) => style(t).padding === '2px 7px')
    expect(bundles, 'one bundle badge per row').toHaveLength(EVIDENCE_DATA.length)
    for (const b of bundles) {
      expect.soft([style(b)['border-radius'], style(b).color], 'bundle badge').toEqual(['var(--radius-sm)', 'var(--status-green-text)'])
    }
  })

  it('SC-09 API: key tags, key box, Rotate, Add endpoint, ACTIVE pills and the rate bar', () => {
    const html = SCREENS.api
    const ts = tagsOf('api')
    const pills = ts.filter((t) => style(t).padding === '2px 7px')
    expect(pills.length, 'two key tags, one ACTIVE pill per endpoint, one env tag per endpoint').toBe(2 + 2 * WEBHOOKS.length)
    for (const p of pills) {
      expect.soft(style(p)['border-radius'], 'pill corner').toBe('var(--radius-sm)')
    }
    const dots = ts.filter((t) => style(t).width === '6px' && style(t).height === '6px')
    expect(dots.length, 'a dot per key tag and per ACTIVE pill').toBe(2 + WEBHOOKS.length)
    for (const d of dots) expect.soft(style(d)['border-radius'], 'dot').toBe('50%')

    const boxes = ts.filter((t) => style(t).padding === '9px 12px')
    expect(boxes, 'one key box per key').toHaveLength(2)
    for (const b of boxes) expect.soft([style(b).gap, style(b)['border-radius']], 'key box').toEqual(['10px', 'var(--radius-md)'])

    const rotates = buttonsOf(html).filter((b) => b.text === 'Rotate')
    expect(rotates, 'one Rotate per key').toHaveLength(2)
    for (const r of rotates) {
      const s = buttonStyle(r)
      expect.soft(attr(r, 'class'), 'Rotate class').toBe('ops-btn v2-btn v2-btn-ghost')
      expect.soft([s.height, s.padding, s['font-size'], s.gap], 'Rotate').toEqual(['30px', '0 11px', '12px', '6px'])
    }
    const add = buttonByText(html, 'Add endpoint')
    expect.soft([buttonStyle(add).padding, buttonStyle(add)['font-size']], 'Add endpoint').toEqual(['0 12px', '13px'])

    const heads = withClass(ts, 'ops-keys-table')
    expect(heads.length, 'a delivery head, then its rows').toBeGreaterThanOrEqual(2)
    expect.soft(style(heads[0]).background, 'delivery head (the first) background').toBe('var(--bg-3)')

    const track = ts.find((t) => style(t).height === '6px' && style(t).overflow === 'hidden')
    expect(track, 'rate bar track').toBeDefined()
    expect.soft(style(track!)['border-radius'], 'rate track').toBe('2px')
    const fill = style(ts[ts.indexOf(track!) + 1])
    expect.soft(fill.height, 'the next tag is the fill').toBe('100%')
    expect.soft(Object.keys(fill), 'rate fill corners').not.toContain('border-radius')
    for (const k of API_KEYS) expect.soft(style(withText(ts, k.name))['font-size'], `key name "${k.name}"`).toBe('13.5px')
  })

  it('SC-10 Billing: grid clearance, plan tag, quota bar, swatches, status pills, heads and buttons', () => {
    const html = SCREENS.billing
    const ts = tagsOf('billing')
    expect.soft(style(withClass(ts, 'ops-billing-grid')[0])['margin-bottom'], 'grid marginBottom').toBe('26px')
    const active = style(withText(ts, 'ACTIVE'))
    expect.soft([active.border, active['border-radius'], active.padding], 'plan tag').toEqual(['1px solid var(--action-border)', 'var(--radius-sm)', '2px 7px'])
    const over = style(withText(ts, 'OVER QUOTA'))
    expect.soft([over['border-radius'], over.padding], 'OVER QUOTA').toEqual(['var(--radius-sm)', '2px 7px'])
    const bar = ts.find((t) => style(t).height === '12px')
    expect.soft(style(bar!)['border-radius'], 'usage bar').toBe('2px')
    const swatches = ts.filter((t) => style(t).width === '9px' && style(t).height === '9px')
    expect(swatches, 'two swatches').toHaveLength(2)
    for (const s of swatches) expect.soft(style(s)['border-radius'], 'swatch').toBe('2px')

    const pills = ts.filter((t) => style(t).padding === '2px 7px' && t.name === 'span' && !classes(t).includes('mono'))
    expect(pills.length, 'a status pill per past invoice').toBeGreaterThanOrEqual(3)
    for (const p of pills) expect.soft(style(p)['border-radius'], 'status pill').toBe('var(--radius-sm)')

    const heads = [...withClass(ts, 'ops-usage-table'), ...withClass(ts, 'ops-invoice-table')].filter((t) => style(t).background !== undefined)
    expect(heads, 'usage head, total row and invoice head').toHaveLength(3)
    for (const h of heads) expect.soft(style(h).background, 'head or total row').toBe('var(--bg-3)')

    const manage = buttonByText(html, 'Manage plan')
    expect.soft(buttonStyle(manage)['font-size'], 'Manage plan').toBe('13px')
    const pdf = buttonsOf(html).filter((b) => b.text === 'PDF')
    expect(pdf.length, 'a PDF button per invoice').toBeGreaterThanOrEqual(3)
    for (const b of pdf) {
      expect.soft(attr(b, 'class'), 'PDF class').toBe('ops-btn v2-btn v2-btn-ghost')
      expect.soft([buttonStyle(b).height, buttonStyle(b).padding, buttonStyle(b)['font-size'], buttonStyle(b).gap], 'PDF').toEqual(['28px', '0 10px', '11.5px', '6px'])
    }
  })

  it('SC-11 Status: banner, badges, uptime labels, components card and incidents', () => {
    const ts = tagsOf('status')
    const tile = ts.find((t) => style(t).width === '40px' && style(t).height === '40px')
    expect(tile, 'banner icon tile').toBeDefined()
    expect.soft([style(tile!).color, style(tile!)['border-radius']], 'icon tile').toEqual(['var(--primary-foreground)', 'var(--radius-md)'])
    const title = style(withText(ts, 'Partial degradation — tax-authority latency elevated'))
    expect.soft(title['font-weight'], 'banner title').toBe('700')
    const sub = style(withText(ts, '5 of 6 components operational · clearance times above target'))
    expect.soft(Object.keys(sub), 'banner subtitle').not.toContain('opacity')

    const badges = ts.filter((t) => style(t).padding === '3px 8px')
    expect(badges.length, 'a badge per component').toBe(6)
    for (const b of badges) {
      expect.soft(style(b)['border-radius'], 'badge corner').toBe('var(--radius-sm)')
      const dot = ts[ts.indexOf(b) + 1]
      expect.soft([style(dot).width, style(dot)['border-radius']], 'badge dot').toEqual(['7px', '50%'])
    }
    const card = ts.find((t) => style(t).overflow === 'hidden' && style(t).border === '1px solid var(--line-1)')
    expect.soft(style(card!)['margin-bottom'], 'components card').toBe('26px')

    const ago = ts.filter((t) => t.text === '90 days ago')
    const uptime = ts.filter((t) => t.text.endsWith(' uptime') && t.name === 'span')
    expect(ago, 'a "90 days ago" per component').toHaveLength(6)
    expect(uptime, 'an uptime label per component').toHaveLength(6)
    for (const l of [...ago, ...uptime]) expect.soft(style(l).color, `label "${l.text}"`).toBe('var(--fg-3)')

    const incidents = withClass(ts, 'ops-incident-row')
    expect(incidents.length, 'incident rows').toBeGreaterThanOrEqual(3)
    const details = ts.filter((t) => style(t)['line-height'] === '1.55')
    expect(details, 'one detail per incident').toHaveLength(incidents.length)
    for (const d of details) expect.soft(style(d)['font-size'], 'incident detail').toBe('13px')
    const status = ts.filter((t) => ['MONITORING', 'RESOLVED'].includes(t.text))
    expect(status.length, 'incident status badges').toBe(incidents.length)
    for (const s of status) expect.soft([style(s)['border-radius'], style(s).padding], 'incident badge').toEqual(['var(--radius-sm)', '2px 7px'])
  })

  it('SC-12 the real class locators keep their names and every occurrence', () => {
    const n = SEED_SUBMISSIONS.length
    const keep: Record<Screen, Record<string, number>> = {
      overview: { 'ops-screen-pad': 1, 'ops-kpi-strip': 1, 'ops-overview-grid': 2 },
      submissions: { 'ops-screen-pad': 1, 'ops-sub-stats': 1, 'ops-chip': 1 + JOB_FILTER_KEYS.length, 'ops-row': n, 'ops-jobs-table': 1 + n, 'ops-input': 2 },
      evidence: { 'ops-screen-pad': 1, 'ops-row': EVIDENCE_DATA.length, 'ops-evidence-table': 1, 'ops-input': 2 },
      api: { 'ops-screen-pad': 1, 'ops-api-grid': 2, 'ops-keys-table': 6, 'ops-webhook-table': 6 },
      billing: { 'ops-screen-pad': 1, 'ops-billing-grid': 1, 'ops-billing-kpis': 1, 'ops-usage-table': 6, 'ops-invoice-table': 5 },
      status: { 'ops-screen-pad': 1, 'ops-incident-row': 3 },
    }
    for (const screen of SCREEN_KEYS) {
      const seen = tagsOf(screen).flatMap(classes)
      for (const [c, count] of Object.entries(keep[screen])) expect.soft(seen.filter((x) => x === c).length, `${screen}: .${c} count`).toBe(count)
    }
  })
})
