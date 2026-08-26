// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Search, date-range, event-type, actor and company controls.

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

  // AUDIT-11-08: the new copy contains "invoice numbers", so a bare /invoice number/ match
  // stays green on the opposite claim. Assert the DENIAL classes instead of a substring.
  it('auditSearch_helperDeniesEmailButNotInvoiceNumberSearch', () => {
    renderCard()
    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    const text = screen.getByTestId('audit-search-helper').textContent ?? ''
    expect(text, 'must not deny invoice-number search').not.toMatch(/cannot find an invoice number/i)
    expect(text, 'must still deny actor-by-email search').toMatch(
      /cannot find.{0,60}email|email address.{0,20}(cannot|can't|not)/i,
    )
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


// AUDIT-07-06 (task-658): company control -- All / Workspace-level only / one row per named
// bucket from facets.company. Wire encoding (companyParam) already shipped and green in
// lib/auditFilters.test.ts; this block covers only the popover UI over it.
function companyFacets(): AuditFacets {
  const f = facets()
  f.company = [
    { value: null, name: null, count: 4 }, // the workspace/unattributed bucket
    { value: 'co-acme', name: 'Acme Ltd', count: 40 },
    { value: 'co-deleted', name: null, count: 3 }, // non-null id, deleted company
  ]
  return f
}

function openCompanyPopover() {
  fireEvent.click(screen.getByTestId('audit-company-trigger'))
}

function ensureCompanyPopoverOpen() {
  if (!screen.queryByTestId('audit-company-panel')) openCompanyPopover()
}

describe('AuditFilterCard: company', () => {
  it('auditCompanyFilter_threeChoicesRenderAndEachEmitsTheRightWireValue', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const f = companyFacets()
    const { rerender } = render(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)

    ensureCompanyPopoverOpen()
    // Population floor -- guards every lookup below against an empty/collapsed panel.
    const kindRows = screen.getAllByTestId(/^audit-company-kind-/)
    expect(kindRows.length, 'population floor: All + Workspace-level only').toBe(2)
    const namedRows = screen.getAllByTestId(/^audit-company-row-/)
    expect(namedRows.length, 'population floor: one row per named facet bucket (AC#2)').toBe(2)

    // Workspace-level only -> the literal 'workspace', never a uuid, never absent.
    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-kind-workspace'))
    sync()
    expect(auditFilterQuery(state).company, "workspace row emits the 'workspace' literal").toBe('workspace')

    // Named company -> its uuid verbatim, and the workspace literal must be gone.
    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-row-co-acme'))
    sync()
    let query = auditFilterQuery(state)
    expect(query.company, 'named row emits the uuid verbatim').toBe('co-acme')
    expect(query.company, "must not still carry the 'workspace' literal").not.toBe('workspace')

    // All -> no company param at all, not an empty string and not 'all'.
    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-kind-all'))
    sync()
    query = auditFilterQuery(state)
    expect('company' in query, 'All emits no company param').toBe(false)

    // Control needle -- the sequence above genuinely drove three distinct changes, not a no-op run.
    expect(onChange.mock.calls.length, 'three selections must have fired three changes').toBeGreaterThanOrEqual(3)
  })

  it('auditCompanyFilter_workspaceCaveatIsVisibleTextNotTitleOnly', () => {
    renderCard(AUDIT_FILTER_DEFAULT, companyFacets())
    openCompanyPopover()

    const caveat = screen.getByTestId('audit-company-workspace-caveat')
    expect(
      caveat.textContent?.trim(),
      'the D-7 honesty line (contract §3) must render as real text content, not rely on title=',
    ).toBe('Also includes events with no company to attribute them to.')
    expect(
      caveat.getAttribute('title'),
      'the caveat element itself must not carry the line ONLY via a title= attribute',
    ).not.toBe('Also includes events with no company to attribute them to.')
    expect(
      screen.getByTestId('audit-company-kind-workspace').getAttribute('title'),
      'the honesty line must not be hidden behind a title= on the row button either',
    ).not.toBe('Also includes events with no company to attribute them to.')
  })

  it('auditCompanyFilter_namedCountsAndTheWorkspaceCountComeFromTheFacetNotAnyOtherDerivation', () => {
    const f = facets()
    f.company = [
      { value: null, name: null, count: 777 }, // workspace/unattributed bucket
      { value: 'co-acme', name: 'Acme Ltd', count: 999 },
      { value: 'co-deleted', name: null, count: 5 },
    ]
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openCompanyPopover()

    const workspaceCount = screen.getByTestId('audit-company-count-workspace')
    expect(
      workspaceCount.textContent,
      'control needle: Workspace-level only shows the value===null bucket count (AC#3)',
    ).toBe('777')
    expect(workspaceCount.textContent, 'must not be the sum of every bucket').not.toBe('1781')
    expect(workspaceCount.textContent, 'must not be the number of named buckets').not.toBe('2')

    const acmeCount = screen.getByTestId('audit-company-count-co-acme')
    expect(acmeCount.textContent, 'control needle: the real facet count must render').toBe('999')
    expect(acmeCount.textContent, "must not be the OTHER named bucket's count").not.toBe('5')
    expect(acmeCount.textContent, 'must not be the workspace bucket count').not.toBe('777')

    const deletedCount = screen.getByTestId('audit-company-count-co-deleted')
    expect(deletedCount.textContent, "a second bucket must show its own count, not the first row's").toBe('5')
  })

  it('auditCompanyFilter_namedRowsNeverFetchThePortfolioEntityList', () => {
    const fetchMock = vi.fn(async (_url: string) => ({ ok: true, status: 200, json: async () => ({}) }))
    vi.stubGlobal('fetch', fetchMock)
    try {
      // Control needle -- prove the mock is genuinely wired and would record a call.
      void fetch('https://gw.test/v1/audit-log?limit=25')
      renderCard(AUDIT_FILTER_DEFAULT, companyFacets())
      openCompanyPopover()

      const urls = fetchMock.mock.calls.map((c) => String(c[0]))
      expect(urls.some((u) => u.includes('/v1/audit-log')), 'control needle recorded').toBe(true)
      expect(
        urls.some((u) => u.includes('/portfolio/v1/entities')),
        'named company rows must come from facets.company, never a fetched portfolio entity list',
      ).toBe(false)
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('auditCompanyFilter_labelUsesFacetsResolvedNameFallsBackToDeletedCopyWhenNameIsNull', () => {
    const f = facets()
    f.company = [
      { value: null, name: null, count: 1 },
      { value: 'co-acme', name: 'Acme Ltd', count: 10 },
      { value: 'co-deleted', name: null, count: 2 },
    ]
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openCompanyPopover()

    // Population floor -- guards the two label checks below against an empty/collapsed panel.
    const namedRows = screen.getAllByTestId(/^audit-company-row-/)
    expect(namedRows.length, 'population floor: two named buckets').toBe(2)

    expect(screen.getByTestId('audit-company-label-co-acme').textContent, "uses the facet's resolved Name").toBe(
      'Acme Ltd',
    )

    const deletedLabel = screen.getByTestId('audit-company-label-co-deleted')
    expect(
      deletedLabel.textContent,
      'a null Name (deleted company, non-null id) renders the §5 copy, never blank and never Workspace (AC#6)',
    ).toBe('A company that no longer exists')
    expect(deletedLabel.textContent, 'must never render blank').not.toBe('')
    expect(deletedLabel.textContent, 'must never be mislabelled as the Workspace row').not.toBe('Workspace-level only')
  })

  it('auditCompanyFilter_namedThenAllClearsTheParamEntirely', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const f = companyFacets()
    const { rerender } = render(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)

    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-row-co-acme'))
    sync()
    expect(auditFilterQuery(state).company, 'named selection lands on the wire first').toBe('co-acme')
    expect(state.company, 'state captures the name at selection time (AC#7)').toEqual({
      mode: 'named',
      id: 'co-acme',
      name: 'Acme Ltd',
    })

    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-kind-all'))
    sync()
    const query = auditFilterQuery(state)
    expect('company' in query, 'switching back to All clears the param entirely').toBe(false)
    expect(query.company, 'no stale company value survives the switch').toBeUndefined()
    expect(state.company, 'state resets to the all mode').toEqual({ mode: 'all' })
  })
})

// AUDIT-07-06 QA (task-658): adversarial coverage over the company control.
describe('AuditFilterCard: company adversarial (AUDIT-07-06 QA)', () => {
  it('auditCompanyFilter_emptyFacetRendersAllAndWorkspaceOnlyNoNamedRowsNoCrash', () => {
    const f = facets() // company: []
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openCompanyPopover()

    const kindRows = screen.getAllByTestId(/^audit-company-kind-/)
    expect(kindRows.length, 'All + Workspace-level only still render with no facet data').toBe(2)
    expect(screen.queryAllByTestId(/^audit-company-row-/).length, 'no named rows when facets.company is empty').toBe(0)
    expect(
      screen.getByTestId('audit-company-count-workspace').textContent,
      'workspace count falls back to 0, not a crash, when there is no null bucket at all',
    ).toBe('0')
  })

  it('auditCompanyFilter_nullValueWorkspaceBucketNeverCollapsesWithANamedDeletedCompany', () => {
    const f = facets()
    f.company = [
      { value: null, name: null, count: 4 }, // workspace/unattributed bucket
      { value: 'co-deleted', name: null, count: 3 }, // named bucket, id present, name null
    ]
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openCompanyPopover()

    // Population floor -- exactly one named row; the null-value bucket must not become a second one.
    const namedRows = screen.getAllByTestId(/^audit-company-row-/)
    expect(namedRows.length, 'the null-value bucket is not a named row').toBe(1)

    const workspaceCount = screen.getByTestId('audit-company-count-workspace').textContent
    const deletedCount = screen.getByTestId('audit-company-count-co-deleted').textContent
    expect(workspaceCount, "workspace shows the null-VALUE bucket's own count").toBe('4')
    expect(deletedCount, "the deleted company shows its own count, distinct from workspace's").toBe('3')
    // Control needle -- the two counts must actually differ, or this test cannot tell the buckets apart.
    expect(workspaceCount, 'a null value and a null name are different things').not.toBe(deletedCount)

    expect(
      screen.getByTestId('audit-company-label-co-deleted').textContent,
      'a null NAME with a non-null id renders the deleted-company copy, not the workspace label',
    ).toBe('A company that no longer exists')
  })

  it('auditCompanyFilter_sequentialSwitchWorkspaceNamedAllLeavesNoResidueAtAnyStep', () => {
    let state = AUDIT_FILTER_DEFAULT
    const onChange = vi.fn((next: AuditFilterState) => {
      state = next
    })
    const f = companyFacets()
    const { rerender } = render(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)
    const sync = () => rerender(<AuditFilterCard state={state} facets={f} busy={false} onChange={onChange} />)

    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-kind-workspace'))
    sync()
    expect(state.company, 'workspace step carries no id/name residue').toEqual({ mode: 'workspace' })
    expect(auditFilterQuery(state).company).toBe('workspace')

    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-row-co-acme'))
    sync()
    expect(state.company, 'named step fully replaces the workspace mode').toEqual({
      mode: 'named',
      id: 'co-acme',
      name: 'Acme Ltd',
    })
    const namedQuery = auditFilterQuery(state)
    expect(namedQuery.company).toBe('co-acme')
    expect(namedQuery.company, 'no leftover workspace literal').not.toBe('workspace')

    ensureCompanyPopoverOpen()
    fireEvent.click(screen.getByTestId('audit-company-kind-all'))
    sync()
    expect(state.company, 'all step drops id/name entirely, not just the mode tag').toEqual({ mode: 'all' })
    expect('company' in auditFilterQuery(state), 'no company param at all after returning to All').toBe(false)
  })

  it('auditCompanyFilter_veryLongCompanyNameRendersInFullNoTruncation', () => {
    const longName = 'A'.repeat(180) + ' Very Long Trading Name (Nigeria) Unlimited by Shares'
    const f = facets()
    f.company = [{ value: 'co-long', name: longName, count: 9 }]
    renderCard(AUDIT_FILTER_DEFAULT, f)
    openCompanyPopover()

    const label = screen.getByTestId('audit-company-label-co-long')
    expect(label.textContent, 'the full name renders -- this is a popover row, not the collapsing table').toBe(longName)
    expect(label.textContent?.includes('…'), 'no ellipsis truncation is applied').toBe(false)
  })

  it('auditCompanyFilter_busyDisablesTheCompanyTrigger', () => {
    render(<AuditFilterCard state={AUDIT_FILTER_DEFAULT} facets={companyFacets()} busy={true} onChange={vi.fn()} />)
    expect(screen.getByTestId('audit-company-trigger')).toHaveProperty('disabled', true)
  })
})

// AUDIT-07-07 QA (task-659): adversarial coverage for the pills row and Clear all, beyond
// the RED specs pinned in AuditView.test.tsx / auditFilters.test.ts.
describe('AuditFilterCard: pills row adversarial coverage (AUDIT-07-07)', () => {
  it('auditPills_tenEventPillsDoNotWidenTheCard', () => {
    renderCard(AUDIT_FILTER_DEFAULT)
    const widthAtDefault = screen.getByTestId('audit-filter-card').style.width
    cleanup()

    const ids = Object.keys(AUDIT_EVENTS).slice(0, 10)
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, events: ids }
    renderCard(state)
    const pills = screen.getAllByTestId(/^audit-pill-event:/)
    expect(pills.length, 'population floor: ten selected events must render ten event pills').toBe(10)

    const card = screen.getByTestId('audit-filter-card')
    // The card carries no content-driven width -- growth is absorbed by the pills row
    // wrapping, never by the card widening (task-659 layout note; px sweep is AUDIT-07-11's).
    expect(card.style.width, 'card width is not a function of pill count').toBe(widthAtDefault)
    const pillsRow = card.children[1] as HTMLElement
    expect(pillsRow.style.flexWrap, 'pills wrap onto new lines instead of growing the row').toBe('wrap')
  })

  it('auditPills_unknownEventIdIsHumanisedNeverTheBareId', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, events: ['custom.mystery_type'] }
    renderCard(state)

    const pill = screen.getByTestId('audit-pill-event:custom.mystery_type')
    expect(pill.textContent, 'never the bare id').not.toContain('custom.mystery_type')
    expect(pill.textContent, 'humanised tail, matching auditVocabulary.ts humanise()').toContain('Mystery type')
  })

  it('auditPills_clearAllDisappearsExactlyWhenTheLastNonDefaultPillIsGone', () => {
    const onChange = vi.fn()
    const stateWithQ: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, q: 'kept' }
    const { rerender } = render(<AuditFilterCard state={stateWithQ} facets={facets()} busy={false} onChange={onChange} />)
    expect(screen.getByTestId('audit-clear-all'), 'Clear all present with one non-default filter applied').toBeTruthy()

    rerender(<AuditFilterCard state={AUDIT_FILTER_DEFAULT} facets={facets()} busy={false} onChange={onChange} />)
    expect(screen.queryByTestId('audit-clear-all'), 'Clear all vanishes the instant state returns to default').toBeNull()
  })

  it('auditPills_pillAndClearAllStayClickableWhileBusy', () => {
    // Matches the shipped precedent (auditEventFilter_rowsStayInteractiveIfBusyArrivesWhileThePanelIsAlreadyOpen):
    // `busy` only gates the five FilterPopover triggers, never an already-rendered row or pill.
    const onChange = vi.fn()
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, q: 'kept' }
    render(<AuditFilterCard state={state} facets={facets()} busy={true} onChange={onChange} />)

    const pill = screen.getByTestId('audit-pill-q') as HTMLButtonElement
    const clearAll = screen.getByTestId('audit-clear-all') as HTMLButtonElement
    expect(pill.disabled, 'pill removal is not gated by busy').toBe(false)
    expect(clearAll.disabled, 'Clear all is not gated by busy').toBe(false)

    fireEvent.click(pill)
    fireEvent.click(clearAll)
    expect(onChange, 'both remain genuinely clickable while busy').toHaveBeenCalledTimes(2)
  })

  it('auditPills_actorPillMonoFallbackHasAControlNeedle', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actors: ['u-raw', 'u-resolved'] }
    const f: AuditFacets = { ...facets(), actor: [{ value: 'u-resolved', name: 'Musa Danjuma', kind: 'people', count: 2 }] }
    renderCard(state, f)

    const rawPill = screen.getByTestId('audit-pill-actor:u-raw')
    const resolvedPill = screen.getByTestId('audit-pill-actor:u-resolved')
    expect(rawPill.style.fontFamily, 'unresolved actor pill falls back to mono').toContain('font-mono')
    expect(resolvedPill.style.fontFamily, 'control needle: a resolved actor pill must not be mono').not.toContain(
      'font-mono',
    )
  })
})
