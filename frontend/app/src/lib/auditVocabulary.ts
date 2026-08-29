// Human labels for every audit event the Go tree emits: 38 identifiers across 10 domains.
// auditVocabulary.test.ts scans audit.Record( call sites, so a new writer without a label
// here fails the suite rather than rendering a raw identifier at the user.
//
// Colour is reserved for OUTCOME, never for domain -- `tone` is derived from `outcome`
// alone, so a domain can never tint a row.

export type AuditDomain =
  | 'invoices'
  | 'approvals'
  | 'policies'
  | 'roles'
  | 'companies'
  | 'documents'
  | 'memberships'
  | 'validation'
  | 'submissions'
  | 'reconciliation'

export type AuditOutcome = 'good' | 'bad' | 'neutral'
export type AuditTone = 'green' | 'red' | 'amber' | null

export interface AuditEventDef {
  label: string
  domain: AuditDomain
  outcome?: AuditOutcome
}

export const AUDIT_EVENTS: Record<string, AuditEventDef> = {
  'invoice.created': { label: 'Invoice created', domain: 'invoices' },
  'invoice.updated': { label: 'Invoice updated', domain: 'invoices' },
  'invoice.validated': { label: 'Invoice validated', domain: 'invoices' },
  'invoice.transitioned': { label: 'Status changed', domain: 'invoices' },
  'invoice.kept_as_is': { label: 'Kept as is', domain: 'invoices' },
  'invoice.unkept_as_is': { label: 'Kept-as-is withdrawn', domain: 'invoices' },
  'invoice.resolved_outside': { label: 'Resolved outside the system', domain: 'invoices' },
  'invoice.unresolved_outside': { label: 'Outside resolution withdrawn', domain: 'invoices' },

  'invoice.approval_armed': { label: 'Approval requested', domain: 'approvals' },
  'invoice.approval_approved': { label: 'Approved', domain: 'approvals', outcome: 'good' },
  'invoice.approval_rejected': { label: 'Rejected', domain: 'approvals', outcome: 'bad' },
  'invoice.approval_cancelled': { label: 'Approval cancelled', domain: 'approvals', outcome: 'neutral' },

  'approval_policy.created': { label: 'Policy created', domain: 'policies' },
  'approval_policy.updated': { label: 'Policy updated', domain: 'policies' },
  'approval_policy.published': { label: 'Policy published', domain: 'policies' },
  'approval_policy.deleted': { label: 'Policy deleted', domain: 'policies' },

  'workflow_role.created': { label: 'Role created', domain: 'roles' },
  'workflow_role.updated': { label: 'Role updated', domain: 'roles' },
  'workflow_role.deleted': { label: 'Role deleted', domain: 'roles' },
  'workflow_role.staffed': { label: 'Role staffed', domain: 'roles' },

  'portfolio.entity.created': { label: 'Company added', domain: 'companies' },
  'portfolio.entity.updated': { label: 'Company updated', domain: 'companies' },
  'portfolio.entity.onboarded': { label: 'Company onboarded', domain: 'companies' },
  'portfolio.entity.offboarded': { label: 'Company offboarded', domain: 'companies' },

  'document.created': { label: 'Document uploaded', domain: 'documents' },
  'document.read': { label: 'Document opened', domain: 'documents' },
  'document.reused': { label: 'Document reused', domain: 'documents' },
  'extraction.succeeded': { label: 'Extraction completed', domain: 'documents', outcome: 'good' },
  'extraction.failed': { label: 'Extraction failed', domain: 'documents', outcome: 'bad' },

  'membership.suspended': { label: 'Member suspended', domain: 'memberships', outcome: 'neutral' },
  'membership.reactivated': { label: 'Member reactivated', domain: 'memberships', outcome: 'neutral' },

  'validation.rule.enabled': { label: 'Rule enabled', domain: 'validation' },
  'validation.rule.disabled': { label: 'Rule disabled', domain: 'validation' },

  'submission.accepted': { label: 'Transmission accepted', domain: 'submissions', outcome: 'good' },
  'submission.rejected': { label: 'Transmission rejected', domain: 'submissions', outcome: 'bad' },
  'submission.failed': { label: 'Transmission failed', domain: 'submissions', outcome: 'bad' },

  'reconciliation.drift_detected': { label: 'Drift detected', domain: 'reconciliation', outcome: 'neutral' },
  'reconciliation.auto_fixed': { label: 'Drift auto-corrected', domain: 'reconciliation', outcome: 'good' },
}

const TONE: Record<AuditOutcome, AuditTone> = { good: 'green', bad: 'red', neutral: 'amber' }

export interface AuditEventView {
  label: string
  domain: AuditDomain | null
  tone: AuditTone
}

// An unrecognised identifier still renders a readable label -- never the raw string as the
// primary label -- and never takes a tone, because nothing proves it bears an outcome.
export function auditEventView(event: string): AuditEventView {
  const def = AUDIT_EVENTS[event]
  if (!def) return { label: humanise(event), domain: null, tone: null }
  return { label: def.label, domain: def.domain, tone: def.outcome ? TONE[def.outcome] : null }
}

function humanise(event: string): string {
  const tail = event.split('.').pop() ?? event
  const words = tail.replace(/_/g, ' ').trim()
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : 'Unknown event'
}
