// App-side validation-playground types + data-access helpers (M3-09-01). STUB — the
// executor implements the bodies next; every export below throws (or returns a wrong
// sentinel) so the RED specs in validationApi.test.ts (V1-V8) fail on a thrown/assertion
// mismatch, not an import or type error.
//
// validateInvoice is a thin wrapper around an injected authedFetch (the app-side 401
// seam from M3-07-02, src/lib/authedFetch.ts), mirroring listEntities/createEntity in
// portfolio.ts:
// - validateInvoice: POST `${base}/api/validation/v1/validate`, body = the raw
//   InvoicePayload, resolves the unwrapped ValidateResponse.
// Non-2xx responses reject with the underlying ApiError unchanged (apiFetch's own
// contract) — this helper must not swallow or reshape it.
//
// severityStyle is a pure StatusStyle mapper, following the established
// var(--status-<color>-{bg,border,text}) + uppercase-label convention (see
// entityStatusStyle in portfolio.ts / src/lib/clients.ts's statusStyle/pillFor):
// error -> red, warning -> amber, info -> muted.
//
// shouldValidate/playgroundState are pure render-decision helpers, mirroring
// shouldFetchEntities/clientsViewState in portfolio.ts: the no-gateway zero-network
// short-circuit (a deployed SPA with no backend behind it must make no network calls)
// means base==null => 'idle' regardless of async status; otherwise the view state
// mirrors async.status.
import type { AuthedFetch } from './portfolio'
import type { StatusStyle } from '../types'
import type { AsyncState, AsyncStatus } from '@invoice-os/api-client'

export type Severity = 'error' | 'warning' | 'info'

export interface Violation {
  rule_key: string
  severity: Severity
  message: string
  path?: string
}

export interface ValidateResponse {
  rule_set_version: number
  violations: Violation[]
}

export interface InvoicePayload {
  invoice: Record<string, unknown>
}

export async function validateInvoice(
  _authedFetch: AuthedFetch,
  _base: string,
  _payload: InvoicePayload,
): Promise<ValidateResponse> {
  throw new Error('not implemented')
}

export function severityStyle(_sev: Severity): StatusStyle {
  throw new Error('not implemented')
}

export function shouldValidate(_base: string | null): boolean {
  throw new Error('not implemented')
}

export function playgroundState(_base: string | null, _s: AsyncState<ValidateResponse>): AsyncStatus {
  throw new Error('not implemented')
}
