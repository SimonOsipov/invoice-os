// STUB (AUDIT-08-04 Stage 2.5): renders nothing so every RED spec fails on its own assertion,
// never on module resolution. The Execution stage replaces this body with the real drawer.

import type { PlatformCtx } from '../types'

export interface EvidenceBundleDrawerProps {
  ctx: PlatformCtx
  base: string
  onClose: () => void
  onToast: (t: { kind: 'success' | 'error'; text: string }) => void
}

export function EvidenceBundleDrawer(_props: EvidenceBundleDrawerProps) {
  return null
}
