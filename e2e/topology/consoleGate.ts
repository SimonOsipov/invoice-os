import type { Page } from '@playwright/test'

// The console errors the topology gate may ignore: a deliberate, URL-scoped non-2xx.
//
// The invoice detail page reads its approval run on mount, and that GET answers 404 for
// an invoice with no run. Chromium logs every failed load as a console error, so without
// this every detail-page test trips its own `collectErrors` gate — measured on PR #167:
// 22 tests across invoice-surfaces and import-wizard, none of them a real defect.
//
// The 404 is deliberate and must NOT become a 200-with-null-run: `read_model.go:77-79`
// answers unknown, cross-tenant, malformed-uuid and no-run ids alike on purpose, and
// splitting the no-run case out would turn that into a cross-tenant existence oracle
// (.claude/rules/approvals.md). So the gate carries the exception, not the API.
//
// Two signals, because neither alone is reliable. Chromium usually names the failing
// resource in the message's own location, but PR #167's first gate run produced two 404
// lines that location could not attribute, on a run whose retry attributed its one line
// fine. So the real 404 RESPONSES are counted too, and at most that many unattributable
// 404 lines are dropped. A 404 from any other URL still fails the gate, which is the
// property that matters — this narrows the gate, it does not switch it off.
// A deliberate 409 in an oracle (a taken invoice number) uses the same two signals.
//
// `portfolio.spec.ts` holds its own `collectErrors` copy and does not need this yet; it can
// call it if it ever opens a detail page.
const APPROVAL_RUN_URL = /\/api\/invoice\/v1\/invoices\/[^/]+\/approval$/

export type Dropper = (text: string, url: string | undefined) => boolean

// The two-signal mechanism above, parameterised by status and which URLs may answer it.
// `urlPattern` is the whole narrowing: this status from anywhere else still fails the gate.
export function expectedStatusDropper(page: Page, status: number, urlPattern: RegExp): Dropper {
  let budget = 0
  const needle = `status of ${status}`
  page.on('response', (res) => {
    if (res.status() === status && urlPattern.test(res.url())) budget += 1
  })

  return (text, url) => {
    if (!text.includes(needle)) return false
    // A line that names its resource is judged on that alone, so this status from
    // anywhere else is never masked just because an expected one happened to occur too.
    if (url != null && url !== '') return urlPattern.test(url)
    // Nameless line: fall back to the response count, and spend it.
    if (budget === 0) return false
    budget -= 1
    return true
  }
}

export function expected404Dropper(page: Page, urlPattern: RegExp): Dropper {
  return expectedStatusDropper(page, 404, urlPattern)
}

export function approvalRun404Dropper(page: Page): Dropper {
  return expected404Dropper(page, APPROVAL_RUN_URL)
}

// ROUTE-02-07 (X-3): a deep link to an id that does not exist for the viewer 404s FOUR
// times, not once -- LiveInvoiceDetail fires getInvoice, getInvoiceHistory,
// getSourceDocument and getInvoiceApprovalRun unconditionally on mount
// (InvoiceDetail.tsx:170-211), and under FORCE RLS all four answer 404 alike for a
// cross-tenant or absent id. Those 404s ARE that test's premise, so the gate must expect
// them; scoping the pattern to the ids the test itself deep-links keeps every other 404
// -- including one on a real invoice -- a failure.
export function notFoundIdDropper(page: Page, ids: string[]): Dropper {
  const alternation = ids.map((id) => id.replace(/[^a-zA-Z0-9-]/g, '')).join('|')
  return expected404Dropper(page, new RegExp(`/api/invoice/v1/invoices/(?:${alternation})(?:[/?]|$)`))
}

// Origin and path only: a URL's query or fragment can carry a token, and these strings reach CI logs.
function originAndPath(url: string): string {
  return url.replace(/[?#].*$/, '').replace(/eyJ[\w.-]*/g, '<jwt>')
}

// Names the source of a console error: the message's location url plus, for a
// `status of NNN` line, the recorded non-2xx responses of that status ("<status> <METHOD> <url>").
export function sourcedConsoleError(text: string, locationUrl: string, failed: string[]): string {
  const parts: string[] = []
  if (locationUrl !== '') parts.push(`url: ${originAndPath(locationUrl)}`)
  const status = /status of (\d{3})/.exec(text)?.[1]
  if (status) {
    const seen = failed
      .filter((f) => f.startsWith(`${status} `))
      .map((f) => {
        const at = f.lastIndexOf(' ')
        return f.slice(0, at + 1) + originAndPath(f.slice(at + 1))
      })
    parts.push(`${status} responses: ${seen.length > 0 ? seen.join(', ') : 'none seen'}`)
  }
  return parts.length > 0 ? `${text} [${parts.join('; ')}]` : text
}

// Console/pageerror collector whose console errors carry their source. An entry lists the
// responses of its status seen up to its console line; a later response only fills an
// entry of that status that still reads `none seen`.
export function collectSourcedErrors(page: Page, extra?: Dropper): string[] {
  const errors: string[] = []
  const failed: string[] = []
  const unfilled: { index: number; status: string; text: string; url: string }[] = []
  page.on('response', (res) => {
    if (res.status() < 400) return
    failed.push(`${res.status()} ${res.request().method()} ${res.url()}`)
    for (let i = unfilled.length - 1; i >= 0; i--) {
      const e = unfilled[i]
      if (e.status !== String(res.status())) continue
      errors[e.index] = sourcedConsoleError(e.text, e.url, failed)
      unfilled.splice(i, 1)
    }
  })
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    const text = msg.text()
    const url = msg.location().url
    if (extra?.(text, url)) return
    const status = /status of (\d{3})/.exec(text)?.[1]
    if (status && !failed.some((f) => f.startsWith(`${status} `))) {
      unfilled.push({ index: errors.length, status, text, url })
    }
    errors.push(sourcedConsoleError(text, url, failed))
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}
