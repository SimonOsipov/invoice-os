import { IMPORT_KEYS, INVOICE_FIELDS } from './invoiceFields.gen'

export { IMPORT_KEYS, INVOICE_FIELDS }

export type InvoiceField = (typeof INVOICE_FIELDS)[number]
export type ImportKey = NonNullable<InvoiceField['importKey']>
export type EditFieldKey = Extract<InvoiceField, { line: false; edit: true }>['key']
export type LineEditKey = Extract<InvoiceField, { line: true; edit: true }>['key']
export type LineWireRole = Extract<InvoiceField, { line: true; extract: true }>['key']
export type FormField = Extract<InvoiceField, { formKey: string }>

export function labelOf(key: InvoiceField['key']): string {
  return INVOICE_FIELDS.find((f) => f.key === key)!.label
}
