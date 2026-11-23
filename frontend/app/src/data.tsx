// Seed data re-authored from the prototype's `this.SECTORS` / `this.CFG` (constructor,
// Platform.dc.html ~L983-998) as typed TS constants. `buildClients()` (src/lib/clients.ts)
// turns this into the full `Client[]` the app renders.
//
// Note: the prototype's raw CFG literal also carries `vd`/`vatd`/`fd`/`pd`/`validated`/
// `dist`/`docs`/`health`/`vat`/`vatNum`/`failing`/`pending`/`head` per company — every one
// of those is unconditionally recomputed (and so overwritten) inside `buildClients()`, and
// none is read anywhere else in the render output. They are intentionally omitted here;
// dropping them changes nothing about what's rendered.

import { IMPORT_KEYS, INVOICE_FIELDS } from './lib/invoiceFields'
import type { CanonField, ClientCfg, SectorDef, SectorKey, SettingsTab } from './types'

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

// Honeywell Group (index 4 above) — one of the 5 non-onboarding rows lib/clients.ts's
// DEMO_TEMPLATES cycles onto every REAL portfolio entity (buildClientForEntity, keyed by
// a hash of the entity's own id, never array position or name). Before task-304
// (INVCR-01-19) this row was ALSO reused directly, by name, as the in-house tenant's
// permanent stand-in profile (lib/clients.ts's now-deleted inhouseClient()) — in-house
// had no business_entities row of its own to hash. Now that db/seed.dev.sql seeds
// in-house one real entity, its Client goes through the same cycled-by-hash path as
// every other entity, firm or in-house; this row lands on it only if the hash says so,
// same as any other CFG template.

/* ------------------------------------------------------------------ */
/* Create-flow static content                                          */
/* ------------------------------------------------------------------ */

// The document path's strip. No Map step — a document's fields are extracted, never
// mapped onto columns.
export const WIZARD_STEPS: [string, string][] = [
  ['1', 'Import'],
  ['2', 'Review'],
]

// Manual entry's strip. One screen, one round trip (INVCR-01-03).
export const ENTER_STEPS: [string, string][] = [
  ['1', 'Enter'],
]

// Canonical invoice fields the Map step targets: the import fields of the shared list.
export const CANON: CanonField[] = IMPORT_KEYS.map((key) =>
  INVOICE_FIELDS.some((f) => f.importKey === key && f.required) ? { key, required: true } : { key },
)

// The buyer-type vocabulary; the Map step's buyer-type select (CreateMapping) reads it.
export const DOC_TYPE_DEFS: [string, string, string][] = [
  ['B2B', 'Business', 'Standard tax invoice'],
  ['B2G', 'Government', 'Routed to MDA portal'],
  ['B2C', 'Consumer', 'Simplified / aggregated'],
]

/* ------------------------------------------------------------------ */
/* Settings — connectors / API & webhooks / signing & certificates     */
/* ------------------------------------------------------------------ */

// Array order IS tab order (SettingsView maps this straight through). Annotated with
// `SettingsTab` rather than an inline copy of that union, so widening the union can
// never again leave two places to edit.
export const SETTINGS_TABS: { id: SettingsTab; label: string }[] = [
  { id: 'members', label: 'Members' },
  { id: 'roles', label: 'Roles' },
  { id: 'connectors', label: 'ERP connectors' },
  { id: 'api', label: 'API & webhooks' },
  { id: 'signing', label: 'Signing & certificates' },
  { id: 'workspace', label: 'Workspace' },
]

export const API_BASE = 'https://api.ascomply.com/v1'

export type EndpointDef = { m: 'POST' | 'GET'; path: string; desc: string }

export const ENDPOINTS: EndpointDef[] = [
  { m: 'POST', path: '/invoices', desc: 'Create & validate an invoice' },
  { m: 'GET', path: '/invoices/{id}', desc: 'Retrieve invoice + validation result' },
  { m: 'POST', path: '/invoices/{id}/transmit', desc: 'Transmit to NRS · returns IRN, CSID, QR' },
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
  { name: 'Digital signing certificate', cn: 'CN=ASComply SI · O=Okafor & Partners', issuer: 'NRS MBS Root CA', serial: '3A:F2:8B:14:9C:02:7D:E1', issued: '2026-01-12', expires: '2027-01-12', daysLeft: '209 days', pct: '58%', barColor: 'var(--action)' },
  { name: 'CSID stamping key', cn: 'RSA-2048 · SHA-256 signature', issuer: 'NRS APP CA', serial: '7B:11:6E:A0:33:91:C4:4E', issued: '2026-01-12', expires: '2027-01-12', daysLeft: '209 days', pct: '58%', barColor: 'var(--action)' },
]

export const EXPORTS_LIST: { name: string; fmt: string }[] = [
  { name: 'VAT return', fmt: 'CSV' },
  { name: 'Audit log', fmt: 'PDF' },
  { name: 'Invoice register', fmt: 'XLSX' },
  { name: 'WHT schedule', fmt: 'CSV' },
]

