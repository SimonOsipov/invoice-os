// Shared types for the Support Console. Mirrors the shape of the prototype's
// `this.state` (Support Console.dc.html:706-727) with the loose JS widened into
// discriminated unions where the console actually branches on them.

import type { ReactNode } from 'react'

export type Screen = 'submissions' | 'rules' | 'audit' | 'tenants' | 'health'

export type Env = 'sandbox' | 'live'

// The submission-pipeline states the console can display. Ordered as the filter
// chips are (proto:866): the chip row is the canonical enumeration, and `JobFilter`
// is this plus the 'all' pseudo-state.
export type JobState = 'queued' | 'submitting' | 'pending' | 'accepted' | 'rejected' | 'failed' | 'dead-letter'

export type JobFilter = 'all' | JobState

export type SubTab = 'jobs' | 'recon'

export type Severity = 'error' | 'warn' | 'info'

// 'global' applies to every tenant; 'tenant-override' means at least one tenant has
// its own variant. The console renders these as GLOBAL / TENANT (proto:910).
export type RuleScope = 'global' | 'tenant-override'

export interface Job {
  id: string
  tenant: string
  tin: string
  invoice: string
  state: JobState
  attempts: number
  lastError: string
  age: string
  app: string
}

export interface Rule {
  key: string
  type: string
  field: string
  severity: Severity
  scope: RuleScope
  enabled: boolean
  message: string
}

export interface ReconRow {
  id: string
  tenant: string
  /** What our own submission_jobs row says. */
  internal: JobState
  /** What the Access Point reports for the same job. */
  app: JobState
  detail: string
}

export interface RuleSetVersion {
  version: string
  meta: string
  tag: string
  kind: 'draft' | 'active' | 'arch'
}

export interface LearnedRule {
  key: string
  source: string
}

export interface DiffRow {
  sign: '+' | '~' | '−'
  key: string
  detail: string
  tag: 'ADDED' | 'CHANGED' | 'REMOVED'
  kind: 'added' | 'changed' | 'removed'
}

export type AuditTone = 'red' | 'amber' | 'green' | 'teal'

export type AuditObjectType = 'submission' | 'rule' | 'state'

export interface AuditEntry {
  id: string
  ts: string
  action: string
  object: string
  objectType: AuditObjectType
  tenant: string
  /** Display name of who acted — a person, or the literal 'system'. */
  actor: string
  /** Two-letter avatar initials for `actor` ('SY' for system events). */
  who: string
  tone: AuditTone
  glyph: ReactNode
}

export type AuditFilter = 'all' | AuditObjectType

export interface TenantMember {
  name: string
  initials: string
  role: 'admin' | 'preparer' | 'reviewer'
}

export interface TenantKpi {
  label: string
  value: string
  tone?: 'green' | 'amber' | 'red'
}

export interface TenantRecent {
  invoice: string
  state: JobState
  age: string
}

export interface Tenant {
  id: string
  name: string
  initials: string
  tin: string
  status: 'ok' | 'warn' | 'red'
  entityCount: string
  kpis: TenantKpi[]
  members: TenantMember[]
  recent: TenantRecent[]
}

export interface HealthCard {
  label: string
  value: string
  unit: string
  status: string
  tone: 'green' | 'amber' | 'red'
  points: number[]
}

export interface NavItem {
  key: Screen
  label: string
  glyph: ReactNode
}

export type DrawerState = { type: 'job'; id: string } | { type: 'audit'; id: string } | { type: 'rule'; id: string } | null

export type ToastTone = 'ok' | 'red'

// Nullable, matching the ops console: null IS the "no toast" state, so every consumer
// narrows with `NonNullable<ToastState>` rather than carrying its own `| null`.
export type ToastState = {
  msg: string
  tag: string
  tone: ToastTone
} | null
