// Filter state model, query builder and pill derivation for the Audit screen (AUDIT-07-01,
// task-653). Pure -- no React, no fetch, no DOM. lib/audit.ts owns the wire types and stays
// unmodified; this module only builds an AuditLogQuery, it never serializes one.

import type { AuditFacets, AuditLogQuery } from './audit'
import { invoiceFilterPillLabel } from './auditView'
import { auditEventView } from './auditVocabulary'

export type AuditRangePreset = '24h' | '7d' | '30d' | 'custom'

export interface AuditRange {
  preset: AuditRangePreset
  // 'YYYY-MM-DD' from <input type="date">, set only when preset === 'custom'.
  from?: string
  to?: string
}

export type AuditCompanySel = { mode: 'all' } | { mode: 'workspace' } | { mode: 'named'; id: string; name: string }

export interface AuditFilterState {
  q: string
  range: AuditRange
  events: string[]
  actorKind: '' | 'people' | 'system'
  actors: string[]
  company: AuditCompanySel
  invoiceId: string | null
  // Kept alongside invoiceId for invoiceFilterPillLabel (lib/auditView.ts), which takes the
  // number, not the id.
  invoiceNumber: string | null
}

export const AUDIT_FILTER_DEFAULT: AuditFilterState = {
  q: '',
  range: { preset: '30d' },
  events: [],
  actorKind: '',
  actors: [],
  company: { mode: 'all' },
  invoiceId: null,
  invoiceNumber: null,
}

export interface AuditFilterPill {
  key: string
  label: string
  // Q6 ladder (display_name -> email -> raw subject in mono): set when the label falls
  // back to a raw, unresolved identifier, so the pill renders in the mono face.
  mono?: boolean
  onRemove: (state: AuditFilterState) => AuditFilterState
}

const DAY_MS = 24 * 60 * 60 * 1000

// Removing the date pill leaves 'custom' with no from/to -- the type has no fifth "none"
// preset, so an empty custom range is how "no date filter" is represented.
const REMOVE_RANGE: AuditRange = { preset: 'custom' }

function rangeToQuery(range: AuditRange, now: Date): Pick<AuditLogQuery, 'from' | 'to'> {
  switch (range.preset) {
    case '24h':
      return { from: new Date(now.getTime() - DAY_MS).toISOString() }
    case '7d':
      return { from: new Date(now.getTime() - 7 * DAY_MS).toISOString() }
    case '30d':
      return { from: new Date(now.getTime() - 30 * DAY_MS).toISOString() }
    case 'custom': {
      const out: Pick<AuditLogQuery, 'from' | 'to'> = {}
      if (range.from) out.from = `${range.from}T00:00:00.000Z`
      // Inclusive <= on created_at (filter.go) -- a bare date would drop the whole last day.
      if (range.to) out.to = `${range.to}T23:59:59.999Z`
      return out
    }
  }
}

// actor/actor_kind are server-mutually-exclusive (handlers.go:167-169, a 400 on both). The
// type doesn't forbid both being set at once, so the builder tie-breaks defensively: actors
// wins, the same direction selectActor already clears toward.
function actorParams(state: AuditFilterState): Pick<AuditLogQuery, 'actor' | 'actor_kind'> {
  if (state.actors.length > 0) return { actor: state.actors }
  if (state.actorKind !== '') return { actor_kind: state.actorKind }
  return {}
}

function companyParam(company: AuditCompanySel): Pick<AuditLogQuery, 'company'> {
  if (company.mode === 'named') return { company: company.id }
  if (company.mode === 'workspace') return { company: 'workspace' }
  return {}
}

export function auditFilterQuery(state: AuditFilterState, now: Date = new Date()): AuditLogQuery {
  return {
    ...rangeToQuery(state.range, now),
    ...(state.q !== '' ? { q: state.q } : {}),
    ...(state.events.length > 0 ? { event: state.events } : {}),
    ...actorParams(state),
    ...companyParam(state.company),
    ...(state.invoiceId != null ? { invoice_id: state.invoiceId } : {}),
  }
}

export function auditRangeIsValid(range: AuditRange): boolean {
  if (range.preset !== 'custom') return true
  if (!range.from || !range.to) return true
  return range.from <= range.to
}

// Clears actorKind -- actor and actor_kind are server-mutually-exclusive (400 on both).
export function selectActor(state: AuditFilterState, actorId: string): AuditFilterState {
  const actors = state.actors.includes(actorId)
    ? state.actors.filter((a) => a !== actorId)
    : [...state.actors, actorId]
  return { ...state, actorKind: '', actors }
}

// Clears actors -- same exclusivity, opposite direction.
export function selectKind(state: AuditFilterState, kind: 'people' | 'system'): AuditFilterState {
  return { ...state, actorKind: kind, actors: [] }
}

function rangePillFor(range: AuditRange): AuditFilterPill | null {
  const onRemove = (state: AuditFilterState): AuditFilterState => ({ ...state, range: REMOVE_RANGE })
  switch (range.preset) {
    case '30d':
      // Always present -- the 30-day window is a pre-applied filter (CA-12), not an unset one.
      return { key: 'range', label: 'Last 30 days', onRemove }
    case '24h':
      return { key: 'range', label: 'Last 24 hours', onRemove }
    case '7d':
      return { key: 'range', label: 'Last 7 days', onRemove }
    case 'custom':
      if (!range.from || !range.to) return null
      return { key: 'range', label: `${range.from} – ${range.to}`, onRemove }
  }
}

export function auditFilterPills(state: AuditFilterState, facets: AuditFacets): AuditFilterPill[] {
  const pills: AuditFilterPill[] = []

  const rangePill = rangePillFor(state.range)
  if (rangePill) pills.push(rangePill)

  if (state.q !== '') {
    pills.push({ key: 'q', label: `Search: "${state.q}"`, onRemove: (s) => ({ ...s, q: '' }) })
  }

  for (const id of state.events) {
    pills.push({
      key: `event:${id}`,
      // The server never resolves Name for the event facet (facets.go/reader.go), so this
      // reads the same vocabulary the event-type control trusts, never facets.event[i].name.
      label: auditEventView(id).label,
      onRemove: (s) => ({ ...s, events: s.events.filter((e) => e !== id) }),
    })
  }

  if (state.actorKind !== '') {
    pills.push({
      key: 'actorKind',
      label: state.actorKind === 'people' ? 'People only' : 'System only',
      onRemove: (s) => ({ ...s, actorKind: '' }),
    })
  }

  for (const id of state.actors) {
    const facetActor = facets.actor.find((f) => f.value === id)
    pills.push({
      key: `actor:${id}`,
      label: facetActor?.name ?? id,
      // Q6: no resolvable name is the designed raw-subject fallback, not a bug -- render it
      // mono, matching lib/actor.ts's presentation of an unresolved subject.
      mono: facetActor?.name == null,
      onRemove: (s) => ({ ...s, actors: s.actors.filter((a) => a !== id) }),
    })
  }

  if (state.company.mode !== 'all') {
    pills.push({
      key: 'company',
      label: state.company.mode === 'workspace' ? 'Workspace-level only' : state.company.name,
      onRemove: (s) => ({ ...s, company: { mode: 'all' } }),
    })
  }

  if (state.invoiceId != null) {
    pills.push({
      key: 'invoice',
      label: invoiceFilterPillLabel(state.invoiceNumber),
      onRemove: (s) => ({ ...s, invoiceId: null, invoiceNumber: null }),
    })
  }

  return pills
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false
  const aKeys = Object.keys(a as Record<string, unknown>)
  const bKeys = Object.keys(b as Record<string, unknown>)
  if (aKeys.length !== bKeys.length) return false
  return aKeys.every((k) => deepEqual((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]))
}

// Gates Clear all's absence (AC#5) on the real state, not on pills.length <= 1, which
// breaks the moment an always-present pill other than range is added.
export function auditFilterIsDefault(state: AuditFilterState): boolean {
  return deepEqual(state, AUDIT_FILTER_DEFAULT)
}

export function clearAllFilters(): AuditFilterState {
  return AUDIT_FILTER_DEFAULT
}
