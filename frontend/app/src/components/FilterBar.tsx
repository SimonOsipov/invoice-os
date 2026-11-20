import type { ReactNode } from 'react'

// The Review invoices toolbar row (the ReviewInvoicesTab component), for filters that sit above a list.
export function FilterBar({ testId, children }: { testId: string; children: ReactNode }) {
  return (
    <div data-testid={testId} style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 16 }}>
      {children}
    </div>
  )
}
