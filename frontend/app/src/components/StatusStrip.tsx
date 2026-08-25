// RED stub (AUDIT-09-02) -- the signature is real, only the body is missing. Specs in
// StatusStrip.test.tsx fail on the throw below, not on a compile error. The explicit
// ReactNode return type is load-bearing: TS infers `void`, not `never`, for a declared
// function whose body only throws, and JSX then rejects the component.

import type { ReactNode } from 'react'

import type { StripNode } from '../lib/invoiceStrip'

export function StatusStrip(_props: { nodes: StripNode[] }): ReactNode {
  throw new Error('not implemented')
}
