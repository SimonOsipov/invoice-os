// Test-only fixture: eight sample rules for the unit tests. No source file imports it.
import type { Rule } from './types'

export const SEED_RULES: Rule[] = [
  { key: 'buyer.tin.required', type: 'required', field: 'buyer.tin', severity: 'error', scope: 'global', enabled: true, message: 'Buyer TIN is mandatory', params: {}, when: null },
  { key: 'buyer.tin.format', type: 'format-regex', field: 'buyer.tin', severity: 'error', scope: 'global', enabled: true, message: 'TIN must match NNNNNNNN-NNNN', params: { pattern: '^\\d{8}-\\d{4}$' }, when: null },
  { key: 'vat.rate.taxmath', type: 'tax_math', field: 'lines[].vat', severity: 'error', scope: 'global', enabled: true, message: 'VAT must equal 7.5% of line net', params: { rate: 0.075, tolerance: 0.01 }, when: null },
  { key: 'wht.services.crossfield', type: 'cross_field', field: 'lines[].wht', severity: 'warn', scope: 'global', enabled: true, message: 'WHT expected on service lines', params: {}, when: null },
  { key: 'currency.enum', type: 'enum', field: 'header.currency', severity: 'error', scope: 'global', enabled: true, message: 'Currency must be NGN, USD or EUR', params: { values: ['NGN', 'USD', 'EUR'] }, when: null },
  { key: 'invoice.no.unique', type: 'expression-CEL', field: 'header.invoice_no', severity: 'error', scope: 'global', enabled: true, message: 'Invoice number must be unique per seller', params: {}, when: null },
  { key: 'issue.date.sequence', type: 'date_rule', field: 'header.issue_date', severity: 'warn', scope: 'tenant-override', enabled: true, message: 'Issue date must not precede prior invoice', params: {}, when: null },
  { key: 'line.qty.range', type: 'range', field: 'lines[].qty', severity: 'info', scope: 'global', enabled: false, message: 'Quantity outside expected range', params: { min: 1, max: 100000 }, when: null },
]
