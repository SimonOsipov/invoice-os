// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Search + date-range controls only -- events, actor and company land in AUDIT-07-04..06.

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { AuditFacets } from '../lib/audit'
import { AUDIT_FILTER_DEFAULT, auditFilterQuery, type AuditFilterState } from '../lib/auditFilters'
import { AUDIT_EVENTS, type AuditDomain } from '../lib/auditVocabulary'

import { AuditFilterCard } from './AuditFilterCard'

afterEach(cleanup)

function facets(): AuditFacets {
  return { event: [], actor: [], company: [] }
}

function renderCard(state: AuditFilterState = AUDIT_FILTER_DEFAULT, customFacets: AuditFacets = facets()) {
  const onChange = vi.fn()
  const utils = render(<AuditFilterCard state={state} facets={customFacets} busy={false} onChange={onChange} />)
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

// AUDIT-07-04 (task-656): event-type control -- ten domains, live counts, All/Clear, sticky
// Clear all. RED -- AuditFilterCard has no event-type popover yet.
const DOMAIN_ORDER: AuditDomain[] = [
  'invoices',
  'approvals',
  'policies',
  'roles',
  'companies',
  'documents',
  'memberships',
  'validation',
  'submissions',
  'reconciliation',
]

const DOMAIN_HEADING_TEXT: Record<AuditDomain, string> = {
  invoices: 'Invoices',
  approvals: 'Approvals',
  policies: 'Policies',
  roles: 'Roles',
  companies: 'Companies',
  documents: 'Documents',
  memberships: 'Memberships',
  validation: 'Validation rules',
  submissions: 'Submissions',
  reconciliation: 'Reconciliation',
}

function idsInDomain(domain: AuditDomain): string[] {
  return Object.entries(AUDIT_EVENTS)
    .filter(([, def]) => def.domain === domain)
    .map(([id]) => id)
}

function openEventPopover() {
  fireEvent.click(screen.getByTestId('audit-event-trigger'))
}

describe('AuditFilterCard: event type', () => {
  it('auditEventFilter_tenGroupsCoverAllThirtySix', () => {
    renderCard()
    openEventPopover()

    const groups = screen.getAllByTestId(/^audit-event-group-.*-heading$/)
    expect(groups.length, 'population floor: ten group headings').toBe(10)

    const rows = screen.getAllByTestId(/^audit-event-row-/)
    expect(rows.length, 'population floor: 36 rows').toBe(36)

    const flattenedIds = rows.map((r) => r.getAttribute('data-testid')!.replace('audit-event-row-', ''))
    expect(flattenedIds, 'the union of every group must equal the vocabulary exactly').toEqual(Object.keys(AUDIT_EVENTS))
  })

  it('auditEventFilter_groupOrderIsFixed', () => {
    function headingTexts(): (string | null)[] {
      openEventPopover()
      const headings = screen.getAllByTestId(/^audit-event-group-.*-heading$/)
      expect(headings.length, 'population floor: ten group headings').toBe(10)
      return headings.map((h) => h.textContent)
    }

    const expected = DOMAIN_ORDER.map((d) => DOMAIN_HEADING_TEXT[d])

    const first = renderCard()
    const firstOrder = headingTexts()
    expect(firstOrder, 'first render must follow the declared domain order').toEqual(expected)
    first.unmount()

    renderCard()
    const secondOrder = headingTexts()
    expect(secondOrder, 'a second render must produce the identical order').toEqual(expected)
  })

  it('auditEventFilter_typeAbsentFromFacetsShowsZeroAndStaysSelectable', () => {
    const f = facets()
    f.event = Object.keys(AUDIT_EVENTS)
      .filter((id) => id !== 'document.reused')
      .map((id) => ({ value: id, name: null, count: 1 }))
    const { onChange } = renderCard(AUDIT_FILTER_DEFAULT, f)
    openEventPopover()

    const count = screen.getByTestId('audit-event-count-document.reused')
    expect(count.textContent, 'a bucket-less type renders 0').toBe('0')

    const row = screen.getByTestId('audit-event-row-document.reused') as HTMLButtonElement
    expect(row.disabled, 'a zero-count type must stay selectable').toBe(false)

    fireEvent.click(row)
    expect(onChange, 'clicking it must still fire a change -- proving it is genuinely interactive').toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    expect(next.events).toContain('document.reused')
  })

  it('auditEventFilter_documentReadIsPresent', () => {
    renderCard()
    openEventPopover()
    expect(screen.getByTestId('audit-event-row-document.read'), 'document.read must render (Decision Q2)').toBeTruthy()
    const label = screen.getByTestId('audit-event-label-document.read')
    expect(label.textContent).toBe('Document opened')
  })

  it('auditEventFilter_groupAllSelectsOnlyThatGroup', () => {
    const approvalIds = idsInDomain('approvals')
    expect(approvalIds.length, 'sanity: approvals has 4 ids').toBe(4)

    const { onChange } = renderCard()
    openEventPopover()
    fireEvent.click(screen.getByTestId('audit-event-group-approvals-all'))

    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    expect([...next.events].sort(), "All must select exactly that group's ids").toEqual([...approvalIds].sort())
  })

  it('auditEventFilter_groupClearRemovesOnlyThatGroup', () => {
    const approvalIds = idsInDomain('approvals')
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, events: [...approvalIds, 'invoice.created'] }
    const { onChange } = renderCard(state)
    openEventPopover()
    fireEvent.click(screen.getByTestId('audit-event-group-approvals-clear'))

    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    expect(next.events, "control needle: the untouched invoice selection must survive").toContain('invoice.created')
    expect(next.events, 'Clear must remove only that group').toEqual(['invoice.created'])
  })

  it('auditEventFilter_clearAllEmptiesOnlyEvents', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      events: ['invoice.created', 'document.read'],
      q: 'acme',
      company: { mode: 'workspace' },
    }
    const { onChange } = renderCard(state)
    openEventPopover()
    fireEvent.click(screen.getByTestId('audit-event-clear-all'))

    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    expect(next.q, 'control needle: q must survive untouched').toBe('acme')
    expect(next.company, 'control needle: company must survive untouched').toEqual({ mode: 'workspace' })
    expect(next.events, 'events alone must empty').toEqual([])
  })

  it('auditEventFilter_rendersLabelsNeverIdentifiers', () => {
    renderCard()
    openEventPopover()

    const labels = screen.getAllByTestId(/^audit-event-label-/)
    expect(labels.length, 'population floor: 36 labels').toBe(36)

    const texts = labels.map((l) => l.textContent ?? '')
    expect(texts, 'control needle: a known human label must be present').toContain('Transmission failed')
    expect(texts.every((t) => !t.includes('.')), 'no raw identifier as the primary label').toBe(true)
  })

  // The story's most important honesty constraint (CA-4, D-2): the count must come from
  // facets.event, never from anything else the component could plausibly derive a number
  // from. Every decoy below is deliberately distinct from the two real counts.
  it('auditEventFilter_countsComeFromTheFacetNotAnyOtherDerivation', () => {
    const f = facets()
    f.event = [
      { value: 'invoice.created', name: null, count: 999 },
      { value: 'document.created', name: null, count: 5 },
    ]
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      events: ['workflow_role.created', 'approval_policy.created', 'membership.suspended'],
    }
    renderCard(state, f)
    openEventPopover()

    const created = screen.getByTestId('audit-event-count-invoice.created')
    expect(created.textContent, 'control needle: the real facet count must render').toBe('999')
    expect(created.textContent, "must not be the OTHER bucket's count").not.toBe('5')
    expect(created.textContent, 'must not be the sum of every bucket').not.toBe('1004')
    expect(created.textContent, 'must not be facets.event.length').not.toBe('2')
    expect(created.textContent, 'must not be the number of currently-selected events').not.toBe(
      String(state.events.length),
    )

    const documentCreated = screen.getByTestId('audit-event-count-document.created')
    expect(documentCreated.textContent, "a second bucket must show its own count, not the first row's").toBe('5')
  })
})
