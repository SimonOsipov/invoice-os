// Approvals trail rail card (APPR-13-03, task-553). Row rhythm precedent is Status
// history (InvoiceDetail.tsx:1024-1052) -- the sibling ordered-events card on this same
// rail, two slots below; only the numbered medallion (WorkflowSimulator.tsx:77-90) has no
// Status-history equivalent and is borrowed for that one piece. Wrapper/header recipe
// copied from SourceDocumentCard.tsx:84-90.
//
// STUB (Stage 2.5, Mode A, task-553): throws so every test fails on this throw, never on
// a missing module. Stage 3 replaces the body.
import type { AsyncState } from '@invoice-os/api-client'

import type { ApprovalRun } from '../lib/approvals'

export function ApprovalTrailCard(_props: { run: AsyncState<ApprovalRun | null> & { run: () => void } }): never {
  throw new Error('not implemented')
}
