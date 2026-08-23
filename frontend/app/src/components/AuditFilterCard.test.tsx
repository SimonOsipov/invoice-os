// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Search + date-range controls only -- events, actor and company land in AUDIT-07-04..06.

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { AuditFacets } from '../lib/audit'
import { AUDIT_FILTER_DEFAULT, auditFilterQuery, type AuditFilterState } from '../lib/auditFilters'

import { AuditFilterCard } from './AuditFilterCard'

afterEach(cleanup)

function facets(): AuditFacets {
  return { event: [], actor: [], company: [] }
}

function renderCard(state: AuditFilterState = AUDIT_FILTER_DEFAULT) {
  const onChange = vi.fn()
  const utils = render(<AuditFilterCard state={state} facets={facets()} busy={false} onChange={onChange} />)
  return { ...utils, onChange }
}

describe('AuditFilterCard: search', () => {
  it('auditSearch_appliesOnEnterNotPerKeystroke', async () => {
    const user = userEvent.setup()
    const { onChange } = renderCard()

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    const input = screen.getByTestId('audit-search-input')
    await user.type(input, 'hello')
    // Control needle: prove typing actually reached the input before asserting the negative.
    expect((input as HTMLInputElement).value, 'typing must reach the input').toBe('hello')
    expect(onChange, 'no change per keystroke').not.toHaveBeenCalled()

    await user.type(input, '{Enter}')
    expect(onChange).toHaveBeenCalledTimes(1)
    expect((onChange.mock.calls[0][0] as AuditFilterState).q).toBe('hello')
  })

  it('auditSearch_statesWhatItCannotFind', () => {
    renderCard()
    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    const helper = screen.getByTestId('audit-search-helper')
    expect(helper.textContent, 'must state the invoice-number caveat').toMatch(/invoice number/)
    expect(helper.textContent, 'must state the email caveat').toMatch(/email address/)
  })

  it('auditSearch_maxLengthIs200', () => {
    renderCard()
    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    const input = screen.getByTestId('audit-search-input') as HTMLInputElement
    expect(input.maxLength, 'AC#5 pins the exact cap').toBe(200)
  })

  it('auditSearch_typedBoundaryAt200Kept201Truncated', async () => {
    const user = userEvent.setup()
    renderCard()
    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    const input = screen.getByTestId('audit-search-input') as HTMLInputElement
    await user.type(input, 'a'.repeat(200))
    expect(input.value.length, 'exactly 200 chars accepted in full').toBe(200)
    await user.type(input, 'b')
    expect(input.value.length, '201st char is rejected by the cap').toBe(200)
  })

  it('auditSearch_clearingToEmptyCommitsEmptyQOnEnter', async () => {
    const user = userEvent.setup()
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, q: 'existing' }
    const { onChange } = renderCard(state)
    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    const input = screen.getByTestId('audit-search-input') as HTMLInputElement
    expect(input.value, 'draft opens pre-filled with the current query').toBe('existing')
    await user.clear(input)
    await user.type(input, '{Enter}')
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(
      (onChange.mock.calls[0][0] as AuditFilterState).q,
      'clearing must commit an explicit empty string, not omit q',
    ).toBe('')
  })
})

describe('AuditFilterCard: date range', () => {
  it('auditDate_fourPresetsAndCustom', () => {
    renderCard()
    fireEvent.click(screen.getByTestId('audit-date-trigger'))
    const presets = screen.getAllByTestId(/^audit-date-preset-/)
    expect(presets.length, 'exactly 4 preset rows').toBe(4)

    fireEvent.click(screen.getByTestId('audit-date-preset-custom'))
    const dateInputs = screen.getAllByTestId(/^audit-date-custom-(from|to)$/)
    expect(dateInputs.length, 'exactly 2 date inputs on Custom').toBe(2)
  })

  it('auditDate_invalidCustomRangeBlocksApply', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      range: { preset: 'custom', from: '2026-08-20', to: '2026-08-10' },
    }
    renderCard(state)
    fireEvent.click(screen.getByTestId('audit-date-trigger'))

    const apply = screen.getByTestId('audit-date-apply') as HTMLButtonElement
    expect(apply.disabled, 'Apply must be disabled on an invalid range').toBe(true)
    expect(screen.getByTestId('audit-date-apply-reason'), 'a visible reason must be present').toBeTruthy()
  })

  it('auditDate_presetSendsFromOnly', () => {
    const { onChange } = renderCard()
    fireEvent.click(screen.getByTestId('audit-date-trigger'))
    fireEvent.click(screen.getByTestId('audit-date-preset-7d'))

    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    const query = auditFilterQuery(next)
    expect(query.from, 'preset must emit from').toBeTruthy()
    expect(query.to, 'preset must not emit to').toBeUndefined()
  })

  // Inherited obligation (task-653 QA): preset==='custom' with no from/to is what removing
  // the date pill produces (auditFilters.ts REMOVE_RANGE) -- it must render as no date
  // filter selected, never as Custom highlighted with two blank inputs.
  it('auditDate_presetlessCustomRendersAsNoDateFilterSelected', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, range: { preset: 'custom' } }
    renderCard(state)
    fireEvent.click(screen.getByTestId('audit-date-trigger'))

    // Population floor first -- guards the "none selected" check below against a vacuous
    // pass on an empty render.
    const presets = screen.getAllByTestId(/^audit-date-preset-/)
    expect(presets.length, 'population floor').toBe(4)
    expect(presets.every((p) => p.getAttribute('aria-pressed') !== 'true'), 'no preset row highlighted').toBe(true)

    expect(screen.queryAllByTestId(/^audit-date-custom-(from|to)$/).length, 'Custom inputs must not auto-reveal').toBe(0)
  })

  it('auditDate_presetlessCustomAlsoHidesTriggerSummary', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, range: { preset: 'custom' } }
    renderCard(state)
    const trigger = screen.getByTestId('audit-date-trigger')
    expect(trigger.textContent, 'trigger must read as no selection, not a blank "-" range').not.toMatch(/[–-]/)
  })

  // Same-day is valid per auditRangeIsValid (from <= to) -- this pins that the UI actually
  // lets it through rather than the panel implicitly treating equal dates as invalid.
  it('auditDate_sameDayCustomRangeIsValidAndAppliable', () => {
    const { onChange } = renderCard()
    fireEvent.click(screen.getByTestId('audit-date-trigger'))
    fireEvent.click(screen.getByTestId('audit-date-preset-custom'))
    fireEvent.change(screen.getByTestId('audit-date-custom-from'), { target: { value: '2026-08-20' } })
    fireEvent.change(screen.getByTestId('audit-date-custom-to'), { target: { value: '2026-08-20' } })

    const apply = screen.getByTestId('audit-date-apply') as HTMLButtonElement
    expect(apply.disabled, 'same-day range is valid, Apply must be enabled').toBe(false)
    expect(screen.queryByTestId('audit-date-apply-reason'), 'no reason shown while valid').toBeNull()

    fireEvent.click(apply)
    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    expect(next.range).toEqual({ preset: 'custom', from: '2026-08-20', to: '2026-08-20' })
  })

  // The shipped oracle only checks the reason element EXISTS (`toBeTruthy()`), which a
  // Chromium-invisible `title=`-only span would also satisfy. This pins actual visible text.
  it('auditDate_applyReasonIsVisibleTextNotTitleOnly', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      range: { preset: 'custom', from: '2026-08-20', to: '2026-08-10' },
    }
    renderCard(state)
    fireEvent.click(screen.getByTestId('audit-date-trigger'))
    const reason = screen.getByTestId('audit-date-apply-reason')
    expect(reason.textContent?.trim(), 'reason must carry real text content, not rely on title=').toBe(
      'End date must be on or after the start date.',
    )
  })

  it('auditDate_openingDatePopoverClosesAnOpenSearchPopover', () => {
    renderCard()
    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    expect(screen.getByTestId('audit-search-panel'), 'search panel opens first').toBeTruthy()

    fireEvent.click(screen.getByTestId('audit-date-trigger'))
    expect(screen.queryByTestId('audit-search-panel'), 'search panel must close when date opens').toBeNull()
    expect(screen.getByTestId('audit-date-panel'), 'date panel opens').toBeTruthy()
  })

  it('auditDate_presetRowsAndApplyAreNativeFocusableElements', () => {
    renderCard()
    fireEvent.click(screen.getByTestId('audit-date-trigger'))
    for (const id of ['24h', '7d', '30d', 'custom']) {
      expect(screen.getByTestId(`audit-date-preset-${id}`).tagName, 'preset rows must be real buttons').toBe('BUTTON')
    }
    fireEvent.click(screen.getByTestId('audit-date-preset-custom'))
    expect(screen.getByTestId('audit-date-apply').tagName, 'Apply must be a real button').toBe('BUTTON')
    expect(screen.getByTestId('audit-date-custom-from').tagName, 'From must be a real input').toBe('INPUT')
    expect(screen.getByTestId('audit-date-custom-to').tagName, 'To must be a real input').toBe('INPUT')
  })
})
