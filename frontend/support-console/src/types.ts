// Shared types for the Support Console. Mirrors the shape of the prototype's
// `this.state` (Support Console.dc.html:706-727) with the loose JS widened into
// discriminated unions where the console actually branches on them.

import type { ReactNode } from 'react'

export type Screen = 'submissions' | 'rules' | 'audit' | 'tenants' | 'health' | 'ai'

export type Env = 'sandbox' | 'live'

export type Severity = 'error' | 'warn' | 'info'

// The wire value (rule.go scope, 'document' in v1); the seed fixture also uses 'global' and 'tenant-override'.
export type RuleScope = string

export interface Rule {
  key: string
  type: string
  field: string
  severity: Severity
  scope: RuleScope
  enabled: boolean
  message: string
  params: Record<string, unknown>
  when: string | null
}

// Wire shapes: internal/validation/staff_codelists.go. status is open: the sync log owns its values.
export interface CodeListLatest {
  list: string
  synced_at: string
  entry_count: number
  added_count: number
  removed_count: number
  changed_count: number
  status: string
}
export interface CodeListSync {
  status: string
  synced_at: string
  entry_count: number
  /** At most CODES_SHOWN codes each; the counts are the true totals. */
  added: string[]
  removed: string[]
  changed: string[]
  added_count: number
  removed_count: number
  changed_count: number
}

export interface NavItem {
  key: Screen
  label: string
  glyph: ReactNode
}

export type DrawerState = { type: 'code-list'; id: string } | { type: 'rule'; id: string } | { type: 'new-rule' } | { type: 'job'; id: string } | { type: 'audit'; id: string } | { type: 'staff-change'; id: string } | { type: 'evidence' } | null

export type ToastTone = 'ok' | 'red'

// Nullable, matching the ops console: null IS the "no toast" state, so every consumer
// narrows with `NonNullable<ToastState>` rather than carrying its own `| null`.
export type ToastState = {
  msg: string
  tag: string
  tone: ToastTone
} | null
