// What the privacy page must describe, read from the Go and TS sources that do it.
// A parse miss throws, so a moved fact fails the test instead of emptying the oracle.
import { readFileSync, readdirSync } from 'node:fs'
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

// Non-test newsletter*.go files of internal/notifications, comments stripped, joined.
function newsletterGo(): string {
  const dir = 'internal/notifications'
  const files = readdirSync(join(REPO, dir)).filter((f) => /^newsletter.*\.go$/.test(f) && !f.endsWith('_test.go'))
  if (files.length === 0) throw new Error('privacyCodeFacts: no newsletter*.go found')
  return files.map((f) => readSource(`${dir}/${f}`)).join('\n')
}

// Hours a pending blog request lives (newsletter.go Subscribe).
export function newsletterPendingHours(): number {
  return Number(found(/INSERT INTO blog_subscription_requests[\s\S]*?expires_at\)[\s\S]*?now\(\) \+ interval '(\d+) hours'/.exec(newsletterGo())?.[1], 'the request expiry interval'))
}

// The SubscribeIntake fields, lower-cased.
export function newsletterIntakeFields(): string[] {
  const body = found(/type SubscribeIntake struct \{([^}]*)\}/.exec(newsletterGo())?.[1], 'SubscribeIntake')
  return keysOf(/^\s*(\w+)\s+\S+\s*$/gm, body).sort()
}

// The Resend contact property keys SyncBlog writes.
export function newsletterResendPropertyKeys(): string[] {
  const body = found(/func \(r \*BlogResend\) SyncBlog\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'BlogResend.SyncBlog')
  const literal = found(/props\s*:=\s*map\[string\]string\{([^}]*)\}/.exec(body)?.[1], 'the SyncBlog property literal')
  return unique(keysOf(/"([a-z_]+)"\s*:/g, literal))
}

// SyncBlog adds the contact to the Blog segment, opts it in to the Blog topic, and sends neither unsubscribe nor opt_out.
export function syncBlogAddsSegmentAndTopic(): boolean {
  const body = found(/func \(r \*BlogResend\) SyncBlog\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'BlogResend.SyncBlog')
  return /"\/segments\/"\+url\.PathEscape\(r\.keys\.SegmentID\)/.test(body) && /"id": r\.keys\.TopicID, "subscription": "opt_in"/.test(body)
}

// Confirm consumes the request once, only while unexpired, and answers ErrLinkInvalid otherwise.
export function confirmLinkIsSingleUseAndExpires(): boolean {
  const body = found(/func \(s \*NewsletterStore\) Confirm\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'NewsletterStore.Confirm')
  return /DELETE FROM blog_subscription_requests WHERE token_hash = \$1 AND expires_at > now\(\)/.test(body) && /ErrNoRows\) \{\s*return "", ErrLinkInvalid/.test(body)
}

// A second sign-up overwrites topics and frequency and replaces the earlier token (one pending row per email).
export function resubscribeReplacesRequest(): boolean {
  const body = found(/func \(s \*NewsletterStore\) Subscribe\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'NewsletterStore.Subscribe')
  return /ON CONFLICT \(email\) DO UPDATE SET\s*category_ids = EXCLUDED\.category_ids, frequency = EXCLUDED\.frequency, token_hash = EXCLUDED\.token_hash/.test(body)
}

// Confirm overwrites a known subscriber's topics and frequency.
export function confirmOverwritesSubscriber(): boolean {
  const body = found(/func \(s \*NewsletterStore\) Confirm\b[\s\S]*?\n}\n/.exec(newsletterGo())?.[0], 'NewsletterStore.Confirm')
  return /ON CONFLICT \(email\) DO UPDATE SET\s*category_ids = EXCLUDED\.category_ids, frequency = EXCLUDED\.frequency/.test(body)
}

// Confirm stores the confirmation time on the subscriber row.
export function confirmStoresConfirmedAt(): boolean {
  return /INSERT INTO blog_subscribers \([^)]*confirmed_at\)\s*VALUES \([^)]*now\(\)\)/.test(newsletterGo())
}

// The confirmation mail goes through the accountmail Sender, which the Resend sender implements against api.resend.com.
export function confirmationMailGoesThroughResend(): boolean {
  const handler = newsletterGo()
  const sender = readSource('internal/accountmail/sender.go')
  return (
    /Sender\s+accountmail\.Sender/.test(handler) &&
    /RenderBlogConfirm\(/.test(handler) &&
    /n\.Sender\.Send\(/.test(handler) &&
    /ResendBaseURL\s*=\s*"https:\/\/api\.resend\.com"/.test(sender) &&
    /func \(r \*Resend\) Send\(/.test(sender)
  )
}

// True when a non-test newsletter*.go file names HubSpot (worker.go is the control that does).
export function newsletterCodeNamesHubSpot(): boolean {
  return /hubspot/i.test(newsletterGo())
}
export function workerNamesHubSpot(): boolean {
  return /hubspot/i.test(readSource('internal/notifications/worker.go'))
}

// contacts.md tells staff to put the unsubscribe merge tag in every blog broadcast.
export function rulePutsUnsubscribeInEveryBroadcast(): boolean {
  return readFileSync(join(REPO, '.claude/rules/contacts.md'), 'utf8').includes('Put `{{{RESEND_UNSUBSCRIBE_URL}}}` in every blog broadcast.')
}

// The blog confirm page tells the subscriber every email has a one-click unsubscribe.
export function confirmPageSaysOneClickUnsubscribe(): boolean {
  return readFileSync(join(REPO, 'frontend/blog/src/components/Confirm.tsx'), 'utf8').includes('Every email has a one-click unsubscribe.')
}
