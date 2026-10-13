// @vitest-environment jsdom
// Style pins read SSR markup: jsdom drops backdrop-filter and color-mix from `style`.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ADMIN_ONLY_REASON, ruleJSON, type CustomRule, type GoldenRule } from '../lib/rulesApi'
import { RuleDrawer, type RuleDrawerActions } from './RuleDrawer'

afterEach(cleanup)

const GOLDEN_BANNER = 'Managed by ASComply · golden ruleset v4 · read-only'
const CUSTOM_BANNER = 'Custom rule · FIRM-WIDE · runs after the golden ruleset'

const golden: GoldenRule = {
  key: 'buyer-tin-format',
  type: 'format/regex',
  target: 'buyer.tin',
  params: { pattern: '^\\d{8}-\\d{4}$', flags: ['i'], limit: 3 },
  severity: 'error',
  when: null,
  message: 'TIN must match 00000000-0000',
}

const custom: CustomRule = {
  id: '3f2a0000-0000-0000-0000-000000000001',
  key: 'custom:3f2a0000-0000-0000-0000-000000000001',
  revision: 2,
  revision_id: 'rev-2',
  entity_id: null,
  type: 'range',
  target: 'total',
  params: { max: 500000000 },
  severity: 'warning',
  when: 'has(invoice.buyer)',
  message: 'Invoices above 500M need director approval',
  enabled: true,
  updated_at: '2026-10-12T00:00:00Z',
  updated_by: 'u1',
}

function actions(over: Partial<RuleDrawerActions> = {}): RuleDrawerActions {
  return { disabled: false, reason: null, removing: false, error: null, onRemove: () => {}, ...over }
}

function mount(rule: GoldenRule | CustomRule, act?: RuleDrawerActions) {
  const html = renderToStaticMarkup(
    <RuleDrawer rule={rule} banner={act ? CUSTOM_BANNER : GOLDEN_BANNER} onClose={() => {}} actions={act} />,
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
    const { html, host } = mount(golden)
    const scrim = css(host.firstElementChild)

    expect(scrim.get('position'), 'control: the scrim declarations are read').toBe('fixed')
    expect.soft(scrim.get('background')).toBe('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect.soft(scrim.get('backdrop-filter')).toBe('blur(6px)')
    expect.soft(scrim.get('-webkit-backdrop-filter')).toBe('blur(6px)')
    expect.soft(html).not.toContain('oklch')
  })

  it('the drawer panel has a border and no shadow', () => {
    const panel = css(mount(custom, actions()).dialog)

    expect(panel.get('width'), 'control: the panel declarations are read').toBe('560px')
    expect.soft(panel.get('border-left')).toBe('1px solid var(--line-2)')
    expect.soft(panel.has('box-shadow')).toBe(false)
  })

  it('ruleDrawer_goldenIsReadOnly: banner, params from the object, no button but Close', () => {
    const { dialog } = mount(golden)

    expect(dialog.children[1].textContent).toBe(GOLDEN_BANNER)
    expect(dialog.querySelectorAll('button'), 'only the Close button').toHaveLength(1)
    expect(dialog.textContent).not.toContain('Remove rule')
    expect(byOwnText(dialog, 'ALWAYS ON')).toBeTruthy()
    const boxes = ['pattern', 'flags', 'limit'].map((label) => byOwnText(dialog, label).nextElementSibling as HTMLElement)
    expect(boxes.map((b) => b.textContent)).toEqual(['^\\d{8}-\\d{4}$', '["i"]', '3'])
    expect(byOwnText(dialog, 'Failure message').nextElementSibling?.textContent).toBe(golden.message)
    expect(dialog.querySelector('pre.pf-json')!.textContent).toBe(ruleJSON(golden))
  })

  it('parameter boxes are plain 6px boxes', () => {
    const { host } = mount(golden)
    const boxes = ['pattern', 'flags', 'limit'].map((label) => byOwnText(host, label).nextElementSibling as HTMLElement)

    boxes.forEach((box, i) => {
      const d = css(box)
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
    })
    expect(host.querySelectorAll('.pf-input'), 'no element wears pf-input').toHaveLength(0)
  })

  it('the failure message box is a plain 36px-min box', () => {
    const box = byOwnText(mount(golden).host, 'Failure message').nextElementSibling as HTMLElement
    const d = css(box)

    expect(own(box), 'control: the box shows the message').toBe(golden.message)
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
    const { dialog } = mount(custom, actions())
    const [header, banner, body, footer] = Array.from(dialog.children) as HTMLElement[]

    expect(dialog.children, 'control: header, banner, body and footer').toHaveLength(4)
    const h = css(header)
    expect.soft(h.get('padding'), 'header padding').toBe('20px 24px 16px')
    expect.soft(h.get('gap'), 'header gap').toBe('14px')
    expect.soft(css(header.firstElementChild!.firstElementChild).get('margin-bottom'), 'key row margin').toBe('5px')

    const close = header.querySelector('button[aria-label="Close"]')
    const c = css(close)
    expect.soft(c.get('background'), 'close fill').toBe('transparent')
    expect.soft(c.get('width'), 'close width').toBe('30px')
    expect.soft(c.get('height'), 'close height').toBe('30px')
    expect.soft(c.get('border-radius'), 'close radius').toBe('var(--radius-btn)')
    expect.soft(close!.querySelector('svg')?.getAttribute('width'), 'close glyph is the 11px x').toBe('11')

    const b = css(banner)
    expect.soft(b.get('padding'), 'banner padding').toBe('10px 24px')
    expect.soft(b.get('font-size'), 'banner size').toBe('12px')
    expect.soft(b.get('line-height'), 'banner line height').toBe('1.45')

    expect.soft(css(body).get('padding'), 'body padding').toBe('18px 24px 28px')
    expect.soft(css(footer).get('padding'), 'footer padding').toBe('14px 24px')
    expect.soft(css(byOwnText(footer, 'Remove rule')).get('border-radius'), 'remove radius').toBe('var(--radius-btn)')
  })

  it('a custom rule: banner, Applies when, JSON and footer come from the server object', () => {
    const { dialog } = mount(custom, actions())

    expect(dialog.children[1].textContent).toBe(CUSTOM_BANNER)
    expect(byOwnText(dialog, 'max').nextElementSibling?.textContent).toBe('500000000')
    expect(byOwnText(dialog, 'Applies when').nextElementSibling?.textContent).toBe('has(invoice.buyer)')
    expect(dialog.querySelector('pre.pf-json')!.textContent).toBe(ruleJSON(custom))
    expect(byOwnText(dialog.children[3] as HTMLElement, 'LIVE')).toBeTruthy()
    expect([...dialog.children[3].querySelectorAll('button')].map((b) => b.textContent)).toEqual(['Remove rule'])
  })

  it('a rule with no when draws no Applies when block; a key shows its label with the full key as title', () => {
    const { dialog } = mount({ ...custom, when: null }, actions())

    expect(dialog.textContent).not.toContain('Applies when')
    const key = byOwnText(dialog, 'Custom rule')
    expect(key.getAttribute('title')).toBe(custom.key)
  })

  it('the banner and status split by ownership', () => {
    const g = mount(golden)
    const gBanner = css(g.dialog.children[1])
    expect.soft(gBanner.get('background'), 'golden banner fill').toBe('var(--bg-3)')
    expect.soft(gBanner.get('color'), 'golden banner text').toBe('var(--fg-3)')
    expect.soft(css(byOwnText(g.dialog.children[3] as HTMLElement, 'ALWAYS ON')).get('color'), 'ALWAYS ON is green').toBe('var(--status-green-text)')

    const live = mount(custom, actions())
    const lBanner = css(live.dialog.children[1])
    expect.soft(lBanner.get('background'), 'custom banner fill').toBe('var(--action-tint)')
    expect.soft(lBanner.get('color'), 'custom banner text').toBe('var(--action)')
    expect.soft(css(byOwnText(live.dialog.children[3] as HTMLElement, 'LIVE')).get('color'), 'LIVE is green').toBe('var(--status-green-text)')
  })

  it('a disabled custom rule reads OFF in --fg-3', () => {
    const { dialog } = mount({ ...custom, enabled: false }, actions())
    const footer = dialog.children[3] as HTMLElement

    expect.soft(css(byOwnText(footer, 'OFF')).get('color')).toBe('var(--fg-3)')
    expect.soft(footer.textContent).not.toContain('LIVE')
    expect.soft(footer.textContent).not.toContain('ALWAYS ON')
    expect([...footer.querySelectorAll('button')].map((b) => b.textContent), 'an off rule keeps Remove').toEqual(['Remove rule'])
  })

  it('Remove rule is disabled with the reason for a non-admin, and while its request runs', () => {
    for (const a of [actions({ disabled: true, reason: ADMIN_ONLY_REASON }), actions({ removing: true })]) {
      const btn = byOwnText(mount(custom, a).dialog, 'Remove rule')
      expect(btn.hasAttribute('disabled')).toBe(true)
      expect(btn.getAttribute('title')).toBe(a.reason)
    }
  })

  it('Edit draws only when the editor is wired, and is disabled with Remove', () => {
    expect(mount(custom, actions()).dialog.querySelector('[data-testid="rule-edit"]')).toBeNull()
    const edit = mount(custom, actions({ disabled: true, reason: ADMIN_ONLY_REASON, onEdit: () => {} })).dialog.querySelector('[data-testid="rule-edit"]')!
    expect(edit.hasAttribute('disabled')).toBe(true)
    expect(edit.getAttribute('title')).toBe(ADMIN_ONLY_REASON)
  })

  it('a remove failure shows the sentence above the footer', () => {
    const { dialog } = mount(custom, actions({ error: 'custom rule not found' }))
    expect(dialog.querySelector('[data-testid="rule-drawer-error"]')?.textContent).toBe('custom rule not found')
  })

  it('close, scrim and Remove each fire their own handler once', () => {
    const onClose = vi.fn()
    const onRemove = vi.fn()
    const { container } = render(<RuleDrawer rule={custom} banner={CUSTOM_BANNER} onClose={onClose} actions={actions({ onRemove })} />)

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
    const { host, dialog } = mount(golden)

    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(dialog.getAttribute('aria-label')).toBe(`Rule ${golden.key}`)
    expect.soft(Number(css(dialog).get('z-index'))).toBeGreaterThan(Number(css(host.firstElementChild).get('z-index')))
    expect.soft(css(dialog).get('background'), 'panel ground').toBe('var(--bg-1)')
  })

  it('a rule with no parameters still renders the failure box and the JSON', () => {
    const { host, dialog } = mount({ ...golden, params: {} })

    expect(own(byOwnText(host, 'Failure message').nextElementSibling!), 'control: the failure box shows').toBe(golden.message)
    expect(JSON.parse(dialog.querySelector('pre.pf-json')!.textContent!).params).toEqual({})
  })
})
