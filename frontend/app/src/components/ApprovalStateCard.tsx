// AUDIT-09-06 stub. ApprovalStateCard.test.tsx is RED against this on purpose; the
// executor replaces the body with .ralph/AUDIT-09-06-arch.md §3.3.
//
// The `: ReactNode` return type is load-bearing: TS infers `void` for a declared function
// whose body only throws, and JSX then rejects the element (TS2786).

import type { ReactNode } from 'react'

import type { AsyncState } from '@invoice-os/api-client'

import type { ApprovalRun } from '../lib/approvals'

export function ApprovalStateCard(_props: { run: AsyncState<ApprovalRun | null> & { run: () => void } }): ReactNode {
  throw new Error('not implemented')
}
