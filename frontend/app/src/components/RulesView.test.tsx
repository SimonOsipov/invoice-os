// @vitest-environment jsdom
// A real (hand-off) session's Rules screen shows its own empty lines, not the demo rules and suggestions.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { GOLDEN_RULES, GOLDEN_VERSIONS, SEED_CUSTOM_RULES, SUGGESTED_RULES, type CustomRule } from '../lib/rules'
import type { PlatformCtx } from '../types'
import { RulesView } from './RulesView'

function rulesCtx(handoff: boolean): PlatformCtx {
  const ctx = {
    mode: 'firm',
    handoff,
    active: { short: 'Adaeze Ventures', initials: 'AV', tin: '', entityId: null },
    customRules: handoff ? [] : SEED_CUSTOM_RULES,
    openRuleKey: null,
    openRule: vi.fn(),
    closeRule: vi.fn(),
    addSuggestedRule: vi.fn(),
    toggleCustomRule: vi.fn(),
    removeCustomRule: vi.fn(),
  }
  return ctx as unknown as PlatformCtx
}

afterEach(cleanup)

describe('RulesView', () => {
  it('hand-off: Rules shows no custom rule and no suggestion', () => {
    render(<RulesView ctx={rulesCtx(true)} />)
    expect(screen.getByText('No suggestions to show.')).toBeTruthy()
    expect(screen.getByText('No custom rules yet — the golden ruleset alone is running.')).toBeTruthy()
    expect(SUGGESTED_RULES.length).toBeGreaterThan(0)
    SUGGESTED_RULES.forEach((s) => expect(screen.queryByText(s.key)).toBeNull())
    expect(screen.queryByText(/Derived from/)).toBeNull()
    expect(screen.queryAllByText('CUSTOM')).toHaveLength(0)
    expect(screen.queryAllByText('Add as custom rule')).toHaveLength(0)
  })

  // Control: green before and after; the total is arithmetic over ctx.customRules.
  it('hand-off: the total counts the golden rules only', () => {
    render(<RulesView ctx={rulesCtx(true)} />)
    expect(screen.getAllByText(`${GOLDEN_RULES.length} RULES`).length).toBeGreaterThan(0)
    expect(screen.getByText(/\+ 0 CUSTOM/)).toBeTruthy()
    SEED_CUSTOM_RULES.forEach((r) => expect(screen.queryByText(r.key)).toBeNull())
  })

  // Control: green before and after.
  it('persona: Rules keeps the seeded rules and suggestions', () => {
    render(<RulesView ctx={rulesCtx(false)} />)
    expect(screen.getAllByText('CUSTOM')).toHaveLength(5)
    expect(SEED_CUSTOM_RULES).toHaveLength(5)
    expect(screen.getByText('Derived from 9 rejections on your invoices')).toBeTruthy()
    expect(screen.getAllByText('Add as custom rule')).toHaveLength(3)
    expect(screen.getAllByText(`${GOLDEN_RULES.length + 5} RULES`).length).toBeGreaterThan(0)
    expect(screen.queryByText('No suggestions to show.')).toBeNull()
  })
})

describe('RulesView, adversarial', () => {
  const stored: CustomRule[] = [{ ...SEED_CUSTOM_RULES[0], key: 'mine.only' }]

  function ctxWith(handoff: boolean, customRules: CustomRule[]): PlatformCtx {
    return { ...(rulesCtx(handoff) as unknown as Record<string, unknown>), customRules } as unknown as PlatformCtx
  }

  it("hand-off: a stored rule renders, the empty row goes, and the total counts it", () => {
    render(<RulesView ctx={ctxWith(true, stored)} />)
    expect(screen.getByText('mine.only')).toBeTruthy()
    expect(screen.getAllByText('CUSTOM')).toHaveLength(1)
    expect(screen.queryByText('No custom rules yet — the golden ruleset alone is running.')).toBeNull()
    expect(screen.getAllByText(`${GOLDEN_RULES.length + 1} RULES`).length).toBeGreaterThan(0)
    expect(screen.getByText('No suggestions to show.')).toBeTruthy()
  })

  // Control: persona copy for the empty custom list and for exhausted suggestions is unchanged.
  it('persona: an empty custom list keeps the full empty line', () => {
    render(<RulesView ctx={ctxWith(false, [])} />)
    expect(screen.getByText('No custom rules yet — the golden ruleset alone is running. Add one from the suggestions on the left.')).toBeTruthy()
    expect(screen.queryByText('No custom rules yet — the golden ruleset alone is running.')).toBeNull()
    expect(screen.getAllByText('Add as custom rule')).toHaveLength(3)
  })

  it('persona: every suggestion adopted keeps the "Nothing to suggest" line', () => {
    const adopted = SUGGESTED_RULES.map((s) => ({ ...SEED_CUSTOM_RULES[0], key: s.key }))
    render(<RulesView ctx={ctxWith(false, adopted)} />)
    expect(screen.getByText(/^Nothing to suggest right now/)).toBeTruthy()
    expect(screen.queryByText('No suggestions to show.')).toBeNull()
    expect(screen.getAllByText('CUSTOM')).toHaveLength(SUGGESTED_RULES.length)
  })
})

// The raw style attribute is read from SSR markup: jsdom's `style` drops properties it does not know.
function mountSsr(ctx: PlatformCtx): HTMLElement {
  const host = document.createElement('div')
  host.innerHTML = renderToStaticMarkup(<RulesView ctx={ctx} />)
  return host
}

function css(el: Element | null | undefined): Map<string, string> {
  expect(el, 'element exists').toBeTruthy()
  const style = el!.getAttribute('style') ?? ''
  return new Map(style.split(';').filter(Boolean).map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()]))
}

const own = (e: Element) =>
  Array.from(e.childNodes).filter((n) => n.nodeType === 3).map((n) => n.textContent).join('').trim()

function allByOwnText(root: Element, match: string | RegExp): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>('*')].filter((e) => (typeof match === 'string' ? own(e) === match : match.test(own(e))))
}

function byOwnText(root: Element, match: string | RegExp): HTMLElement {
  const hits = allByOwnText(root, match)
  expect(hits.length, `an element reads ${String(match)}`).toBeGreaterThan(0)
  return hits[0]
}

describe('RulesView, reskin surface', () => {
  it('headings carry no inline weight', () => {
    const host = mountSsr(rulesCtx(false))
    const h1 = host.querySelector('h1')!
    const inForce = byOwnText(host, 'Rules in force')
    const suggested = byOwnText(host, 'Suggested for you')

    expect(own(h1), 'control: the h1 is read').toBe('Rules')
    expect.soft(css(h1).has('font-weight'), 'h1 inherits its weight').toBe(false)
    expect.soft(inForce.classList.contains('card-title'), 'Rules in force is a card title').toBe(true)
    expect.soft(css(inForce).has('font-weight'), 'Rules in force has no inline weight').toBe(false)
    expect.soft(css(inForce).has('font-family'), 'Rules in force has no inline family').toBe(false)
    expect.soft(css(suggested).get('font-weight'), 'Suggested for you').toBe('700')
  })

  it('pills are 4px, LOCKED is --fg-3, the knob is round', () => {
    const host = mountSsr(rulesCtx(false))
    const count = byOwnText(host, 'Suggested for you').nextElementSibling
    const stack = byOwnText(host, /^GOLDEN v8 \+ 5 CUSTOM$/)
    const versionTag = byOwnText(host, GOLDEN_VERSIONS[0].tag).parentElement
    const locked = allByOwnText(host, 'LOCKED')
    const knobs = [...host.querySelectorAll('.pf-knob')]
    const suggest = allByOwnText(host, 'Add as custom rule')

    expect(locked, 'control: one LOCKED per golden rule').toHaveLength(GOLDEN_RULES.length)
    expect(knobs, 'control: one knob per custom rule').toHaveLength(SEED_CUSTOM_RULES.length)
    expect(suggest, 'control: one suggest button per suggestion').toHaveLength(3)
    expect.soft(css(count).get('border-radius'), 'suggest count').toBe('var(--radius-sm)')
    expect.soft(css(count).has('margin-left'), 'suggest count is not pushed right').toBe(false)
    expect.soft(css(stack).get('border-radius'), 'stack pill').toBe('var(--radius-sm)')
    expect.soft(css(versionTag).get('border-radius'), 'version tag').toBe('var(--radius-sm)')
    locked.forEach((l, i) => expect.soft(css(l).get('color'), `LOCKED ${i}`).toBe('var(--fg-3)'))
    knobs.forEach((k, i) => {
      expect.soft(css(k).get('border-radius'), `knob ${i} radius`).toBe('50%')
      expect.soft(css(k).has('box-shadow'), `knob ${i} has no shadow`).toBe(false)
    })
    suggest.forEach((b, i) => expect.soft(css(b).get('border-radius'), `suggest button ${i}`).toBe('var(--radius-btn)'))
  })

  // Pin: the toggle track is exempt.
  it('the toggle track stays a 99px pill', () => {
    const tracks = [...mountSsr(rulesCtx(false)).querySelectorAll('.pf-toggle')]

    expect(tracks).toHaveLength(SEED_CUSTOM_RULES.length)
    tracks.forEach((t, i) => expect.soft(css(t).get('border-radius'), `track ${i}`).toBe('99px'))
  })

  it('the header, rail and table follow the D-34 values', () => {
    const host = mountSsr(rulesCtx(false))
    const h1 = host.querySelector('h1')!
    const left = h1.parentElement!
    const row = left.parentElement!
    const total = row.lastElementChild
    const versionText = byOwnText(host, GOLDEN_VERSIONS[0].version)
    const suggestHead = byOwnText(host, 'Suggested for you').parentElement
    const footer = byOwnText(host, /^Custom rules evaluate after the golden ruleset/)

    expect(own(total!), 'control: the header count is read').toBe(`${GOLDEN_RULES.length + 5} RULES`)
    expect.soft(css(row).get('gap'), 'header row gap').toBe('20px')
    expect.soft(css(row).get('flex-wrap'), 'carve-out: the row still wraps at narrow widths').toBe('wrap')
    expect.soft(css(left).get('min-width'), 'left column').toBe('0')
    expect.soft(css(left.firstElementChild).get('margin-bottom'), 'eyebrow margin').toBe('7px')
    expect.soft(css(total).has('letter-spacing'), 'header count has no tracking').toBe(false)
    expect.soft(css(total).get('flex'), 'header count does not shrink').toBe('none')
    expect.soft(css(versionText).has('flex'), 'version text does not stretch').toBe(false)
    expect.soft(css(versionText.parentElement).get('gap'), 'version row gap').toBe('10px')
    expect.soft(css(suggestHead).get('gap'), 'suggest head gap').toBe('8px')
    expect.soft(css(footer).get('padding'), 'table footer padding').toBe('12px 16px')
    expect.soft(css(footer).get('line-height'), 'table footer line height').toBe('1.5')
    expect.soft(css(footer).has('background'), 'table footer has no fill').toBe(false)
  })

  it('the empty suggestion and empty custom boxes follow the D-34 values', () => {
    const adopted = SUGGESTED_RULES.map((s) => ({ ...SEED_CUSTOM_RULES[0], key: s.key }))
    const noSuggest = byOwnText(mountSsr({ ...(rulesCtx(false) as unknown as Record<string, unknown>), customRules: adopted } as unknown as PlatformCtx), /^Nothing to suggest right now/)
    const noCustom = byOwnText(mountSsr({ ...(rulesCtx(false) as unknown as Record<string, unknown>), customRules: [] } as unknown as PlatformCtx), /^No custom rules yet/)
    const box = css(noCustom).has('padding') ? noCustom : noCustom.parentElement!

    expect.soft(css(noSuggest).get('padding'), 'empty suggestions padding').toBe('12px 14px')
    expect.soft(css(box).get('padding'), 'empty custom padding').toBe('18px 16px')
    expect.soft(css(noCustom).get('line-height') ?? css(box).get('line-height'), 'empty custom line height').toBe('1.55')
  })
})

describe('RulesView, rows and drawer wiring', () => {
  function ctxWith(over: Record<string, unknown>): PlatformCtx {
    return { ...(rulesCtx(false) as unknown as Record<string, unknown>), ...over } as unknown as PlatformCtx
  }

  it('golden rows lock, custom rows switch, and only custom rows carry a switch', () => {
    render(<RulesView ctx={rulesCtx(false)} />)
    const switches = screen.getAllByRole('switch')

    expect(switches, 'control: one switch per custom rule').toHaveLength(SEED_CUSTOM_RULES.length)
    expect(screen.getAllByText('LOCKED'), 'one lock per golden rule').toHaveLength(GOLDEN_RULES.length)
    switches.forEach((sw, i) => {
      const r = SEED_CUSTOM_RULES[i]
      expect.soft(sw.getAttribute('aria-checked'), r.key).toBe(String(r.enabled))
      expect.soft(sw.getAttribute('aria-label'), r.key).toBe(`${r.enabled ? 'Disable' : 'Enable'} ${r.key}`)
      expect.soft(sw.style.background, `${r.key} track`).toBe(r.enabled ? 'var(--action)' : 'var(--line-3)')
      expect.soft((sw.firstElementChild as HTMLElement).style.transform, `${r.key} knob`).toBe(r.enabled ? 'translateX(14px)' : 'translateX(0)')
    })
    GOLDEN_RULES.forEach((r) => {
      const row = screen.getByText(r.key).closest('.pf-row') as HTMLElement
      expect.soft(within(row).queryByRole('switch'), `${r.key} has no switch`).toBeNull()
      expect.soft(within(row).getByText('GOLDEN'), `${r.key} source`).toBeTruthy()
    })
  })

  it('a click on a row opens it, and a click on its switch toggles without opening', () => {
    const ctx = rulesCtx(false)
    render(<RulesView ctx={ctx} />)
    const target = SEED_CUSTOM_RULES[2]

    fireEvent.click(screen.getByRole('switch', { name: `Disable ${target.key}` }))
    expect(ctx.toggleCustomRule).toHaveBeenCalledWith(target.key)
    expect(ctx.openRule, 'the switch click stops at the switch').not.toHaveBeenCalled()
    fireEvent.click(screen.getByText(target.key))
    expect(ctx.openRule).toHaveBeenCalledWith(target.key)
    fireEvent.click(screen.getByText(GOLDEN_RULES[2].key))
    expect(ctx.openRule).toHaveBeenLastCalledWith(GOLDEN_RULES[2].key)
    expect(ctx.toggleCustomRule, 'opening never toggles').toHaveBeenCalledTimes(1)
  })

  it('a suggestion button adopts its own suggestion', () => {
    const ctx = rulesCtx(false)
    render(<RulesView ctx={ctx} />)
    const buttons = screen.getAllByRole('button', { name: 'Add as custom rule' })

    expect(buttons, 'control: one button per suggestion').toHaveLength(SUGGESTED_RULES.length)
    fireEvent.click(buttons[1])
    expect(ctx.addSuggestedRule).toHaveBeenCalledTimes(1)
    expect(ctx.addSuggestedRule).toHaveBeenCalledWith(SUGGESTED_RULES[1])
  })

  it('an open golden key opens a read-only drawer; an open custom key opens one that removes that rule', () => {
    const golden = render(<RulesView ctx={ctxWith({ openRuleKey: GOLDEN_RULES[1].key })} />)
    const gDialog = screen.getByRole('dialog')
    expect(gDialog.getAttribute('aria-label')).toBe(`Rule ${GOLDEN_RULES[1].key}`)
    expect(within(gDialog).queryByText('Remove rule'), 'a golden drawer cannot remove').toBeNull()
    expect(within(gDialog).getByText('ALWAYS ON')).toBeTruthy()
    golden.unmount()

    const remove = vi.fn()
    const target = SEED_CUSTOM_RULES[3]
    render(<RulesView ctx={ctxWith({ openRuleKey: target.key, removeCustomRule: remove })} />)
    const cDialog = screen.getByRole('dialog')
    expect(cDialog.getAttribute('aria-label')).toBe(`Rule ${target.key}`)
    expect(screen.getAllByRole('dialog'), 'exactly one drawer').toHaveLength(1)
    fireEvent.click(within(cDialog).getByText('Remove rule'))
    expect(remove).toHaveBeenCalledTimes(1)
    expect(remove).toHaveBeenCalledWith(target.key)
  })

  it('no drawer opens for no key or for a key that names no rule', () => {
    const none = render(<RulesView ctx={rulesCtx(false)} />)
    expect(screen.getAllByText('LOCKED'), 'control: the view rendered').not.toHaveLength(0)
    expect(screen.queryByRole('dialog')).toBeNull()
    none.unmount()

    render(<RulesView ctx={ctxWith({ openRuleKey: 'no.such.rule' })} />)
    expect(screen.getAllByText('LOCKED'), 'control: the view rendered').not.toHaveLength(0)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('the subtitle names the scope for a firm and for an in-house workspace', () => {
    const firm = render(<RulesView ctx={rulesCtx(false)} />)
    expect(screen.getByText("ASComply's golden ruleset plus the custom checks you run for Adaeze Ventures")).toBeTruthy()
    firm.unmount()

    render(<RulesView ctx={ctxWith({ mode: 'inhouse' })} />)
    expect(screen.getByText("ASComply's golden ruleset plus the custom checks Adaeze Ventures runs internally")).toBeTruthy()
  })
})

// Values are the Platform prototype's seed (goldenRules, seedCustomRules, ruleSuggestions), not read back from lib/rules.
describe('RulesView, prototype seed', () => {
  const GOLDEN = [
    ['buyer.tin.required', 'required', 'buyer.tin', 'ERROR', 'Buyer TIN is mandatory'],
    ['buyer.tin.format', 'regex', 'buyer.tin', 'ERROR', 'TIN must match 00000000-0000'],
    ['vat.math', 'expression-CEL', 'totals.vat', 'ERROR', 'VAT must equal 7.5% of taxable base'],
    ['line.description.required', 'required', 'lines[].description', 'ERROR', 'Every line needs a description'],
    ['currency.enum', 'enum', 'header.currency', 'ERROR', 'Currency must be NGN, USD or EUR'],
    ['invoice.no.unique', 'expression-CEL', 'header.invoice_no', 'ERROR', 'Invoice number must be unique per seller'],
    ['issue.date.sequence', 'date_rule', 'header.issue_date', 'WARN', 'Issue date must not precede prior invoice'],
  ]
  const CUSTOM = [
    ['po.number.required', 'required', 'header.po_number', 'ERROR', 'Purchase-order number is required on every invoice'],
    ['buyer.approved.list', 'enum', 'buyer.tin', 'ERROR', 'Buyer must be on the approved customer list'],
    ['cost.centre.required', 'required', 'lines[].cost_centre', 'WARN', 'Every line needs a cost centre'],
    ['invoice.value.cap', 'range', 'totals.gross', 'WARN', 'Invoices above ₦500M need director approval'],
    ['wht.required.services', 'cross_field', 'lines[].wht', 'WARN', 'WHT expected on service lines'],
  ]

  it('the table rows carry the prototype key, type, field, severity and message', () => {
    const host = mountSsr(rulesCtx(false))
    const rows = [...host.querySelectorAll('.pf-row')].map((r) => {
      const c = [...r.children].map((e) => e.textContent)
      return [c[0], c[1], c[2], c[3], c[5]]
    })

    expect(rows, 'control: every golden and custom row renders').toHaveLength(GOLDEN.length + CUSTOM.length)
    expect(rows).toEqual([...GOLDEN, ...CUSTOM])
  })

  it('the rail carries the prototype versions and suggestions', () => {
    const host = mountSsr(rulesCtx(false))
    const text = host.textContent!

    expect(text, 'control: the rail rendered').toContain('Golden ruleset · NG-MBS')
    expect.soft(text).toContain('v8IN USEeff. 2026-06-01 · 7 rules')
    expect.soft(text).toContain('v7SUPERSEDEDeff. 2026-04-15 · 40 rules')
    expect.soft(text).toContain('buyer.email.formatDerived from 9 rejections on your invoicesAdd as custom rule')
    expect.soft(text).toContain('lines[].hsn.requiredDerived from 6 rejections on your invoicesAdd as custom rule')
    expect.soft(text).toContain('fx.rate.rangeDerived from 4 rejections on your USD invoicesAdd as custom rule')
    expect.soft(text).toContain('Published and maintained by ASComply. New versions arrive automatically — you never edit these.')
    expect.soft(text).toContain('Custom rules evaluate after the golden ruleset. A golden rule can never be disabled or edited.')
    expect.soft(text).toContain('INHERITED · GOLDEN RULESET NG-MBS v8')
    expect.soft(text).toContain('CUSTOM · Adaeze Ventures')
  })
})
