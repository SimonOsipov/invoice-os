// The audit filter card's five popover triggers + pills row (AUDIT-07). This subtask
// (AUDIT-07-02) wires only search + date-range; events/actor/company land in
// AUDIT-07-04..06, the pills row in AUDIT-07-07.
//
// STUB for Test-Spec (Mode A) -- renders nothing. Real anatomy lands with the executor
// pass that turns AuditFilterCard.test.tsx green.

import type { AuditFacets } from '../lib/audit'
import type { AuditFilterState } from '../lib/auditFilters'

export interface AuditFilterCardProps {
  state: AuditFilterState
  facets: AuditFacets
  busy: boolean
  onChange: (next: AuditFilterState) => void
}

export function AuditFilterCard(_props: AuditFilterCardProps): JSX.Element | null {
  return null
}
