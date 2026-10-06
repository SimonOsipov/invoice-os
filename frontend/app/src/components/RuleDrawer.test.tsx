// @vitest-environment jsdom
// Read from SSR markup: jsdom drops backdrop-filter and color-mix from `style`, and the
// raw attribute is what the browser gets.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { GOLDEN_RULES, GOLDEN_SET, GOLDEN_SOURCE_REF, SEED_CUSTOM_RULES, ruleJSON, type Rule } from '../lib/rules'
import { RuleDrawer } from './RuleDrawer'

afterEach(cleanup)

const SCOPE = 'Adaeze Ventures'
const golden: Rule = GOLDEN_RULES.find((r) => r.key === 'buyer.tin.format')!
const custom = SEED_CUSTOM_RULES.find((r) => r.key === 'invoice.value.cap')!

function mount(rule: Rule, isCustom: boolean, scope = SCOPE) {
  const html = renderToStaticMarkup(
    <RuleDrawer rule={rule} scope={scope} onClose={() => {}} onRemove={isCustom ? () => {} : undefined} />,
  )
  const host = document.createElement('div')
  host.innerHTML = html
  return { html, host, dialog: host.querySelector('[role="dialog"]') as HTMLElement }
}

function css(el: Element | null | undefined): Map<string, string> {
  expect(el, 'element exists').toBeTruthy()
  const style = el!.getAttribute('style') ?? ''
  return new Map(style.split(';').filter(Boolean).map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()]))
}

const own = (e: Element) =>
  Array.from(e.childNodes).filter((n) => n.nodeType === 3).map((n) => n.textContent).join('').trim()

function byOwnText(root: Element, text: string): HTMLElement {
  const hit = [...root.querySelectorAll<HTMLElement>('*')].find((e) => own(e) === text)
  expect(hit, `an element reads "${text}"`).toBeTruthy()
  return hit!
}

describe('RuleDrawer', () => {
  it('the drawer scrim is the v2 mix with both blurs', () => {
    const { html, host } = mount(golden, false)
    const scrim = css(host.firstElementChild)

    expect(scrim.get('position'), 'control: the scrim declarations are read').toBe('fixed')
    expect.soft(scrim.get('background')).toBe('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect.soft(scrim.get('backdrop-filter')).toBe('blur(6px)')
    expect.soft(scrim.get('-webkit-backdrop-filter')).toBe('blur(6px)')
    expect.soft(html).not.toContain('oklch')
  })

  it('the drawer panel has a border and no shadow', () => {
    const { dialog } = mount(custom, true)
    const panel = css(dialog)

    expect(panel.get('width'), 'control: the panel declarations are read').toBe('560px')
    expect.soft(panel.get('border-left')).toBe('1px solid var(--line-2)')
    expect.soft(panel.has('box-shadow')).toBe(false)
  })

  it('parameter boxes are plain 6px boxes', () => {
    const { host } = mount(golden, false)
    const boxes = golden.params.map((p) => byOwnText(host, p.label).nextElementSibling as HTMLElement)

    expect(golden.params.length, 'control: the rule has parameters').toBeGreaterThan(1)
    expect.soft(host.querySelectorAll('.pf-input'), 'no element wears pf-input').toHaveLength(0)
    boxes.forEach((box, i) => {
      const d = css(box)
      expect.soft(box.textContent, `box ${i} shows its value`).toBe(golden.params[i].value)
      expect.soft(d.get('border-radius'), `box ${i} radius`).toBe('var(--radius-md)')
      expect.soft(d.get('min-height'), `box ${i} min height`).toBe('36px')
      expect.soft(d.has('height'), `box ${i} has no fixed height`).toBe(false)
      expect.soft(d.get('overflow-wrap'), `box ${i} wraps long values`).toBe('anywhere')
      expect.soft(d.get('padding'), `box ${i} padding`).toBe('8px 11px')
      expect.soft(d.get('font-size'), `box ${i} font size`).toBe('12px')
      expect.soft(d.get('color'), `box ${i} colour`).toBe('var(--fg-1)')
      expect.soft(d.get('background'), `box ${i} fill`).toBe('var(--bg-2)')
      expect.soft(d.get('border'), `box ${i} border`).toBe('1px solid var(--line-2)')
      expect.soft(box.classList.contains('mono'), `box ${i} is mono`).toBe(true)
      expect.soft(box.firstElementChild ? css(box.firstElementChild).get('white-space') : undefined, `box ${i} value wraps`).not.toBe('nowrap')
    })
  })

  it('the failure message box is a plain 36px-min box', () => {
    const { host } = mount(golden, false)
    const box = byOwnText(host, 'Failure message').nextElementSibling as HTMLElement
    const d = css(box)

    expect(own(box), 'control: the box shows the message').toBe(golden.message)
    expect.soft(box.classList.contains('pf-input')).toBe(false)
    expect.soft(d.get('min-height')).toBe('36px')
    expect.soft(d.has('height')).toBe(false)
    expect.soft(d.get('overflow-wrap'), 'a long unbroken message wraps').toBe('anywhere')
    expect.soft(d.get('padding')).toBe('8px 11px')
    expect.soft(d.get('font-size')).toBe('12.5px')
    expect.soft(d.get('line-height')).toBe('1.45')
    expect.soft(d.get('border-radius')).toBe('var(--radius-md)')
    expect.soft(d.get('background')).toBe('var(--bg-2)')
    expect.soft(d.get('border')).toBe('1px solid var(--line-2)')
  })

  it('the drawer header, banner, body and footer follow the D-14 values', () => {
    const { dialog } = mount(custom, true)
    const [header, banner, body, footer] = Array.from(dialog.children) as HTMLElement[]

    expect(dialog.children, 'control: header, banner, body and footer').toHaveLength(4)
    const h = css(header)
    expect.soft(h.get('padding'), 'header padding').toBe('20px 24px 16px')
    expect.soft(h.get('gap'), 'header gap').toBe('14px')
    const keyRow = header.firstElementChild!.firstElementChild
    expect.soft(css(keyRow).get('margin-bottom'), 'key row margin').toBe('5px')

    const close = header.querySelector('button[aria-label="Close"]')
    const c = css(close)
    expect.soft(c.get('background'), 'close fill').toBe('transparent')
    expect.soft(c.get('width'), 'close width').toBe('30px')
    expect.soft(c.get('height'), 'close height').toBe('30px')
    expect.soft(c.get('border-radius'), 'close radius').toBe('var(--radius-btn)')
    expect.soft(close!.querySelector('svg')?.getAttribute('width'), 'close glyph is the 11px x').toBe('11')

    const b = css(banner)
    const bannerText = banner.firstElementChild ? css(banner.firstElementChild) : b
    expect.soft(b.get('padding'), 'banner padding').toBe('10px 24px')
    expect.soft(b.get('font-size') ?? bannerText.get('font-size'), 'banner size').toBe('12px')
    expect.soft(b.get('line-height') ?? bannerText.get('line-height'), 'banner line height').toBe('1.45')

    expect.soft(css(body).get('padding'), 'body padding').toBe('18px 24px 28px')
    expect.soft(css(footer).get('padding'), 'footer padding').toBe('14px 24px')

    const remove = byOwnText(footer, 'Remove rule')
    expect.soft(css(remove).get('border-radius'), 'remove radius').toBe('var(--radius-btn)')
  })

  // Pin, green at write: banner, JSON and footer copy are unchanged.
  it('the drawer keeps its banner, JSON and footer', () => {
    const g = mount(golden, false)
    const gBanner = g.dialog.children[1]
    expect(own(gBanner) || gBanner.textContent).toBe(
      `Managed by ASComply · inherited from golden ruleset ${GOLDEN_SET.id} ${GOLDEN_SET.version} · read-only`,
    )
    const gJson = g.dialog.querySelector('pre.pf-json')!
    expect(gJson.textContent).toBe(ruleJSON(golden, GOLDEN_SOURCE_REF, true))
    const gParsed = JSON.parse(gJson.textContent!)
    expect(Object.keys(gParsed)).toEqual(['key', 'type', 'field', 'severity', 'source', 'enabled', 'params', 'message'])
    expect(gParsed.source).toBe('ascomply/golden:NG-MBS@v8')
    const gFooter = g.dialog.children[3]
    expect(byOwnText(gFooter, 'Live status')).toBeTruthy()
    expect(byOwnText(gFooter, 'ALWAYS ON')).toBeTruthy()
    expect([...gFooter.querySelectorAll('button')].map((b) => b.textContent)).not.toContain('Remove rule')

    const c = mount(custom, true)
    const cBanner = c.dialog.children[1]
    expect(cBanner.textContent).toBe(`Custom rule · ${SCOPE} · editable, runs after the golden ruleset`)
    const cJson = c.dialog.querySelector('pre.pf-json')!
    expect(cJson.textContent).toBe(ruleJSON(custom, 'tenant:adaeze-ventures', true))
    expect(JSON.parse(cJson.textContent!).source).toBe('tenant:adaeze-ventures')
    const cFooter = c.dialog.children[3]
    expect(byOwnText(cFooter, 'LIVE')).toBeTruthy()
    expect(byOwnText(cFooter, 'Remove rule')).toBeTruthy()
  })
  it('the banner and status split by ownership', () => {
    const g = mount(golden, false)
    const gBanner = css(g.dialog.children[1])
    const gStatus = css(byOwnText(g.dialog.children[3] as HTMLElement, 'ALWAYS ON'))

    expect(g.dialog.children, 'control: header, banner, body and footer').toHaveLength(4)
    expect.soft(gBanner.get('background'), 'golden banner fill').toBe('var(--bg-3)')
    expect.soft(gBanner.get('color'), 'golden banner text').toBe('var(--fg-3)')
    expect.soft(gStatus.get('color'), 'ALWAYS ON is green').toBe('var(--status-green-text)')

    const live = mount(custom, true)
    const lBanner = css(live.dialog.children[1])
    expect.soft(lBanner.get('background'), 'custom banner fill').toBe('var(--action-tint)')
    expect.soft(lBanner.get('color'), 'custom banner text').toBe('var(--action)')
    expect.soft(css(byOwnText(live.dialog.children[3] as HTMLElement, 'LIVE')).get('color'), 'LIVE is green').toBe('var(--status-green-text)')
  })

  it('a disabled custom rule reads OFF in --fg-3, keeps Remove and has no ALWAYS ON', () => {
    const off = SEED_CUSTOM_RULES.find((r) => !r.enabled)!
    const { dialog } = mount(off, true)
    const footer = dialog.children[3] as HTMLElement

    expect(off.enabled, 'control: the fixture rule is disabled').toBe(false)
    expect.soft(css(byOwnText(footer, 'OFF')).get('color')).toBe('var(--fg-3)')
    expect.soft(footer.textContent).not.toContain('LIVE')
    expect.soft(footer.textContent).not.toContain('ALWAYS ON')
    expect.soft([...footer.querySelectorAll('button')].map((b) => b.textContent)).toEqual(['Remove rule'])
    expect.soft(JSON.parse(dialog.querySelector('pre.pf-json')!.textContent!).enabled, 'the JSON agrees with the footer').toBe(false)
  })

  it('a golden rule carries no Remove and no destructive control', () => {
    const { dialog } = mount(golden, false)

    expect(dialog.querySelectorAll('button'), 'control: only the Close button').toHaveLength(1)
    expect.soft(dialog.textContent).not.toContain('Remove rule')
    expect.soft(JSON.parse(dialog.querySelector('pre.pf-json')!.textContent!).enabled, 'golden is always enabled').toBe(true)
  })

  it('close, scrim and Remove each fire their own handler once', () => {
    const onClose = vi.fn()
    const onRemove = vi.fn()
    const { container } = render(<RuleDrawer rule={custom} scope={SCOPE} onClose={onClose} onRemove={onRemove} />)

    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose, 'Close button').toHaveBeenCalledTimes(1)
    fireEvent.click(container.firstElementChild as HTMLElement)
    expect(onClose, 'scrim').toHaveBeenCalledTimes(2)
    expect(onRemove, 'neither dismissal removes the rule').not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('Remove rule'))
    expect(onRemove, 'Remove').toHaveBeenCalledTimes(1)
    expect(onClose, 'Remove does not close by itself').toHaveBeenCalledTimes(2)
    fireEvent.click(screen.getByRole('dialog'))
    expect(onClose, 'a click inside the panel does not dismiss').toHaveBeenCalledTimes(2)
  })

  it('the panel is a modal dialog named for its rule, above its scrim', () => {
    const { host, dialog } = mount(golden, false)

    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(dialog.getAttribute('aria-label')).toBe(`Rule ${golden.key}`)
    expect.soft(Number(css(dialog).get('z-index'))).toBeGreaterThan(Number(css(host.firstElementChild).get('z-index')))
    expect.soft(css(dialog).get('background'), 'panel ground').toBe('var(--bg-1)')
  })

  it('the JSON block reads the rule: quoted params, derived keys and the tenant slug', () => {
    const quoted = SEED_CUSTOM_RULES.find((r) => r.key === 'wht.required.services')!
    const { dialog } = mount(quoted, true, 'Acme & Sons (Nig.) Ltd')
    const parsed = JSON.parse(dialog.querySelector('pre.pf-json')!.textContent!)

    expect(quoted.params.length, 'control: the rule has a quoted parameter').toBeGreaterThan(1)
    expect.soft(parsed).toEqual({
      key: 'wht.required.services',
      type: 'cross_field',
      field: 'lines[].wht',
      severity: 'warn',
      source: 'tenant:acme-sons-nig-ltd',
      enabled: false,
      params: { when: 'line.type == "service"', require: 'line.wht > 0' },
      message: 'WHT expected on service lines',
    })

    expect.soft(JSON.parse(mount(custom, true).dialog.querySelector('pre.pf-json')!.textContent!).params, 'a two-word label snake_cases').toEqual({
      max: '₦500,000,000',
      on_breach: 'warn + route to director approval',
    })

    const re = JSON.parse(mount(golden, false).dialog.querySelector('pre.pf-json')!.textContent!)
    expect.soft(re.params, 'a regex with backslashes survives').toEqual({ pattern: '^\\d{8}-\\d{4}$', flags: 'none' })
  })

  it('every golden and seeded rule renders one plain box per parameter plus the failure box', () => {
    const all = [...GOLDEN_RULES.map((r) => [r, false] as const), ...SEED_CUSTOM_RULES.map((r) => [r, true] as const)]

    expect(all.length, 'control: rules are covered').toBeGreaterThan(GOLDEN_RULES.length)
    for (const [rule, isCustom] of all) {
      const { host, dialog } = mount(rule, isCustom)
      const labels = rule.params.map((p) => byOwnText(host, p.label))
      expect.soft(rule.params.length, `${rule.key} has parameters`).toBeGreaterThan(0)
      labels.forEach((l, i) => {
        expect.soft(l.nextElementSibling?.textContent, `${rule.key} parameter ${i}`).toBe(rule.params[i].value)
      })
      expect.soft(own(byOwnText(dialog, 'Failure message').nextElementSibling!), `${rule.key} failure message`).toBe(rule.message)
      expect.soft(dialog.querySelectorAll('.pf-input'), `${rule.key} wears no pf-input`).toHaveLength(0)
    }
  })

  it('a rule with no parameters still renders the failure box and valid JSON', () => {
    const bare: Rule = { ...golden, params: [] }
    const { host, dialog } = mount(bare, false)

    expect(own(byOwnText(host, 'Failure message').nextElementSibling!), 'control: the failure box shows').toBe(golden.message)
    expect.soft(JSON.parse(dialog.querySelector('pre.pf-json')!.textContent!).params).toEqual({})
  })
})
