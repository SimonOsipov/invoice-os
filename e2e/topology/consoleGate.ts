// The one console error the topology gate must ignore.
//
// The invoice detail page reads its approval run on mount, and that GET answers 404
// for an invoice with no run. Chromium logs every failed load as a console error, so
// without this every detail-page test trips its own `collectErrors` gate — measured on
// PR #167: 22 tests across invoice-surfaces and import-wizard, none of them a real defect.
//
// The 404 is deliberate and must NOT become a 200-with-null-run: `read_model.go:77-79`
// answers unknown, cross-tenant, malformed-uuid and no-run ids alike on purpose, and
// splitting the no-run case out would turn that into a cross-tenant existence oracle
// (docs/approvals.md §2.1). So the gate carries the exception, not the API.
//
// Matched on the message's resource URL, never its text alone, so no other 404 is masked.
// `portfolio.spec.ts` and `personaSession.ts` hold their own `collectErrors` copies and
// do not need this yet; they can call it if they ever open a detail page.
const APPROVAL_RUN_URL = /\/api\/invoice\/v1\/invoices\/[^/]+\/approval$/

export function isApprovalRun404(text: string, url: string | undefined): boolean {
  return text.includes('status of 404') && url != null && APPROVAL_RUN_URL.test(url)
}
