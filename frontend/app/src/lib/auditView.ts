// Every user-visible string on the Audit screen, plus the decisions the screen makes.
//
// Copy and derivation live here, not in AuditView.tsx, per the shipped
// [bulk-copy-lives-in-the-lib] convention (lib/approvals.ts): vitest is `environment:
// node` for this project by default, so logic written into a component is logic a plain
// unit test cannot reach.

export const AUDIT_COPY = {
  eyebrow: 'COMPLIANCE',
  h1: 'Audit log',
  subtitle: 'Every recorded action, newest first',
  tenantFallback: 'This workspace',
  loading: 'Loading the audit log…',
  emptyTitle: 'Nothing recorded yet',
  emptyMessage: 'Actions appear here as soon as anyone creates, validates, approves or transmits an invoice.',
} as const
