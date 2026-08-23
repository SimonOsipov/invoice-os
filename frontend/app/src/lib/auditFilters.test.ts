// RED specs (AUDIT-07-01, task-653) -- pin the filter state model, query builder and pill
// derivation before the executor fills auditFilters.ts's stub bodies in. Every spec below
// fails today because the stub throws `not implemented` before doing any real work, or (for
// the one spec that reaches a real assertion, auditFilters_clearAllKeepsTheThirtyDayWindow's
// clearAllFilters() call and every direct object-equality check downstream of a throwing
// call) fails at that throw -- both are the correct red reason (assertion / not-implemented),
// never an import/compile/setup error.

import { afterEach, describe, expect, it, vi } from 'vitest'

import type { AuditFacets } from './audit'
import {
  AUDIT_FILTER_DEFAULT,
  auditFilterPills,
  auditFilterQuery,
  auditRangeIsValid,
  clearAllFilters,
  selectActor,
  selectKind,
  type AuditFilterState,
  type AuditRange,
} from './auditFilters'

afterEach(() => {
  vi.useRealTimers()
})

const EMPTY_FACETS: AuditFacets = { event: [], actor: [], company: [] }

describe('auditFilterQuery', () => {
  it('auditFilters_defaultSendsOnlyThe30DayFrom', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-23T12:00:00.000Z'))

    const query = auditFilterQuery(AUDIT_FILTER_DEFAULT)

    const expectedFrom = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString()
    expect(query.from).toBe(expectedFrom)
    expect(Object.keys(query)).toEqual(['from'])
  })

  it('auditFilters_actorAndKindAreMutuallyExclusive', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actorKind: 'people' }

    const next = selectActor(state, 'u1')

    expect(next.actors).toEqual(['u1'])
    expect(next.actorKind).toBe('')

    const query = auditFilterQuery(next)
    // positive companion for the absence check below: the field the mutex is protecting
    // really is populated, so the missing key isn't just an empty-query fluke.
    expect(query.actor).toEqual(['u1'])
    expect('actor_kind' in query).toBe(false)
  })

  it('auditFilters_kindClearsNamedActors', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actors: ['u1', 'u2'] }

    const next = selectKind(state, 'system')

    expect(next.actors).toEqual([])
    expect(next.actorKind).toBe('system')

    const query = auditFilterQuery(next)
    expect(query.actor_kind).toBe('system')
    expect('actor' in query).toBe(false)
  })

  it('auditFilters_queryTieBreaksToActorsWhenBothAreSet', () => {
    // Bypasses selectActor/selectKind to build the state the type alone does not forbid --
    // AuditFilterState has no discriminated union over actorKind/actors, so both can be set
    // directly. auditFilterQuery must defensively resolve the pair itself: prefer actors,
    // the same direction selectActor already clears toward.
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actorKind: 'system', actors: ['u1'] }

    const query = auditFilterQuery(state)

    expect(query.actor).toEqual(['u1'])
    expect('actor_kind' in query).toBe(false)
  })

  it('auditFilters_companyNamedEmitsTheUuid', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      company: { mode: 'named', id: '11111111-1111-1111-1111-111111111111', name: 'Acme' },
    }

    const query = auditFilterQuery(state)

    expect(query.company).toBe('11111111-1111-1111-1111-111111111111')
  })

  it('auditFilters_companyWorkspaceEmitsTheLiteral', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, company: { mode: 'workspace' } }

    const query = auditFilterQuery(state)

    expect(query.company).toBe('workspace')
  })

  it('auditActorFilter_neverEmitsBothAcrossRandomInterleavings', () => {
    // Property-style sweep, deterministic seed. 20 random kind/name interleavings,
    // asserting after every step that the mutual exclusion holds -- broader than the
    // four hand-picked sequences above.
    let seed = 42
    const rand = () => {
      seed = (seed * 1103515245 + 12345) & 0x7fffffff
      return seed / 0x7fffffff
    }
    const ids = ['u1', 'u2', 'u3']
    const kinds: ('people' | 'system')[] = ['people', 'system']

    let state = AUDIT_FILTER_DEFAULT
    let sawBoth = false
    for (let i = 0; i < 20; i++) {
      state = rand() < 0.5 ? selectActor(state, ids[Math.floor(rand() * ids.length)]) : selectKind(state, kinds[Math.floor(rand() * kinds.length)])
      const query = auditFilterQuery(state)
      if (query.actor !== undefined && query.actor_kind !== undefined) sawBoth = true
    }

    expect(sawBoth, 'no interleaving of selectActor/selectKind may ever produce both keys').toBe(false)
  })

  it('auditFilters_customToIsEndOfDay', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      range: { preset: 'custom', from: '2026-08-01', to: '2026-08-20' },
    }

    const query = auditFilterQuery(state)

    expect(query.to).toBe('2026-08-20T23:59:59.999Z')
  })

  it('auditFilters_customFromIsStartOfDay', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      range: { preset: 'custom', from: '2026-08-01', to: '2026-08-20' },
    }

    const query = auditFilterQuery(state)

    expect(query.from).toBe('2026-08-01T00:00:00.000Z')
  })

  it('auditFilters_eventsAreEmittedAsRepeatedParams', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      events: ['invoice.created', 'submission.failed'],
    }

    const query = auditFilterQuery(state)

    expect(Array.isArray(query.event)).toBe(true)
    expect(query.event).toEqual(['invoice.created', 'submission.failed'])
  })

  it('auditFilters_allCompanyEmitsNoCompanyParam', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, company: { mode: 'all' } }

    const query = auditFilterQuery(state)

    expect('company' in query).toBe(false)
  })

  it('auditFilters_emptyStringQIsOmitted', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, q: '' }

    const query = auditFilterQuery(state)

    expect('q' in query).toBe(false)
  })

  it('auditFilters_whitespaceOnlyQIsSentVerbatim', () => {
    // No trimming rule exists (or is required by any AC) -- pins actual behaviour so a
    // future trim doesn't land silently.
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, q: '   ' }

    const query = auditFilterQuery(state)

    expect(query.q).toBe('   ')
  })

  it('auditFilters_duplicateEventIdsPassThroughUnchanged', () => {
    // The builder is a pure mapper -- dedup is a UI-layer concern (07-04), not this module's.
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, events: ['invoice.created', 'invoice.created'] }

    const query = auditFilterQuery(state)

    expect(query.event).toEqual(['invoice.created', 'invoice.created'])
  })

  it('auditFilters_unknownEventIdentifierPassesThroughUnvalidated', () => {
    // Vocabulary membership is enforced by the popover (AUDIT_EVENTS), not the query builder.
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, events: ['not.a.real.event'] }

    const query = auditFilterQuery(state)

    expect(query.event).toEqual(['not.a.real.event'])
  })
})

describe('auditRangeIsValid', () => {
  it('auditFilters_customFromAfterToIsInvalid', () => {
    const invalid: AuditRange = { preset: 'custom', from: '2026-08-20', to: '2026-08-10' }
    const valid: AuditRange = { preset: 'custom', from: '2026-08-10', to: '2026-08-20' }

    expect(auditRangeIsValid(invalid)).toBe(false)
    // positive companion: proves the function isn't just always returning false.
    expect(auditRangeIsValid(valid)).toBe(true)
  })

  it('auditFilters_customFromEqualsToIsValid', () => {
    // A single-day range -- the string comparison is <=, not <.
    const sameDay: AuditRange = { preset: 'custom', from: '2026-08-15', to: '2026-08-15' }

    expect(auditRangeIsValid(sameDay)).toBe(true)
  })
})

describe('auditFilterPills', () => {
  it('auditFilters_thirtyDayDefaultCarriesAPill', () => {
    const pills = auditFilterPills(AUDIT_FILTER_DEFAULT, EMPTY_FACETS)

    expect(pills.length).toBe(1)
    const rangePill = pills.find((p) => p.key === 'range')
    expect(rangePill).toBeDefined()
    expect(rangePill?.label).toBe('Last 30 days')
  })

  it('auditFilters_everyNonDefaultFieldGetsExactlyOnePill', () => {
    const facets: AuditFacets = {
      event: [
        { value: 'invoice.created', name: 'Invoice created', count: 3 },
        { value: 'submission.failed', name: 'Submission failed', count: 1 },
      ],
      actor: [{ value: 'u1', name: 'Musa Danjuma', kind: 'people', count: 2 }],
      company: [{ value: '11111111-1111-1111-1111-111111111111', name: 'Acme', count: 5 }],
    }
    const state: AuditFilterState = {
      q: 'invoice 42',
      range: { preset: '30d' },
      events: ['invoice.created', 'submission.failed'],
      actorKind: 'people',
      actors: [],
      company: { mode: 'named', id: '11111111-1111-1111-1111-111111111111', name: 'Acme' },
      invoiceId: 'inv-1',
      invoiceNumber: 'INV-0042',
    }
    // 1 range (always present) + 1 q + 2 events + 1 kind + 1 company + 1 invoice = 7.

    const pills = auditFilterPills(state, facets)

    expect(pills.length).toBe(7)
    const keys = pills.map((p) => p.key)
    expect(new Set(keys).size).toBe(7)

    const pillsAgain = auditFilterPills(state, facets)
    expect(pillsAgain.map((p) => p.key)).toEqual(keys)
  })

  it('auditFilters_removingTheDatePillDropsFromAndTo', () => {
    const state: AuditFilterState = {
      ...AUDIT_FILTER_DEFAULT,
      q: 'invoice',
      events: ['invoice.created'],
    }

    const pills = auditFilterPills(state, EMPTY_FACETS)
    expect(pills.length).toBeGreaterThan(0)
    const rangePill = pills.find((p) => p.key === 'range')
    expect(rangePill).toBeDefined()

    const next = rangePill!.onRemove(state)

    // positive first: the pill removal must be scoped to the date range alone.
    expect(next.q).toBe('invoice')
    expect(next.events).toEqual(['invoice.created'])

    const query = auditFilterQuery(next)
    expect('from' in query).toBe(false)
    expect('to' in query).toBe(false)
  })
})

describe('selectActor toggling', () => {
  it('auditFilters_selectActorTogglesOffASecondClick', () => {
    // Multi-select checkbox semantics (matches the 05 popover design): a second click on an
    // already-selected actor removes it, it does not duplicate or replace the array.
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actors: ['u1'] }

    const next = selectActor(state, 'u1')

    expect(next.actors).toEqual([])
  })

  it('auditFilters_selectActorAddsADistinctSecondActor', () => {
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actors: ['u1'] }

    const next = selectActor(state, 'u2')

    expect(next.actors).toEqual(['u1', 'u2'])
  })
})

describe('auditFilterPills adversarial coverage', () => {
  it('auditFilters_clearAllPillsAreJustTheThirtyDayPill', () => {
    const cleared = clearAllFilters()

    const pills = auditFilterPills(cleared, EMPTY_FACETS)

    expect(pills.length).toBe(1)
    expect(pills[0].key).toBe('range')
    expect(pills[0].label).toBe('Last 30 days')
  })

  it('auditFilters_pillKeysAreUniqueAndOrderStableUnderAFullState', () => {
    const facets: AuditFacets = {
      event: [
        { value: 'invoice.created', name: 'Invoice created', count: 3 },
        { value: 'approval.granted', name: 'Approval granted', count: 1 },
      ],
      actor: [{ value: 'u1', name: 'Musa Danjuma', kind: 'people', count: 2 }],
      company: [{ value: '11111111-1111-1111-1111-111111111111', name: 'Acme', count: 5 }],
    }
    const state: AuditFilterState = {
      q: 'invoice 42',
      range: { preset: 'custom', from: '2026-08-01', to: '2026-08-20' },
      events: ['invoice.created', 'approval.granted'],
      actorKind: '',
      actors: ['u1'],
      company: { mode: 'named', id: '11111111-1111-1111-1111-111111111111', name: 'Acme' },
      invoiceId: 'inv-1',
      invoiceNumber: 'INV-0042',
    }
    // range + q + 2 events + 1 actor + company + invoice = 7, all keys distinct.

    const pills = auditFilterPills(state, facets)
    const keys = pills.map((p) => p.key)

    expect(pills.length).toBeGreaterThan(0)
    expect(pills.length).toBe(7)
    expect(new Set(keys).size).toBe(keys.length)

    const secondCall = auditFilterPills(state, facets).map((p) => p.key)
    expect(secondCall).toEqual(keys)
  })

  it('auditFilters_inconsistentActorStateRendersBothPillsUntilTheStateIsCorrected', () => {
    // Only reachable by constructing state directly (selectActor/selectKind never allow
    // this) -- see the same gap auditFilters_queryTieBreaksToActorsWhenBothAreSet covers at
    // the query layer. Pinned so a future pill-layer guard is a deliberate change, not a
    // silent one: today BOTH an actorKind pill and an actor pill render for this state, even
    // though auditFilterQuery would only ever emit `actor`.
    const state: AuditFilterState = { ...AUDIT_FILTER_DEFAULT, actorKind: 'system', actors: ['u1'] }
    const facets: AuditFacets = { ...EMPTY_FACETS, actor: [{ value: 'u1', name: 'Musa Danjuma', kind: 'people', count: 1 }] }

    const pills = auditFilterPills(state, facets)

    const kindPill = pills.find((p) => p.key === 'actorKind')
    const actorPill = pills.find((p) => p.key === 'actor:u1')
    expect(kindPill).toBeDefined()
    expect(actorPill).toBeDefined()
  })
})

describe('clearAllFilters', () => {
  it('auditFilters_clearAllKeepsTheThirtyDayWindow', () => {
    const cleared = clearAllFilters()

    expect(cleared).toEqual(AUDIT_FILTER_DEFAULT)

    const query = auditFilterQuery(cleared)
    expect(query.from).toBeDefined()
    expect('to' in query).toBe(false)
  })
})
