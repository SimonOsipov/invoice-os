// Stub: UblDocumentCard.test.tsx is red against this until the card is built.
// `: ReactNode` is load-bearing: TS infers `void` for a body that only throws, and JSX rejects it.

import type { ReactNode } from 'react'

import type { PlatformCtx } from '../types'

export function UblDocumentCard(_props: {
  ctx: PlatformCtx
  base: string
  invoiceId: string
  invoiceNumber: string
  canView: boolean
  blockedReason: string | null
  editing: boolean
  onView: () => void
}): ReactNode {
  throw new Error('not implemented')
}
