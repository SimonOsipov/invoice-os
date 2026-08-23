// Package archive assembles the tenant evidence bundle (AUDIT-05): invoices, status
// history, submissions and exchange attempts for one company over a period, as a ZIP.
// Reads business_entities, invoices, invoice_status_history, submission_jobs and
// app_exchange under RLS via a caller-supplied pgx.Tx (no pool of its own), plus
// memberships indirectly through actor.Resolve; no HTTP handler or ZIP writer yet.
package archive

// maxBundleInvoices caps a bundle request (D-14: ~30k evidence rows at the cap).
const maxBundleInvoices = 10000
