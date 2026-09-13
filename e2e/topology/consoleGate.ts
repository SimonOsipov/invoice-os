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
// (docs/approvals.md §2.1). So the gate carries the exception, not the API.
//
// Two signals, because neither alone is reliable. Chromium usually names the failing
// resource in the message's own location, but PR #167's first gate run produced two 404
// lines that location could not attribute, on a run whose retry attributed its one line
// fine. So the real 404 RESPONSES are counted too, and at most that many unattributable
// 404 lines are dropped. A 404 from any other URL still fails the gate, which is the
// property that matters — this narrows the gate, it does not switch it off.
// A deliberate 409 in an oracle (a taken invoice number) uses the same two signals.
//
// `portfolio.spec.ts` and `personaSession.ts` hold their own `collectErrors` copies and
// do not need this yet; they can call it if they ever open a detail page.
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
