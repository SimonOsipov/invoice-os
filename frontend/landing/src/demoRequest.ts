// The landing's demo-request client: POST /contacts/demo-request on the gateway. Wire contract: internal/gateway/contacts.go DemoRequestHandler.
import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client/client'

import { MARKETING_CONSENT_TEXT } from './components/MarketingConsent'
import type { DemoLead } from './hubspot'

// The gateway's per-IP limit answered 429 (http.StatusTooManyRequests in internal/gateway/contacts.go).
export class DemoRateLimited extends Error {}

// Null when no gateway is configured, so the caller can skip it without a rejection.
export function sendDemoRequest(lead: DemoLead): Promise<void> | null {
  const base = gatewayBase()
  if (!base) return null
  return apiFetch<unknown>(`${base}/contacts/demo-request`, {
    method: 'POST',
    body: {
      email: lead.email,
      name: lead.name,
      company: lead.company,
      ...(lead.marketing && { marketing_consent_text: MARKETING_CONSENT_TEXT }),
    },
  }).then(
    () => undefined,
    (e: unknown) => {
      if (e instanceof ApiError && e.kind === 'http' && e.status === 429) throw new DemoRateLimited('demo request rate limited')
      throw e
    },
  )
}
