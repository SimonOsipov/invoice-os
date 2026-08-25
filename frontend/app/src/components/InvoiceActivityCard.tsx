// AUDIT-09-04 stub. InvoiceActivityCard.test.tsx is RED against this on purpose; the
// executor replaces the body with .ralph/AUDIT-09-04-arch.md §1.
//
// The `: ReactNode` return type is load-bearing: TS infers `void` for a declared function
// whose body only throws, and JSX then rejects the element (TS2786).

import type { ReactNode } from 'react'

import type { PlatformCtx } from '../types'

export function InvoiceActivityCard(_props: { ctx: PlatformCtx; invoiceId: string }): ReactNode {
  throw new Error('not implemented')
}
