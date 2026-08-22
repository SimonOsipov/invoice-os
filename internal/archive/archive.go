// Package archive assembles the tenant evidence bundle (AUDIT-05): invoices, status
// history, submissions and exchange attempts for one company over a period, as a ZIP.
// Pure package for now — no DB, no HTTP, no ZIP writer yet (AUDIT-05-01 scope).
package archive

// maxBundleInvoices caps a bundle request (D-14: ~30k evidence rows at the cap).
const maxBundleInvoices = 10000
