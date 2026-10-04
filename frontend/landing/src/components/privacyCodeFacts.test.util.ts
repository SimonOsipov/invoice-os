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
  const create = found(/body\s*:=\s*map\[string\]string\{\s*"([a-z_]+)"/.exec(body)?.[1], 'the Resend create body')
  return unique([create, ...keysOf(/body\["([a-z_]+)"\]\s*=/g, body)])
}

export function resendSubscription(): string {
  return found(/"subscription":\s*"([a-z_]+)"/.exec(readSource('internal/notifications/resend.go'))?.[1], 'the Resend topic subscription')
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

// The contacts columns that hold the marketing tick and its time.
export function marketingColumns(): string[] {
  const cols = found(/INSERT INTO contacts \(([^)]*)\)/.exec(readSource('internal/notifications/store.go'))?.[1], 'the contacts INSERT column list')
  return cols.split(',').map((c) => c.trim()).filter((c) => c.startsWith('marketing_'))
}
