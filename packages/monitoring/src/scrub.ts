// RED STUB — QA Mode A. Identity scrubbers; the executor implements D-6, D-10 and D-18.
import type { Breadcrumb, ErrorEvent, EventHint, SpanJSON, TransactionEvent } from '@sentry/core'

export function stripQuery(s: string): string {
  return s
}

export function redactSecrets(s: string): string {
  return s
}

export function scrubEvent(event: ErrorEvent): ErrorEvent {
  return event
}

export function scrubTransaction(event: TransactionEvent): TransactionEvent {
  return event
}

export function scrubSpan(span: SpanJSON): SpanJSON {
  return span
}

export function keepBreadcrumb(b: Breadcrumb): Breadcrumb | null {
  return b
}

export function dropEvent(_event: ErrorEvent, _hint: EventHint): boolean {
  return false
}

export function apiRoute(url: string): string {
  return url
}
