// AUDIT-07-10 stub (Stage 3/Mode A). collectExportRows throws until the executor fills it in.
// Types are real (from audit.ts), not invented -- the RED specs pin the walk/cap/error contract.

import type { AuditEvent, AuditLogQuery, AuditResponse } from './audit'

export type AuditExportFetchPage = (query: AuditLogQuery) => Promise<AuditResponse>

export interface AuditExportResult {
  rows: AuditEvent[]
  truncated: boolean
}

export async function collectExportRows(
  _fetchPage: AuditExportFetchPage,
  _query: AuditLogQuery,
  _cap: number,
): Promise<AuditExportResult> {
  throw new Error('not implemented')
}
