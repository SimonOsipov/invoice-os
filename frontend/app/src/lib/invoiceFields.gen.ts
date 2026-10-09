// Generated from internal/invoicefields by: go test ./internal/invoicefields -run TestInvoiceFields_TheSPAFileIsGeneratedFromTheList -update
export const INVOICE_FIELDS = [
  {
    "key": "invoice_number",
    "label": "Invoice number",
    "type": "text",
    "line": false,
    "importKey": "invoice_number",
    "required": true,
    "formKey": "number",
    "edit": false,
    "extract": true
  },
  {
    "key": "issue_date",
    "label": "Issue date",
    "type": "date",
    "line": false,
    "importKey": "issue_date",
    "required": false,
    "formKey": "date",
    "edit": true,
    "extract": true
  },
  {
    "key": "supplier_tin",
    "label": "Supplier TIN",
    "type": "text",
    "line": false,
    "importKey": null,
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "supplier_name",
    "label": "Supplier name",
    "type": "text",
    "line": false,
    "importKey": null,
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "buyer_tin",
    "label": "Buyer TIN",
    "type": "text",
    "line": false,
    "importKey": "buyer_tin",
    "required": false,
    "formKey": "buyerTin",
    "edit": true,
    "extract": true
  },
  {
    "key": "buyer_name",
    "label": "Buyer name",
    "type": "text",
    "line": false,
    "importKey": "buyer_name",
    "required": false,
    "formKey": "buyer",
    "edit": true,
    "extract": true
  },
  {
    "key": "currency",
    "label": "Currency",
    "type": "text",
    "line": false,
    "importKey": "currency",
    "required": false,
    "formKey": "currency",
    "edit": true,
    "extract": true
  },
  {
    "key": "subtotal",
    "label": "Subtotal",
    "type": "money",
    "line": false,
    "importKey": "subtotal",
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "vat",
    "label": "VAT",
    "type": "money",
    "line": false,
    "importKey": "vat",
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "total",
    "label": "Total",
    "type": "money",
    "line": false,
    "importKey": "total",
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "description",
    "label": "Description",
    "type": "text",
    "line": true,
    "importKey": "line_description",
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "quantity",
    "label": "Quantity",
    "type": "quantity",
    "line": true,
    "importKey": "line_quantity",
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "unit_price",
    "label": "Unit price",
    "type": "money",
    "line": true,
    "importKey": "line_unit_price",
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "line_total",
    "label": "Line total",
    "type": "money",
    "line": true,
    "importKey": null,
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  },
  {
    "key": "line_tax",
    "label": "Line tax",
    "type": "money",
    "line": true,
    "importKey": null,
    "required": false,
    "formKey": null,
    "edit": true,
    "extract": true
  }
] as const
