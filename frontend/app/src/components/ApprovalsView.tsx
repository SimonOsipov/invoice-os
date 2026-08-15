// Approvals screen (APPR-12-03). Empty shell only -- correct props signature so
// App.tsx's mount and this file's own RED specs can import/typecheck against it;
// the real body (fetch, rows, states, entity gate, copy) is Mode B's job.
import type { PlatformCtx } from '../types'

export function ApprovalsView({ ctx: _ctx }: { ctx: PlatformCtx }) {
  return null
}
