import { _INTERNAL_setSpanForScope, Scope } from '@sentry/core'

// v11 removed Scope.clear(); reset what it reset.
export function clearScope(scope: Scope): void {
  const s = scope as unknown as Record<string, unknown>
  const fresh = new Scope() as unknown as Record<string, unknown>
  for (const k of ['_breadcrumbs', '_attachments', '_user', '_tags', '_attributes', '_extra', '_contexts', '_sdkProcessingMetadata', '_propagationContext']) {
    s[k] = fresh[k]
  }
  for (const k of ['_level', '_fingerprint', '_transactionName', '_session', '_conversationId']) s[k] = undefined
  _INTERNAL_setSpanForScope(scope, undefined)
}
