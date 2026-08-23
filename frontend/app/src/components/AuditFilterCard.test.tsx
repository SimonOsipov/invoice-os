// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Search, date-range, event-type and actor controls. Company lands in AUDIT-07-06.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

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
// Clear all.
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

  // Adversarial coverage added at QA (task-656 Mode B) -- the 9 specs above are the
  // architect's RED table; these extend it.

  it('auditEventFilter_unknownFacetIdentifierIsDroppedNotRendered', () => {
    const f = facets()
    f.event = [
      { value: 'invoice.created', name: null, count: 2 },
      { value: 'legacy.retired_event', name: null, count: 9 },
    ]
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openEventPopover()

    const rows = screen.getAllByTestId(/^audit-event-row-/)
    expect(rows.length, 'the vocabulary drives rows, not the facet array -- still exactly 36').toBe(36)
    expect(
      screen.queryByTestId('audit-event-row-legacy.retired_event'),
      'an id outside the vocabulary gets no row',
    ).toBeNull()
    expect(
      document.body.textContent,
      'the unknown identifier must not leak into the panel as text',
    ).not.toContain('legacy.retired_event')
  })

  it('auditEventFilter_deselectingOneRowAfterSelectingWholeGroupKeepsTheRest', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const { rerender } = render(<AuditFilterCard state={state} facets={facets()} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={facets()} busy={false} onChange={onChange} />)
    openEventPopover()

    // Control needle: an unrelated selection must survive the whole sequence below.
    fireEvent.click(screen.getByTestId('audit-event-row-invoice.created'))
    sync()

    const approvalIds = idsInDomain('approvals')
    expect(approvalIds.length, 'sanity: approvals has 4 ids').toBe(4)
    for (const id of approvalIds) {
      fireEvent.click(screen.getByTestId(`audit-event-row-${id}`))
      sync()
    }
    expect(
      [...state.events].sort(),
      'clicking every row in the group one at a time selects all of it',
    ).toEqual([...approvalIds, 'invoice.created'].sort())

    fireEvent.click(screen.getByTestId(`audit-event-row-${approvalIds[0]}`))
    sync()
    expect(state.events, 'deselecting one row removes only that id, the rest and the control needle survive').toEqual([
      'invoice.created',
      ...approvalIds.slice(1),
    ])
  })

  it('auditEventFilter_multiGroupSelectionCarriesAllAsRepeatedQueryParams', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const { rerender } = render(<AuditFilterCard state={state} facets={facets()} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={facets()} busy={false} onChange={onChange} />)
    openEventPopover()

    fireEvent.click(screen.getByTestId('audit-event-row-invoice.created'))
    sync()
    fireEvent.click(screen.getByTestId('audit-event-row-workflow_role.created'))
    sync()

    expect(
      [...state.events].sort(),
      'selections from two different groups both land in events',
    ).toEqual(['invoice.created', 'workflow_role.created'].sort())

    const query = auditFilterQuery(state)
    expect(
      query.event,
      'the query carries every selected id, from every group, as one array the URL layer repeats as params',
    ).toEqual(expect.arrayContaining(['invoice.created', 'workflow_role.created']))
    expect(query.event?.length, 'no extra or dropped ids').toBe(2)
  })

  // `busy` only gates FilterPopover's trigger (AuditFilterCard.tsx's three `disabled={busy}`
  // props) -- it never reaches into an already-open panel. Event selection applies
  // immediately (no draft/Apply step), so a fetch can start while the panel is still open.
  // This pins that actual behavior, matching the shipped search/date pattern -- not a gap
  // introduced here.
  it('auditEventFilter_rowsStayInteractiveIfBusyArrivesWhileThePanelIsAlreadyOpen', () => {
    const onChange = vi.fn()
    const { rerender } = render(
      <AuditFilterCard state={AUDIT_FILTER_DEFAULT} facets={facets()} busy={false} onChange={onChange} />,
    )
    openEventPopover()

    rerender(<AuditFilterCard state={AUDIT_FILTER_DEFAULT} facets={facets()} busy={true} onChange={onChange} />)

    const row = screen.getByTestId('audit-event-row-invoice.created') as HTMLButtonElement
    expect(row.disabled, 'row buttons are not gated by busy once the panel is already open').toBe(false)
    fireEvent.click(row)
    expect(onChange, 'the row remains genuinely clickable while busy').toHaveBeenCalledTimes(1)
  })

  it('auditEventFilter_triggerSummaryReflectsZeroOneAndManySelected', () => {
    renderCard({ ...AUDIT_FILTER_DEFAULT, events: [] })
    expect(
      screen.getByTestId('audit-event-trigger').textContent,
      'no summary text when nothing is selected',
    ).not.toMatch(/selected/)
    cleanup()

    renderCard({ ...AUDIT_FILTER_DEFAULT, events: ['invoice.created'] })
    expect(
      screen.getByTestId('audit-event-trigger').textContent,
      'a single selection still reads "1 selected"',
    ).toContain('1 selected')
    cleanup()

    renderCard({ ...AUDIT_FILTER_DEFAULT, events: ['invoice.created', 'document.read', 'membership.suspended'] })
    expect(screen.getByTestId('audit-event-trigger').textContent, 'many selections read "3 selected"').toContain(
      '3 selected',
    )
  })
})

// AUDIT-07-05 (task-657): actor control -- kind (Anyone/People only/System only) and named
// actors from facets.actor, mutually exclusive with kind (the server 400s on both).
function actorFacets(): AuditFacets {
  const f = facets()
  f.actor = [
    { value: 'user-a', name: 'Amara Chen', kind: 'person', count: 4 },
    { value: 'user-b', name: 'Femi Okoro', kind: 'person', count: 2 },
    { value: 'system', name: 'System', kind: 'system', count: 9 },
    { value: 'backfill-source-rows', name: 'backfill-source-rows', kind: 'raw', count: 1 },
  ]
  return f
}

function openActorPopover() {
  fireEvent.click(screen.getByTestId('audit-actor-trigger'))
}

// Row clicks may or may not close the panel (event rows don't, date presets do) -- this
// re-opens only if it's actually closed, so a multi-click sequence never mistakenly toggles
// an already-open panel shut.
function ensureActorPopoverOpen() {
  if (!screen.queryByTestId('audit-actor-panel')) openActorPopover()
}

describe('AuditFilterCard: actor', () => {
  it('auditActorFilter_neverEmitsBothActorAndActorKind', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const f = actorFacets()
    const { rerender } = render(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)

    function assertNeverBoth() {
      const query = auditFilterQuery(state)
      expect(
        query.actor !== undefined && query.actor_kind !== undefined,
        'the pair the server 400s on must never both be present',
      ).toBe(false)
    }

    // Kind then person -- selecting a person after a kind must clear the kind.
    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-kind-people'))
    sync()
    assertNeverBoth()
    expect(auditFilterQuery(state).actor_kind, 'kind selection lands on the wire').toBe('people')

    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-row-user-a'))
    sync()
    assertNeverBoth()
    expect(auditFilterQuery(state).actor, 'the person landed on the wire').toEqual(['user-a'])
    expect(auditFilterQuery(state).actor_kind, 'selecting a person must clear the active kind').toBeUndefined()

    // Person then kind -- selecting a kind after a person must clear the person.
    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-kind-system'))
    sync()
    assertNeverBoth()
    expect(auditFilterQuery(state).actor_kind, 'the new kind landed on the wire').toBe('system')
    expect(auditFilterQuery(state).actor, 'selecting a kind must clear the previously selected person').toBeUndefined()

    // Several people -- multi-select must not resurrect the kind.
    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-row-user-a'))
    sync()
    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-row-user-b'))
    sync()
    assertNeverBoth()
    expect([...state.actors].sort(), 'both people are selected').toEqual(['user-a', 'user-b'])
    expect(auditFilterQuery(state).actor_kind, 'no kind reappears from selecting people').toBeUndefined()

    // Then clear -- Anyone lands on neither.
    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-kind-anyone'))
    sync()
    assertNeverBoth()
    const finalQuery = auditFilterQuery(state)
    expect(finalQuery.actor, 'Anyone clears actor').toBeUndefined()
    expect(finalQuery.actor_kind, 'Anyone clears actor_kind').toBeUndefined()

    // Control needle: prove the sequence above genuinely drove the control, not a no-op run.
    expect(onChange.mock.calls.length, 'the sequence above must have emitted several changes').toBeGreaterThanOrEqual(5)
  })

  it('auditActorFilter_namedListIsNotTheMemberList', () => {
    const f = facets()
    f.actor = [{ value: 'ex-employee-77', name: 'Departed User', kind: 'person', count: 3 }]
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openActorPopover()

    // Population floor -- guards the "renders" check below against an empty popover.
    const rows = screen.getAllByTestId(/^audit-actor-row-/)
    expect(rows.length, 'population floor: the one facet actor produces one row').toBe(1)

    expect(
      screen.getByTestId('audit-actor-row-ex-employee-77'),
      'a departed member with no active membership still comes from facets.actor -- no memberships join (D-3)',
    ).toBeTruthy()
    expect(screen.getByTestId('audit-actor-label-ex-employee-77').textContent).toBe('Departed User')
  })

  it('auditActorFilter_allThreeShapesRenderWithoutMislabeling', () => {
    renderCard(AUDIT_FILTER_DEFAULT, actorFacets())
    openActorPopover()

    // Population floor -- three of the four seeded facet actors are the three shapes under
    // test (a fourth, user-b, exists only for the multi-select test below).
    const rows = screen.getAllByTestId(/^audit-actor-row-/)
    expect(rows.length, 'population floor: all four facet actors render').toBe(4)

    expect(screen.getByTestId('audit-actor-label-user-a').textContent, 'resolved person').toBe('Amara Chen')
    expect(screen.getByTestId('audit-actor-label-system').textContent, 'system').toBe('System')
    expect(
      screen.getByTestId('audit-actor-label-backfill-source-rows').textContent,
      'free-text raw actor',
    ).toBe('backfill-source-rows')

    // actorLabel's raw rung alone is mono (lib/actor.ts) -- this is the concrete "not
    // mislabelled as a person" check, not a taste call.
    const rawLabel = screen.getByTestId('audit-actor-label-backfill-source-rows')
    expect(rawLabel.style.fontFamily, 'raw kind renders in the mono face').toContain('font-mono')
    const personLabel = screen.getByTestId('audit-actor-label-user-a')
    expect(personLabel.style.fontFamily, 'a resolved person must not render in the raw mono face').not.toContain(
      'font-mono',
    )
  })

  it('auditActorFilter_threeKindRowsRenderAndAnyoneHasNoActorKind', () => {
    renderCard(AUDIT_FILTER_DEFAULT, actorFacets())
    openActorPopover()
    const kindRows = screen.getAllByTestId(/^audit-actor-kind-/)
    expect(kindRows.length, 'population floor: Anyone, People only, System only (AC#2)').toBe(3)
  })

  it('auditActorFilter_peopleOnlyEmitsActorKindPeople', () => {
    const { onChange } = renderCard(AUDIT_FILTER_DEFAULT, actorFacets())
    openActorPopover()
    fireEvent.click(screen.getByTestId('audit-actor-kind-people'))

    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    const query = auditFilterQuery(next)
    expect(query.actor_kind, 'People only emits actor_kind=people').toBe('people')
    expect(query.actor, 'People only emits no actor').toBeUndefined()
  })

  it('auditActorFilter_systemOnlyEmitsActorKindSystem', () => {
    const { onChange } = renderCard(AUDIT_FILTER_DEFAULT, actorFacets())
    openActorPopover()
    fireEvent.click(screen.getByTestId('audit-actor-kind-system'))

    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    const query = auditFilterQuery(next)
    expect(query.actor_kind, 'System only emits actor_kind=system').toBe('system')
    expect(query.actor, 'System only emits no actor').toBeUndefined()
  })

  it('auditActorFilter_anyoneEmitsNeitherActorNorActorKind', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actorKind: 'system' }
    const { onChange } = renderCard(state, actorFacets())
    openActorPopover()
    fireEvent.click(screen.getByTestId('audit-actor-kind-anyone'))

    expect(onChange).toHaveBeenCalledTimes(1)
    const next = onChange.mock.calls[0][0] as AuditFilterState
    const query = auditFilterQuery(next)
    expect(query.actor_kind, 'Anyone emits no actor_kind').toBeUndefined()
    expect(query.actor, 'Anyone emits no actor').toBeUndefined()
  })

  it('auditActorFilter_multiSelectOfNamedPeopleCarriesAllAsRepeatedQueryParams', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const f = actorFacets()
    const { rerender } = render(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)

    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-row-user-a'))
    sync()
    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-row-user-b'))
    sync()

    expect([...state.actors].sort(), 'both selections land in actors').toEqual(['user-a', 'user-b'])

    const query = auditFilterQuery(state)
    expect(
      query.actor,
      'the query carries both selected ids as one array the URL layer repeats as actor= params',
    ).toEqual(expect.arrayContaining(['user-a', 'user-b']))
    expect(query.actor?.length, 'no extra or dropped ids').toBe(2)
  })
  it('auditActorFilter_issuesNoMembershipRequest', () => {
    const fetchMock = vi.fn(async (_url: string) => ({ ok: true, status: 200, json: async () => ({}) }))
    vi.stubGlobal('fetch', fetchMock)
    try {
      // Control needle -- prove the mock is genuinely wired and would record a call.
      void fetch('https://gw.test/v1/audit-log?limit=25')
      const { onChange } = renderCard(AUDIT_FILTER_DEFAULT, actorFacets())
      openActorPopover()
      fireEvent.click(screen.getByTestId('audit-actor-row-user-a'))
      expect(onChange).toHaveBeenCalledTimes(1)

      const urls = fetchMock.mock.calls.map((c) => String(c[0]))
      expect(urls.some((u) => u.includes('/v1/audit-log')), 'control needle recorded').toBe(true)
      expect(urls.some((u) => u.includes('/v1/memberships')), 'the actor control must never fetch memberships').toBe(
        false,
      )
    } finally {
      vi.unstubAllGlobals()
    }

    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'AuditFilterCard.tsx'), 'utf8')
    expect(src.length).toBeGreaterThan(0)
    expect(src, 'the scan must be reading the real component').toContain('facets.actor')
    expect(src, 'the actor control must import nothing from lib/members').not.toMatch(/from ['"].*lib\/members['"]/)
  })

  it('auditActorFilter_emptyFacetRendersKindRowsButNoNamedSection', () => {
    renderCard(AUDIT_FILTER_DEFAULT, facets())
    openActorPopover()

    const kindRows = screen.getAllByTestId(/^audit-actor-kind-/)
    expect(kindRows.length, 'kind rows render regardless of the facet').toBe(3)
    expect(screen.queryAllByTestId(/^audit-actor-row-/).length, 'no named rows with an empty facet').toBe(0)
  })

  it('auditActorFilter_nilNameFallsBackToTheRawSubjectNotBlank', () => {
    const f = facets()
    f.actor = [{ value: 'raw-subject-id', name: null, kind: 'person', count: 1 }]
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openActorPopover()

    const rows = screen.getAllByTestId(/^audit-actor-row-/)
    expect(rows.length, 'population floor').toBe(1)

    const label = screen.getByTestId('audit-actor-label-raw-subject-id')
    expect(label.textContent, 'a nil name falls back to the raw subject, never blank').toBe('raw-subject-id')
    expect(label.style.fontFamily, 'the fallback renders mono, same as any other raw shape').toContain('font-mono')
  })

  it('auditActorFilter_personThenAnyoneThenKindEndsCleanAtEveryStep', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const f = actorFacets()
    const { rerender } = render(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)

    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-row-user-a'))
    sync()
    expect(state.actors, 'person selected').toEqual(['user-a'])
    expect(state.actorKind, 'no kind alongside the person').toBe('')

    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-kind-anyone'))
    sync()
    expect(state.actors, 'Anyone clears the person').toEqual([])
    expect(state.actorKind, 'Anyone clears the kind too').toBe('')

    ensureActorPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-actor-kind-system'))
    sync()
    expect(state.actorKind, 'kind lands cleanly after Anyone').toBe('system')
    expect(state.actors, 'no stale person resurfaces').toEqual([])
  })

  it('auditActorFilter_largeActorListRendersEveryRow', () => {
    const f = facets()
    f.actor = Array.from({ length: 250 }, (_, i) => ({
      value: `user-${i}`,
      name: `Person ${i}`,
      kind: 'person',
      count: i,
    }))
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openActorPopover()

    const rows = screen.getAllByTestId(/^audit-actor-row-/)
    expect(rows.length, 'every facet actor renders, none silently dropped').toBe(250)
    expect(screen.getByTestId('audit-actor-label-user-0').textContent).toBe('Person 0')
    expect(screen.getByTestId('audit-actor-label-user-249').textContent).toBe('Person 249')
  })

  it('auditActorFilter_busyDisablesTheActorTrigger', () => {
    render(<AuditFilterCard state={AUDIT_FILTER_DEFAULT} facets={actorFacets()} busy={true} onChange={vi.fn()} />)
    expect(screen.getByTestId('audit-actor-trigger')).toHaveProperty('disabled', true)
  })
})

