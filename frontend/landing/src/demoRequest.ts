// The landing's demo-request client: POST /contacts/demo-request on the gateway. Wire contract: internal/gateway/contacts.go DemoRequestHandler.
import { apiFetch, gatewayBase } from '@invoice-os/api-client/client'

import { MARKETING_CONSENT_TEXT } from './components/MarketingConsent'
import type { DemoLead } from './hubspot'

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
  }).then(() => undefined)
}
