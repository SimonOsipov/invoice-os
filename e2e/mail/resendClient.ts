// Resend reads for the nightly mail check. Errors name the status only: a body can hold an address.
const API = 'https://api.resend.com'
const POLL_MS = 5_000
const LOOKBACK_MS = 60_000
const LIST_RETRIES = 3
const RETRY_MS = 2_000

export type Fetch = typeof fetch
export type MailItem = { id: string; to: string[]; subject: string; created_at: string }
export type ReceivedMail = MailItem & { html: string | null; text: string | null }
type Page = { data: MailItem[]; has_more: boolean }

// Resend writes "2026-10-24 20:24:01.467000+00".
export function parseCreatedAt(s: string): number {
  const t = Date.parse(s.replace(' ', 'T').replace(/([+-]\d\d)$/, '$1:00'))
  if (Number.isNaN(t)) throw new Error('Resend sent a created_at that is not a date')
  return t
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

// 429, 5xx and a dropped connection are transient; any other 4xx is final.
class ResendError extends Error {
  readonly transient: boolean
  constructor(message: string, transient: boolean) {
    super(message)
    this.transient = transient
  }
}

async function getJson<T>(key: string, path: string, fetchFn: Fetch, retries = 0): Promise<T> {
  for (let attempt = 0; ; attempt++) {
    let err: ResendError
    try {
      const res = await fetchFn(`${API}${path}`, { headers: { Authorization: `Bearer ${key}` } })
      if (res.ok) return (await res.json()) as T
      err = new ResendError(`Resend answered ${res.status}`, res.status === 429 || res.status >= 500)
    } catch {
      err = new ResendError('Resend could not be reached', true)
    }
    if (!err.transient || attempt >= retries) throw err
    await sleep(RETRY_MS)
  }
}

// Newest first; stops once a page ends before `since`.
async function listFrom(base: string, key: string, since: Date, fetchFn: Fetch, retries = 0): Promise<MailItem[]> {
  const kept: MailItem[] = []
  let after = ''
  for (;;) {
    const page = await getJson<Page>(key, `${base}?limit=100${after && `&after=${after}`}`, fetchFn, retries)
    for (const item of page.data) if (parseCreatedAt(item.created_at) >= since.getTime()) kept.push(item)
    const last = page.data[page.data.length - 1]
    if (!page.has_more || !last || parseCreatedAt(last.created_at) < since.getTime()) return kept
    after = last.id
  }
}

export const listSent = (key: string, since: Date, fetchFn: Fetch = fetch) =>
  listFrom('/emails', key, since, fetchFn, LIST_RETRIES)

export const listReceived = (key: string, since: Date, fetchFn: Fetch = fetch) =>
  listFrom('/emails/receiving', key, since, fetchFn)

export const getReceived = (key: string, id: string, fetchFn: Fetch = fetch) =>
  getJson<ReceivedMail>(key, `/emails/receiving/${encodeURIComponent(id)}`, fetchFn)

// A transient error inside the deadline is polled through; the deadline error names the last one.
// Matches the exact recipient and subject: the invite address gets two mails.
export async function waitForMail(
  key: string,
  to: string,
  subject: string,
  deadlineMs: number,
  fetchFn: Fetch = fetch,
): Promise<MailItem> {
  const start = Date.now()
  const since = new Date(start - LOOKBACK_MS)
  const want = to.toLowerCase()
  let last = ''
  for (;;) {
    try {
      const hit = (await listReceived(key, since, fetchFn)).find(
        (m) => m.subject === subject && m.to.some((a) => a.toLowerCase() === want),
      )
      if (hit) return hit
      last = ''
    } catch (e) {
      if (!(e instanceof ResendError && e.transient)) throw e
      last = ` (last: ${e.message})`
    }
    if (Date.now() - start >= deadlineMs) throw new Error(`no mail in ${deadlineMs / 60_000} minutes${last}`)
    await sleep(POLL_MS)
  }
}
