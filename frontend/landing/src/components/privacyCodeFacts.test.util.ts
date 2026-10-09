// What the privacy page must describe, read from the Go and TS sources that do it.
// A parse miss throws, so a moved fact fails the test instead of emptying the oracle.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { stripComments } from '@invoice-os/api-client/strip-comments'

const REPO = join(fileURLToPath(new URL('.', import.meta.url)), '..', '..', '..', '..')

export function readSource(rel: string): string {
  return stripComments(readFileSync(join(REPO, rel), 'utf8'))
}

function found<T>(value: T | null | undefined, what: string): T {
  if (value == null) throw new Error(`privacyCodeFacts: ${what} not found`)
  return value
}

const unique = (xs: string[]): string[] => Array.from(new Set(xs)).sort()
const keysOf = (re: RegExp, text: string): string[] => Array.from(text.matchAll(re), (m) => m[1])

// Properties internal/notifications/hubspot.go sends for a registrant or demo booker.
export function hubspotCrmKeys(): string[] {
  const body = found(/func \(h \*HubSpot\) Upsert\b[\s\S]*?\n}\n/.exec(readSource('internal/notifications/hubspot.go'))?.[0], 'HubSpot.Upsert')
  const literal = found(/range map\[string\]string\{([^}]*)\}/.exec(body)?.[1], 'the Upsert property literal')
  return unique([...keysOf(/"([a-z_]+)"\s*:/g, literal), ...keysOf(/props\["([a-z_]+)"\]\s*=/g, body)])
}

// Properties the browser's HubSpot Forms post sends for a demo booker (hubspot.ts buildSubmission).
export function hubspotFormsKeys(): string[] {
  const list = found(/const mapped[^=]*=\s*\[([\s\S]*?)\n\s*\]\n/.exec(readSource('frontend/landing/src/hubspot.ts'))?.[1], 'the mapped list')
  return unique(keysOf(/\[\s*'([a-z_]+)'\s*,/g, list))
}

// Fields internal/notifications/resend.go sends when it creates a contact.
export function resendContactKeys(): string[] {
  const body = found(/func \(r \*Resend\) Sync\b[\s\S]*?\n}\n/.exec(readSource('internal/notifications/resend.go'))?.[0], 'Resend.Sync')
  const literal = found(/body\s*:=\s*map\[string\]string\{([^}]*)\}/.exec(body)?.[1], 'the Resend create body')
  return unique([...keysOf(/"([a-z_]+)"\s*:/g, literal), ...keysOf(/body\["([a-z_]+)"\]\s*=/g, body)])
}

// Tag values worker.go gives a contact, in source order.
export function crmTags(): string[] {
  const tags = keysOf(/c\.Tags = append\(c\.Tags, "([^"]+)"\)/g, readSource('internal/notifications/worker.go'))
  if (tags.length === 0) throw new Error('privacyCodeFacts: no tag assignment in worker.go')
  return tags
}

// worker.go skips Resend for anyone neither registered nor ticked.
export function resendSkipsUntickedDemoBooker(): boolean {
  return /if !registered && !consent \{\s*return nil/.test(readSource('internal/notifications/worker.go'))
}

// GoTrue sends the verification email through this SMTP host (sidecar/auth/Dockerfile).
export function authSmtpHost(): string {
  return found(/GOTRUE_SMTP_HOST=(\S+)/.exec(readFileSync(join(REPO, 'sidecar/auth/Dockerfile'), 'utf8'))?.[1], 'GOTRUE_SMTP_HOST')
}

// True when a landing client sends a consent time of its own; the page may say "ticked" only then.
export function clientsSendConsentTime(): boolean {
  return ['frontend/landing/src/register.ts', 'frontend/landing/src/demoRequest.ts'].some((f) =>
    /marketing_consent_(?!text)\w+|consent(?:ed)?_at|ticked_at/.test(readSource(f)),
  )
}

// The gateway stamps the registration consent time itself, at the register request.
export function gatewayStampsRegisterConsentTime(): boolean {
  return /"at":\s*time\.Now\(\)/.test(readSource('internal/gateway/register.go'))
}

// The demo form awaits the HubSpot Forms post, then the gateway post: the server send follows the browser's.
export function demoPostsToOurServerAfterHubSpot(): boolean {
  return /await trackedHubSpotSubmit[\s\S]*?await sendDemoRequest/.test(readSource('frontend/landing/src/components/DemoLeadForm.tsx'))
}

// notifications queues each delivery as a River job in the intake transaction, so it can be late and is retried.
export function deliveryIsQueued(): boolean {
  return /river\.InsertTx\(/.test(readSource('internal/notifications/store.go'))
}

// A registrant's HubSpot company is the workspace name, not a company field (store.go Registrant).
export function registrantCompanyIsWorkspaceName(): boolean {
  return /company:\s*strings\.TrimSpace\(in\.WorkspaceName\)/.test(readSource('internal/notifications/store.go'))
}

const AI_CLIENTS = ['internal/platform/ai/client.go', 'internal/platform/jev/client.go']

// The host of each OpenRouter `endpoint` constant, de-duplicated.
export function aiProviderHosts(): string[] {
  return unique(AI_CLIENTS.map((f) => found(/\bendpoint\s*=\s*"https:\/\/([^\/"]+)/.exec(readSource(f))?.[1], `endpoint in ${f}`)))
}

// The vendor prefix of each client's `Model` constant (`~` dropped), in client order.
export function aiModelVendors(): string[] {
  return AI_CLIENTS.map((f) => found(/\bModel\s*=\s*"~?([^\/"]+)\//.exec(readSource(f))?.[1], `Model in ${f}`))
}
