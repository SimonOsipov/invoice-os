// Seed data re-authored from the prototype's `this.SECTORS` / `this.CFG` (constructor,
// Platform.dc.html ~L983-998) as typed TS constants. `buildClients()` (src/lib/clients.ts)
// turns this into the full `Client[]` the app renders.
//
// Note: the prototype's raw CFG literal also carries `vd`/`vatd`/`fd`/`pd`/`validated`/
// `dist`/`docs`/`health`/`vat`/`vatNum`/`failing`/`pending`/`head` per company — every one
// of those is unconditionally recomputed (and so overwritten) inside `buildClients()`, and
// none is read anywhere else in the render output. They are intentionally omitted here;
// dropping them changes nothing about what's rendered.

import type { CanonField, ClientCfg, FieldMapRow, SectorDef, SectorKey } from './types'

export const SECTORS: Record<SectorKey, SectorDef> = {
  logistics: {
    buyers: ['Dangote Cement Plc', 'Nestlé Nigeria Plc', 'BUA Group', 'Flour Mills of Nigeria', 'PZ Cussons Nigeria', 'Nigerian Breweries'],
    items: ['Container haulage', 'Cold-chain transport', 'Last-mile delivery', 'Warehousing — monthly', 'Fleet leasing'],
    addr: ['7 Creek Rd, Apapa, Lagos', '12 Wharf Rd, Lagos', '5 Trinity Ave, Port Harcourt'],
    min: 480000,
    max: 5200000,
  },
  foods: {
    buyers: ['Shoprite Nigeria', 'SPAR Nigeria', 'Chicken Republic', 'Justrite Superstores', 'Ebeano Supermarket', 'HealthPlus Ltd'],
    items: ['Wholesale grains', 'Frozen goods supply', 'Beverage distribution', 'Cooking oil — bulk', 'Packaged snacks'],
    addr: ['9 Market St, Lagos', '3 Allen Ave, Ikeja', '22 Aba Rd, Aba'],
    min: 250000,
    max: 3200000,
  },
  oilfield: {
    buyers: ['Seplat Energy Plc', 'Oando Plc', 'Aiteo Group', 'Shell Nigeria', 'TotalEnergies NG', 'Chevron Nigeria'],
    items: ['Field equipment lease', 'Pipeline supplies', 'Safety gear — bulk', 'Drilling consumables', 'Logistics support'],
    addr: ['3 Aba Rd, Port Harcourt', 'KM12 Warri Rd', 'Trans-Amadi, PH'],
    min: 900000,
    max: 6800000,
  },
  trading: {
    buyers: ['Konga Stores', 'Jumia Nigeria', 'SLOT Systems', 'Pointek', 'Game Stores', 'Mega Plaza'],
    items: ['Electronics resale', 'Building materials', 'Office supplies', 'Textile bales', 'Hardware lot'],
    addr: ['22 Balogun St, Lagos', '14 Computer Village, Ikeja', '6 Main Mkt, Onitsha'],
    min: 120000,
    max: 1800000,
  },
  manufacturing: {
    buyers: ['Lafarge Africa', 'Julius Berger Nigeria', 'Cutix Plc', 'Vitafoam Nigeria', 'Beta Glass Plc', 'Notore Chemical'],
    items: ['Industrial components', 'Raw polymer supply', 'Packaging materials', 'Machinery parts', 'Bulk chemicals'],
    addr: ['Industrial Ave, Ikeja', 'Bompai, Kano', 'Sango-Ota, Ogun'],
    min: 600000,
    max: 5400000,
  },
  textile: {
    buyers: ['Da Viva Stores', 'Vlisco Nigeria', 'Sunflag Nigeria'],
    items: ['Woven fabric — bulk', 'Yarn supply', 'Dyed cotton lot'],
    addr: ['Bompai Industrial, Kano'],
    min: 300000,
    max: 2400000,
  },
}

export const CFG: ClientCfg[] = [
  {
    name: 'Lagos Freight & Logistics Ltd',
    short: 'Lagos Freight',
    initials: 'LF',
    tin: '20184412-0001',
    taxpayer: 'Large',
    sector: 'logistics',
    score: 94,
    vol: 60,
    failTarget: 0,
    readiness: [99, 96, 90],
    readinessNote: 'All core rule groups clear. Only a few optional fields remain on older invoices.',
  },
  {
    name: 'Sahara Foods Distribution Ltd',
    short: 'Sahara Foods',
    initials: 'SF',
    tin: '19847720-0001',
    taxpayer: 'Medium',
    sector: 'foods',
    score: 87,
    vol: 42,
    failTarget: 2,
    readiness: [96, 91, 74],
    readinessNote: '11 of 16 rule groups clear — resolve the open errors to reach transmit-ready.',
  },
  {
    name: 'Nigerian Delta Supplies Co.',
    short: 'Nigerian Delta',
    initials: 'ND',
    tin: '22310984-0001',
    taxpayer: 'Medium',
    sector: 'oilfield',
    score: 71,
    vol: 24,
    failTarget: 3,
    readiness: [84, 72, 61],
    readinessNote: '8 of 16 rule groups clear. TIN and address gaps are the main blockers.',
  },
  {
    name: 'Adeyemi & Sons Trading',
    short: 'Adeyemi & Sons',
    initials: 'AS',
    tin: '20991043-0001',
    taxpayer: 'Small',
    sector: 'trading',
    score: 63,
    vol: 14,
    failTarget: 4,
    readiness: [70, 64, 52],
    readinessNote: 'Only 6 of 16 groups clear. Bulk-fix TINs and totals to lift the score fast.',
  },
  {
    name: 'Honeywell Group',
    short: 'Honeywell',
    initials: 'HG',
    tin: '20665510-0001',
    taxpayer: 'Large',
    sector: 'manufacturing',
    score: 90,
    vol: 50,
    failTarget: 1,
    readiness: [98, 93, 82],
    readinessNote: '14 of 16 rule groups clear. Nearly transmit-ready across the board.',
  },
  {
    name: 'Kano Textile Mills Plc',
    short: 'Kano Textile',
    initials: 'KT',
    tin: '18772300-0001',
    taxpayer: 'Medium',
    sector: 'textile',
    score: null,
    vol: 0,
    readiness: [12, 4, 0],
    readinessNote: '',
    onboarding: true,
  },
]

// Honeywell Group — single company with its own finance department (in-house mode).
export const INHOUSE_IDX = 4

/* ------------------------------------------------------------------ */
/* Create-flow static content                                          */
/* ------------------------------------------------------------------ */

export const WIZARD_STEPS: [string, string][] = [
  ['1', 'Import'],
  ['2', 'Map'],
  ['3', 'Build'],
  ['4', 'Validate'],
  ['5', 'Approve'],
]

// Canonical invoice fields the Map step targets (Platform.dc.html ~L1115).
export const CANON: CanonField[] = [
  { key: 'invoice_number', required: true },
  { key: 'issue_date' },
  { key: 'buyer_tin' },
  { key: 'buyer_name' },
  { key: 'currency' },
  { key: 'subtotal' },
  { key: 'vat' },
  { key: 'total' },
  { key: 'line_description' },
  { key: 'line_quantity' },
  { key: 'line_unit_price' },
]

export type SampleFileDef = {
  id: string
  ext: string
  name: string
  meta: string
  iconBg: string
  iconColor: string
}

export const SAMPLE_FILES: SampleFileDef[] = [
  { id: 'pdf', ext: 'PDF', name: 'lagos-freight-INV-0482.pdf', meta: '1 PAGE · 142 KB', iconBg: 'var(--status-red-bg)', iconColor: 'var(--status-red-text)' },
  { id: 'img', ext: 'JPG', name: 'scan-invoice-0482.jpg', meta: 'IMAGE · 2.1 MB', iconBg: 'var(--accent-tint)', iconColor: 'var(--accent)' },
]

export const VAL_LABELS: string[] = [
  'Buyer TIN format',
  'Buyer billing address',
  'Mandatory seller fields',
  'VAT computed at 7.5%',
  'Line totals reconcile to header',
  'Currency declared · NGN',
  'Invoice number unique in ledger',
  'Invoice date within open period',
  'Withholding-tax logic',
  'Tax-point date valid',
  'Supplier VAT registration active',
  'Line HS / SKU codes present',
  'Rounding within ±0.01 tolerance',
  'Digital-signature slot reserved',
  'QR payload generated',
  'Document schema · UBL 2.1',
]

// The scanline now reads the file and detects columns, then hands off to Map —
// "Mapping to invoice fields" stopped being an animation and became a real step.
export const PARSE_LABELS: string[] = [
  'Reading file',
  'Detecting delimiter & encoding',
  'Reading header row',
  'Scanning line rows',
  'Detecting columns',
]

export const DOC_TYPE_DEFS: [string, string, string][] = [
  ['B2B', 'Business', 'Standard tax invoice'],
  ['B2G', 'Government', 'Routed to MDA portal'],
  ['B2C', 'Consumer', 'Simplified / aggregated'],
]

/* ------------------------------------------------------------------ */
/* Settings — connectors / API & webhooks / signing & certificates     */
/* ------------------------------------------------------------------ */

// The UBL 2.1 targets every connector maps onto, in the order the detail view lists
// them. Paths are the real ones lib/xml.ts emits, minus the /Invoice root.
const UBL_TARGETS: string[] = [
  'cbc:ID',
  'cbc:IssueDate',
  'cbc:DocumentCurrencyCode',
  'cac:AccountingCustomerParty/cac:Party/cac:PartyName/cbc:Name',
  'cac:AccountingCustomerParty/cac:Party/cac:PartyTaxScheme/cbc:CompanyID',
  'cac:TaxTotal/cbc:TaxAmount',
  'cac:LegalMonetaryTotal/cbc:LineExtensionAmount',
  'cac:LegalMonetaryTotal/cbc:PayableAmount',
]

// Each connector's native field names, in UBL_TARGETS order: SAP's table-column names,
// NetSuite's SuiteTalk fields, the QBO Invoice entity, Sage 300's ARIBH columns, Odoo's
// account.move fields, and the D365 CustInvoiceJour table.
const ERP_FIELDS: Record<ConnectorDef['id'], string[]> = {
  sap: ['VBRK-VBELN', 'VBRK-FKDAT', 'VBRK-WAERK', 'KNA1-NAME1', 'KNA1-STCD1', 'VBRK-MWSBK', 'VBRP-NETWR', 'VBRK-NETWR'],
  oracle: ['tranid', 'trandate', 'currency.symbol', 'entity.companyname', 'entity.custentity_tin', 'taxtotal', 'subtotal', 'total'],
  quickbooks: ['DocNumber', 'TxnDate', 'CurrencyRef.value', 'CustomerRef.name', 'CustomerRef.ResaleNum', 'TxnTaxDetail.TotalTax', 'Line.Amount', 'TotalAmt'],
  sage: ['INVNUMBER', 'INVDATE', 'CURNCODE', 'BILNAME', 'IDCUSTTAX1', 'TXAMOUNT1', 'EXTINVNET', 'INVNETWTX'],
  odoo: ['name', 'invoice_date', 'currency_id.name', 'partner_id.name', 'partner_id.vat', 'amount_tax', 'amount_untaxed', 'amount_total'],
  dynamics: ['InvoiceId', 'InvoiceDate', 'CurrencyCode', 'InvoiceAccountName', 'VATNum', 'SumTax', 'SumLineAmount', 'InvoiceAmount'],
}

function mappingOf(id: ConnectorDef['id']): FieldMapRow[] {
  return UBL_TARGETS.map((ubl, i) => ({ erp: ERP_FIELDS[id][i], ubl }))
}

export type ConnectorDef = {
  id: 'sap' | 'oracle' | 'quickbooks' | 'sage' | 'odoo' | 'dynamics'
  name: string
  cat: string
  mono: string
  host: string
  module: string
  mapping: FieldMapRow[]
}

export const CONNECTOR_DEFS: ConnectorDef[] = [
  { id: 'sap', name: 'SAP S/4HANA', cat: 'ERP', mono: 'SAP', host: 'erp.honeywell.ng:44300', module: 'SD/FI billing', mapping: mappingOf('sap') },
  { id: 'oracle', name: 'Oracle NetSuite', cat: 'ERP', mono: 'OR', host: 'td7842.suitetalk.api.netsuite.com', module: 'Invoicing', mapping: mappingOf('oracle') },
  { id: 'quickbooks', name: 'QuickBooks Online', cat: 'ACCOUNTING', mono: 'QB', host: 'quickbooks.api.intuit.com', module: 'Sales · Invoice', mapping: mappingOf('quickbooks') },
  { id: 'sage', name: 'Sage 300', cat: 'ACCOUNTING', mono: 'SG', host: 'sage300.honeywell.ng:8080', module: 'Accounts Receivable', mapping: mappingOf('sage') },
  { id: 'odoo', name: 'Odoo', cat: 'ERP', mono: 'OD', host: 'odoo.honeywell.ng', module: 'account.move', mapping: mappingOf('odoo') },
  { id: 'dynamics', name: 'Microsoft Dynamics 365', cat: 'ERP', mono: 'D365', host: 'honeywell.operations.dynamics.com', module: 'Accounts receivable', mapping: mappingOf('dynamics') },
]

// Mirrored from the ERP's tax-code table — the master-data card lists these, and its
// "Tax codes" stat tile counts them (see lib/connectors.ts).
export const CONNECTOR_TAX_CODES: { code: string; desc: string; rate: string }[] = [
  { code: 'A1', desc: 'Standard rated VAT', rate: '7.5%' },
  { code: 'Z0', desc: 'Zero rated', rate: '0%' },
  { code: 'E0', desc: 'Exempt', rate: '—' },
  { code: 'WH5', desc: 'Withholding — services', rate: '5%' },
  { code: 'WH10', desc: 'Withholding — rent & royalties', rate: '10%' },
]

export const SETTINGS_TABS: { id: 'connectors' | 'api' | 'signing'; label: string }[] = [
  { id: 'connectors', label: 'ERP connectors' },
  { id: 'api', label: 'API & webhooks' },
  { id: 'signing', label: 'Signing & certificates' },
]

export const API_BASE = 'https://api.fiscalbridge.africa/v1'

export type ApiKeyDef = { env: 'LIVE' | 'TEST'; envBg: string; envColor: string; key: string; note: string }

export const API_KEYS: ApiKeyDef[] = [
  { env: 'LIVE', envBg: 'var(--status-green-bg)', envColor: 'var(--status-green-text)', key: 'sk_live_a3F8b91c••••••••••••Kp2Q', note: 'Production — transmits to FIRS' },
  { env: 'TEST', envBg: 'var(--status-amber-bg)', envColor: 'var(--status-amber-text)', key: 'sk_test_9bX24fe1••••••••••••Lq7', note: 'Sandbox — simulated transmissions' },
]

export type EndpointDef = { m: 'POST' | 'GET'; path: string; desc: string }

export const ENDPOINTS: EndpointDef[] = [
  { m: 'POST', path: '/invoices', desc: 'Create & validate an invoice' },
  { m: 'GET', path: '/invoices/{id}', desc: 'Retrieve invoice + validation result' },
  { m: 'POST', path: '/invoices/{id}/transmit', desc: 'Transmit to FIRS · returns IRN, CSID, QR' },
  { m: 'GET', path: '/invoices/{id}/status', desc: 'Poll transmission status' },
]

export type WebhookDef = { event: string; url: string; st: string }

export const WEBHOOKS: WebhookDef[] = [
  { event: 'invoice.validated', url: 'https://erp.honeywell.ng/hooks/validated', st: 'ACTIVE' },
  { event: 'invoice.transmitted', url: 'https://erp.honeywell.ng/hooks/transmitted', st: 'ACTIVE' },
  { event: 'invoice.rejected', url: 'https://ops.honeywell.ng/alerts/firs', st: 'ACTIVE' },
]

export type CertDef = {
  name: string
  cn: string
  issuer: string
  serial: string
  issued: string
  expires: string
  daysLeft: string
  pct: string
  barColor: string
}

export const CERTS: CertDef[] = [
  { name: 'Digital signing certificate', cn: 'CN=FiscalBridge SI · O=Okafor & Partners', issuer: 'FIRS MBS Root CA', serial: '3A:F2:8B:14:9C:02:7D:E1', issued: '2026-01-12', expires: '2027-01-12', daysLeft: '209 days', pct: '58%', barColor: 'var(--accent)' },
  { name: 'CSID stamping key', cn: 'RSA-2048 · SHA-256 signature', issuer: 'FIRS APP CA', serial: '7B:11:6E:A0:33:91:C4:4E', issued: '2026-01-12', expires: '2027-01-12', daysLeft: '209 days', pct: '58%', barColor: 'var(--accent)' },
]

export const EXPORTS_LIST: { name: string; fmt: string }[] = [
  { name: 'VAT return', fmt: 'CSV' },
  { name: 'Audit log', fmt: 'PDF' },
  { name: 'Invoice register', fmt: 'XLSX' },
  { name: 'WHT schedule', fmt: 'CSV' },
]

export const ONBOARD_STEPS: { n: string; title: string; body: string; done: boolean }[] = [
  { n: '1', title: 'Company profile set', body: 'Tax details & numbering', done: true },
  { n: '2', title: 'Import or create invoices', body: 'CSV / XLSX or API', done: false },
  { n: '3', title: 'Run first validation', body: '16-check MBS rule pack', done: false },
  { n: '4', title: 'Activate transmission', body: 'FIRS adapter on accreditation', done: false },
]
