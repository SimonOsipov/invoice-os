// ceiling: mock input of the unmounted Connector detail; goes when the detail reads real data.

// Per-connector integration detail, derived deterministically from the connector id
// via the same seeded PRNG the client fixtures use (lib/prng.ts). Every connector shows
// stable but distinct numbers across reloads and across list <-> detail navigation.
//
// The figures are internally consistent, not independently random:
// - IN ERP  = the sum of the 30-day pull volume (the funnel's period IS the chart's period)
// - HELD    = IN ERP - SUBMITTED  ("pulled, not submitted")
// - DRIFT   = SUBMITTED - CLEARED  ("not yet cleared")
// - the master-data tax-code count is the length of the tax-code list rendered beside it
// - write-back covers the cleared documents: stamped + pending + failed = ACCEPTED

import { hash, mulberry } from './prng'
import { pad2 } from './format'
import type { ConnectorId, FieldMapRow } from '../types'

// The UBL 2.1 targets every connector maps onto, in the order the detail view lists
// them. Paths are the real ones internal/ubl/ubl.go emits, minus the /Invoice root.
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
// "Tax codes" stat tile counts them (see `connectorDetail`).
export const CONNECTOR_TAX_CODES: { code: string; desc: string; rate: string }[] = [
  { code: 'A1', desc: 'Standard rated VAT', rate: '7.5%' },
  { code: 'Z0', desc: 'Zero rated', rate: '0%' },
  { code: 'E0', desc: 'Exempt', rate: '—' },
  { code: 'WH5', desc: 'Withholding — services', rate: '5%' },
  { code: 'WH10', desc: 'Withholding — rent & royalties', rate: '10%' },
]

export type SyncEventKind = 'transmitted' | 'validated' | 'held' | 'scheduled' | 'pull'

export type SyncEvent = {
  time: string
  kind: SyncEventKind
  doc: string
  desc: string
}

export type HeldDoc = {
  doc: string
  reason: string
  age: string
}

export type ConnectorDetailData = {
  lastSync: string
  frequency: string
  queueDepth: number
  tokenExpires: string
  errorRate: string
  funnel: { inErp: number; validated: number; transmitted: number; accepted: number; drift: number }
  volume: number[]
  volumeTotal: number
  activity: SyncEvent[]
  master: { customers: number; taxCodes: number; items: number; uoms: number }
  writeBack: { stamped: number; pending: number; failed: number; pct: number }
  held: HeldDoc[]
  heldTotal: number
}

const FREQUENCIES = ['Every 5 min', 'Every 15 min', 'Every 30 min', 'Hourly']

const HELD_REASONS = [
  'Buyer TIN missing',
  'Billing address incomplete',
  'VAT does not reconcile to lines',
  'Currency not declared',
  'Duplicate invoice number',
  'Tax-point date outside open period',
]

/** Integer in [lo, hi]. */
function pick(rnd: () => number, lo: number, hi: number): number {
  return lo + Math.floor(rnd() * (hi - lo + 1))
}

function docId(rnd: () => number): string {
  return 'INV-' + pick(rnd, 2000, 2999)
}

// 30 daily pull counts with a weekly rhythm — the two weekend days of each 7-day
// block run light, which is what makes the bar chart read as an ERP and not as noise.
function volumeSeries(rnd: () => number): number[] {
  return Array.from({ length: 30 }, (_, i) => {
    const weekend = i % 7 === 5 || i % 7 === 6
    const base = 34 + rnd() * 56
    return Math.round(weekend ? base * 0.34 : base)
  })
}

function activityFeed(rnd: () => number, def: ConnectorDef): SyncEvent[] {
  const kinds: SyncEventKind[] = ['transmitted', 'validated', 'held', 'transmitted', 'pull', 'validated', 'transmitted']
  let minutes = 14 * 60 + 32
  const rows: SyncEvent[] = [
    { time: '15:00', kind: 'scheduled', doc: '—', desc: 'Next scheduled pull' },
  ]
  kinds.forEach((kind) => {
    const doc = docId(rnd)
    const desc =
      kind === 'transmitted'
        ? 'Submitted · simulated IRN assigned'
        : kind === 'validated'
          ? 'Passed 19-check MBS rule pack'
          : kind === 'held'
            ? 'Held — ' + HELD_REASONS[pick(rnd, 0, HELD_REASONS.length - 1)]
            : 'Pulled ' + pick(rnd, 8, 46) + ' documents from ' + def.name
    rows.push({ time: pad2(Math.floor(minutes / 60)) + ':' + pad2(minutes % 60), kind, doc: kind === 'pull' ? '—' : doc, desc })
    minutes -= pick(rnd, 4, 23)
  })
  return rows
}

// Oldest first — an exceptions queue that isn't sorted by age is the one thing a
// finance team would never ship.
function heldDocs(rnd: () => number, count: number): HeldDoc[] {
  const rows = Array.from({ length: Math.min(4, count) }, () => ({
    doc: docId(rnd),
    reason: HELD_REASONS[pick(rnd, 0, HELD_REASONS.length - 1)],
    minutes: pick(rnd, 45, 2400),
  }))
  rows.sort((a, b) => b.minutes - a.minutes)
  return rows.map(({ doc, reason, minutes }) => {
    const h = Math.floor(minutes / 60)
    return { doc, reason, age: h < 24 ? h + 'h ' + (minutes % 60) + 'm' : Math.floor(h / 24) + 'd ' + (h % 24) + 'h' }
  })
}

export function connectorDetail(def: ConnectorDef): ConnectorDetailData {
  const rnd = mulberry(hash('connector:' + def.id))

  const volume = volumeSeries(rnd)
  const inErp = volume.reduce((s, v) => s + v, 0)
  const validated = inErp - pick(rnd, 8, 60)
  const transmitted = validated - pick(rnd, 3, 26)
  // Weighted so roughly half the connectors sit at a clean drift of 0 (green badge).
  const drift = rnd() < 0.45 ? 0 : pick(rnd, 1, 9)
  const accepted = transmitted - drift
  const heldTotal = inErp - transmitted

  // Write-back is driven from a target percentage rather than an absolute pending count:
  // at these volumes a fixed 3-22 pending rounds to a 99% bar on every connector, which
  // makes the progress bar read as decoration. The remainder splits pending/failed.
  const stamped = Math.round(accepted * (0.86 + rnd() * 0.135))
  const failed = Math.min(accepted - stamped, pick(rnd, 0, 5))
  const pending = accepted - stamped - failed

  return {
    lastSync: pick(rnd, 1, 14) + ' min ago',
    frequency: FREQUENCIES[pick(rnd, 0, FREQUENCIES.length - 1)],
    queueDepth: pick(rnd, 0, 38),
    tokenExpires: 'in ' + pick(rnd, 4, 88) + ' days',
    errorRate: (rnd() * 1.4).toFixed(2) + '%',
    funnel: { inErp, validated, transmitted, accepted, drift },
    volume,
    volumeTotal: inErp,
    activity: activityFeed(rnd, def),
    master: { customers: pick(rnd, 180, 1400), taxCodes: CONNECTOR_TAX_CODES.length, items: pick(rnd, 400, 4200), uoms: pick(rnd, 8, 26) },
    writeBack: { stamped, pending, failed, pct: Math.round((stamped / accepted) * 100) },
    held: heldDocs(rnd, heldTotal),
    heldTotal,
  }
}

/** The mapping a connector renders: the saved override when one exists, else its default. */
export function mappingFor(def: ConnectorDef, overrides: Partial<Record<ConnectorId, FieldMapRow[]>>): FieldMapRow[] {
  return overrides[def.id] ?? def.mapping
}
