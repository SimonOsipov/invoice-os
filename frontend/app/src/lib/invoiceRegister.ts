import { ApiError } from '@invoice-os/api-client'

import { fetchAttachment } from './fetchAttachment'
import { SessionEndedError } from './renewal'

export interface RegisterRequest {
  entityId: string
  from: string // YYYY-MM-DD
  to: string
}

const pad = (n: number) => String(n).padStart(2, '0')
const ymd = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`

// Previous calendar month in the browser's local date.
export function previousMonthPeriod(now: Date): { from: string; to: string } {
  return {
    from: ymd(new Date(now.getFullYear(), now.getMonth() - 1, 1)),
    to: ymd(new Date(now.getFullYear(), now.getMonth(), 0)),
  }
}

// ISO dates compare as strings.
export function registerPeriodValid(p: { from: string; to: string }): boolean {
  return p.from !== '' && p.to !== '' && p.from <= p.to
}

export function invoiceRegisterUrl(base: string, r: RegisterRequest): string {
  const q = new URLSearchParams({ entity_id: r.entityId, from: r.from, to: r.to })
  return `${base}/api/invoice/v1/invoice-register?${q}`
}

export function fetchInvoiceRegister(
  getToken: () => string | null | Promise<string | null>,
  base: string,
  r: RegisterRequest,
  signal?: AbortSignal,
): Promise<{ blob: Blob; filename: string }> {
  return fetchAttachment(getToken, invoiceRegisterUrl(base, r), 'ASComply_invoice-register.xlsx', signal)
}

// The inline line for a failed download; null for a session end (the app is signing out).
export function registerErrorLine(err: unknown): string | null {
  if (err instanceof SessionEndedError) return null
  if (err instanceof ApiError && err.kind === 'http') {
    const sentence = err.body && typeof err.body === 'object' && 'error' in err.body ? String(err.body.error) : ''
    return sentence || `The register could not be built (HTTP ${err.status}). Try again.`
  }
  return 'Could not reach the server. Check your connection and try again.'
}
