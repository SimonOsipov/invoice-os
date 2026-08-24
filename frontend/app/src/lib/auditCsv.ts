// AUDIT-07-09: CSV export serializer + toast copy for the audit log.

import type { AuditEvent } from './audit'
import { auditEventView } from './auditVocabulary'
import { pad2 } from './format'
import { formatBytes } from './sourceDocument'

export const AUDIT_CSV_HEADER: string[] = [
  'When', 'Who', 'Actor kind', 'Actor id', 'What',
  'Event identifier', 'Company', 'Company scope', 'Event id',
]

// RFC-4180 quoting, copied from reviewBatch.ts:675's csvCell -- it's module-local there
// and not exported, so this is a copy, not an import.
function csvCell(value: string): string {
  return /[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value
}

// Copied from AuditRow.tsx:96-98's company_scope rule, not reconstructed.
function csvCompany(event: AuditEvent): string {
  return event.company_scope === 'company'
    ? (event.company_name ?? '—')
    : event.company_scope === 'workspace'
      ? 'Workspace'
      : '—'
}

export function auditCsv(events: AuditEvent[]): string {
  const lines = events.map((e) =>
    [
      e.created_at,
      e.actor_name,
      e.actor_kind,
      e.actor,
      auditEventView(e.event).label,
      e.event,
      csvCompany(e),
      e.company_scope,
      e.id,
    ]
      .map((v) => csvCell(String(v)))
      .join(','),
  )
  return [AUDIT_CSV_HEADER.join(','), ...lines].join('\n')
}

export function auditCsvFilename(now: Date): string {
  return `audit-log-${now.getUTCFullYear()}-${pad2(now.getUTCMonth() + 1)}-${pad2(now.getUTCDate())}.csv`
}

export interface AuditExportToastInput {
  rows: number
  bytes: number
  filename: string
  truncated: boolean
  cap: number
}

export function auditExportToastCopy({ rows, bytes, filename, truncated, cap }: AuditExportToastInput): string {
  // Row/cap counts stay raw digits (no thousands separator) -- callers match on the bare number.
  const capNote = truncated ? `, capped at ${cap} rows` : ''
  return `Exported ${rows} ${rows === 1 ? 'row' : 'rows'} to ${filename} (${formatBytes(bytes)})${capNote}. No attachments, no payloads, no invoices.`
}
