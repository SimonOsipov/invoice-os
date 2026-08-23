// AUDIT-07-09 stub (Stage 2.5/Mode A). AUDIT_CSV_HEADER is deliberately wrong (empty) and
// every function throws `new Error('not implemented')` -- Stage 3 (the executor) fills
// both in. Company-scope rendering and RFC-4180 quoting must be COPIED from AuditRow.tsx
// and lib/reviewBatch.ts:675's csvCell, not imported -- neither is exported.

import type { AuditEvent } from './audit'

export const AUDIT_CSV_HEADER: string[] = []

export function auditCsv(_events: AuditEvent[]): string {
  throw new Error('not implemented')
}

export function auditCsvFilename(_now: Date): string {
  throw new Error('not implemented')
}

export interface AuditExportToastInput {
  rows: number
  bytes: number
  filename: string
  truncated: boolean
  cap: number
}

export function auditExportToastCopy(_input: AuditExportToastInput): string {
  throw new Error('not implemented')
}
