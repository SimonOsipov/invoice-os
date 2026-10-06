// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuditPager } from './AuditPager'

afterEach(cleanup)

const sv = (el: Element, prop: string) => (el.getAttribute('style') ?? '').match(new RegExp(`(?:^|;\\s*)${prop}:\\s*([^;]+)`))?.[1].trim() ?? null

function mount(over: Partial<Parameters<typeof AuditPager>[0]> = {}) {
  const props = { range: '1–25 of 300', limit: 25, canPrev: false, canNext: true, busy: false, onPrev: vi.fn(), onNext: vi.fn(), onLimit: vi.fn(), ...over }
  render(<AuditPager {...props} />)
  return props
}

describe('AuditPager', () => {
  it('auditPager_layoutFollowsThePrototype', () => {
    mount()
    const pager = screen.getByTestId('audit-pager')
    expect(sv(pager, 'padding')).toBe('12px 2px')
    expect(sv(pager, 'gap')).toBe('16px')
    expect(sv(pager, 'margin-top')).toBeNull()
    const range = pager.firstElementChild as HTMLElement
    expect(range.textContent).toBe('1–25 of 300')
    expect(range.className).toBe('mono')
    expect(sv(range, 'font-size')).toBe('11.5px')
    expect(sv(range, 'color')).toBe('var(--fg-2)')
    // Rows label is the cascade .label, and the select is 30 tall at 12.5.
    const rows = pager.querySelector('label span.label') as HTMLElement
    expect(rows.textContent).toBe('Rows')
    const select = screen.getByTestId('audit-page-size')
    expect(sv(select, 'height')).toBe('30px')
    expect(sv(select, 'font-size')).toBe('12.5px')
    // One flex row: range, then [Rows+select | Prev | Next] pushed right as siblings
    // 16px apart; a wrapper around the buttons would halve their gap.
    const kids = Array.from(pager.children)
    expect(kids.map((k) => k.getAttribute('data-testid'))).toEqual([null, null, 'audit-pager-prev', 'audit-pager-next'])
    expect(sv(kids[1], 'margin-left')).toBe('auto')
    for (const id of ['audit-pager-prev', 'audit-pager-next']) {
      const b = screen.getByTestId(id)
      expect(sv(b, 'height')).toBe('30px')
      expect(sv(b, 'font-size')).toBe('12.5px')
      expect(sv(b, 'padding'), 'the .v2-btn padding stands').toBeNull()
    }
    expect(select.className).toBe('pf-select')
    expect(sv(select, 'padding')).toBe('0px 30px 0px 10px')
    expect(select.parentElement!.querySelector('svg'), 'the select draws a chevron').toBeTruthy()
  })

  it('auditPager_disabledEndsWearTheD3RecipeAndEnabledOnesDoNot', () => {
    mount({ canPrev: false, canNext: true })
    const prev = screen.getByTestId('audit-pager-prev')
    const next = screen.getByTestId('audit-pager-next')
    expect(prev).toHaveProperty('disabled', true)
    expect(sv(prev, 'opacity')).toBe('0.45')
    expect(sv(prev, 'cursor')).toBe('not-allowed')
    expect(sv(prev, 'filter')).toBe('none')
    expect(next).toHaveProperty('disabled', false)
    expect(sv(next, 'opacity')).toBeNull()
    expect(sv(next, 'cursor')).toBeNull()
    cleanup()

    // Last page: the mirror case.
    mount({ canPrev: true, canNext: false })
    expect(sv(screen.getByTestId('audit-pager-next'), 'opacity')).toBe('0.45')
    expect(sv(screen.getByTestId('audit-pager-prev'), 'opacity')).toBeNull()
    cleanup()

    // In flight: both dead even when both ends have a page.
    const p = mount({ canPrev: true, canNext: true, busy: true })
    for (const id of ['audit-pager-prev', 'audit-pager-next']) {
      const b = screen.getByTestId(id)
      expect(b).toHaveProperty('disabled', true)
      expect(sv(b, 'opacity')).toBe('0.45')
      fireEvent.click(b)
    }
    expect(p.onPrev).not.toHaveBeenCalled()
    expect(p.onNext).not.toHaveBeenCalled()
  })
})
