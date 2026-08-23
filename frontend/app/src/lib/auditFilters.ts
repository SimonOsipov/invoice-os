// Filter state model, query builder and pill derivation for the Audit screen (AUDIT-07-01,
// task-653). Pure -- no React, no fetch, no DOM. lib/audit.ts owns the wire types and stays
// unmodified; this module only builds an AuditLogQuery, it never serializes one.
//
// RED stub (Stage 2.5): every function below throws `not implemented`. auditFilters.test.ts
// pins the target behaviour before the executor fills these bodies in.

import type { AuditFacets, AuditLogQuery } from './audit'

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
  onRemove: (state: AuditFilterState) => AuditFilterState
}

function notImplemented(fn: string): never {
  throw new Error(`auditFilters.${fn}: not implemented`)
}

export function auditFilterQuery(_state: AuditFilterState, _now: Date = new Date()): AuditLogQuery {
  return notImplemented('auditFilterQuery')
}

export function auditRangeIsValid(_range: AuditRange): boolean {
  return notImplemented('auditRangeIsValid')
}

// Clears actorKind -- actor and actor_kind are server-mutually-exclusive (400 on both).
export function selectActor(_state: AuditFilterState, _actorId: string): AuditFilterState {
  return notImplemented('selectActor')
}

// Clears actors -- same exclusivity, opposite direction.
export function selectKind(_state: AuditFilterState, _kind: 'people' | 'system'): AuditFilterState {
  return notImplemented('selectKind')
}

export function auditFilterPills(_state: AuditFilterState, _facets: AuditFacets): AuditFilterPill[] {
  return notImplemented('auditFilterPills')
}

export function clearAllFilters(): AuditFilterState {
  return notImplemented('clearAllFilters')
}
