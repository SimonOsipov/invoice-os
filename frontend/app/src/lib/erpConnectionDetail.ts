// The ERP connector detail: the wire and the pure logic. Server truth; no UI state.

import type { AuthedFetch } from './portfolio'
import type { ErpConnectionWire } from './erpConnections'

export type ActivityCounts = {
  pulled: number
  pending: number
  drafted: number
  linked: number
  held: number
  failed: number
  deleted: number
}

export type SourceDocWire = {
  id: string
  source_doc_id: string
  source_revision: string
  state: string
  failure: string | null
  invoice_id: string | null
  invoice_number: string | null
  differs_from_erp: boolean
  changed_after_clearance_at: string | null
  deleted_in_erp_at: string | null
  updated_at: string
}

export type HeldDocWire = SourceDocWire & { reasons: string[] }

export type WritebackWire = {
  id: string
  invoice_id: string
  invoice_number: string | null
  outcome: string
  state: string
  reason: string | null
  attempt: number
  created_at: string
}

export type ErpOverview = {
  connection: ErpConnectionWire
  activity: { since: string; counts: ActivityCounts; documents: SourceDocWire[] }
  writebacks: WritebackWire[]
  held: HeldDocWire[]
}

export type DocumentMark = {
  connection_id: string
  system: string
  source_doc_id: string
  state: string
  differs_from_erp: boolean
  changed_after_clearance_at: string | null
  deleted_in_erp_at: string | null
}

export type FieldView = {
  target: string
  label: string
  line: boolean
  source_path: string | null
  default_path: string | null
  origin: 'client' | 'default' | 'none' | string
}
export type MappingView = { system: string; fields: FieldView[] }
export type FieldChange = { target: string; source_path: string | null }

export type Lookup = {
  kind: string
  erp_key: string
  tax_category: string | null
  exemption_code: string | null
  hsn_code: string | null
  service_code: string | null
  tin: string | null
  state: string | null
  lga: string | null
}
export type LookupsView = { system: string; external_key: string; lookups: Lookup[] }

export const LOOKUP_VALUE_KEYS = [
  'tax_category',
  'exemption_code',
  'hsn_code',
  'service_code',
  'tin',
  'state',
  'lga',
] as const

// Copy
export const MANAGE = 'Manage'
export const BACK_TO_CONNECTORS = 'All connectors'
export const RECONNECT = 'Reconnect'
export const COMPANY_NOT_CHOSEN = 'ERP company not chosen yet'
export const ACTIVITY_HEAD = 'Sync activity'
export const ACTIVITY_NONE = 'Nothing pulled in the last 24 hours.'
export const ACTIVITY_EMPTY = 'No documents pulled yet.'
export const MAPPING_HEAD = 'Field mapping'
export const MAPPING_SUB = 'ERP → INVOICE FIELD'
export const MAPPING_CUSTOM = 'CUSTOM'
export const MAPPING_NOT_MAPPED = 'Not mapped'
export const MAPPING_EDIT = 'Edit'
export const MAPPING_ADMIN_ONLY = 'Only an admin can edit the mapping.'
export const LOOKUPS_HEAD = 'Lookups'
export const LOOKUPS_NEED_KEY = 'Lookups open once the connection is signed in.'
export const LOOKUPS_EMPTY = 'No lookups yet.'
export const MODAL_TITLE = 'Edit field mapping'
export const MODAL_FIELDS = 'Fields'
export const MODAL_LOOKUPS = 'Lookups'
export const USE_DEFAULT = 'Use default'
export const ADD_LOOKUP = 'Add lookup'
export const SAVE = 'Save'
export const SAVING = 'Saving…'
export const CANCEL = 'Cancel'
export const WRITEBACK_HEAD = 'Write-back to ERP'
export const WRITEBACK_EMPTY = 'Nothing written back yet.'
export const HELD_HEAD = 'Held documents'
export const HELD_SUB = 'Documents that need a look'
export const HELD_EMPTY = 'No held documents.'
export const OPEN_INVOICE = 'Open invoice'
export const LOADING_CONNECTION = 'Loading the connection…'
export const LOADING_MAPPING = 'Loading the mapping…'
export const MARK_CHANGED_AFTER_CLEARANCE = 'Changed in the ERP after NRS accepted it'

export const lookupKindLabel: Record<string, string> = { tax_code: 'Tax code', item: 'Item', customer: 'Customer' }
export const lookupValueLabel: Record<(typeof LOOKUP_VALUE_KEYS)[number], string> = {
  tax_category: 'Tax category',
  exemption_code: 'Exemption code',
  hsn_code: 'HSN code',
  service_code: 'Service code',
  tin: 'TIN',
  state: 'State',
  lga: 'LGA',
}
export const lookupKindText = (kind: string): string => lookupKindLabel[kind] ?? kind

export const notFoundTitle = (clientName: string): string => `This connection is not in ${clientName}.`
export const clientLine = (clientName: string, externalKey: string | null): string =>
  externalKey === null ? `${clientName} · ${COMPANY_NOT_CHOSEN}` : `${clientName} · ERP company ${externalKey}`

const OUTCOME: Record<string, string> = {
  drafted: 'NEW DRAFT',
  linked: 'LINKED',
  held: 'HELD',
  failed: 'FAILED',
  deleted: 'DELETED IN ERP',
  pending: 'IN PROGRESS',
}
const HELD_REASON: Record<string, string> = {
  differs_from_erp: 'Differs from the ERP',
  changed_after_clearance: MARK_CHANGED_AFTER_CLEARANCE,
  deleted_in_erp: 'Deleted or voided in the ERP',
  number_held: 'Its number is held by another ERP document',
}
const FAILURE: Record<string, string> = {
  unmappable: 'the mapping could not read it',
  refused: 'the invoice was refused',
  not_found: 'the ERP no longer has it',
  no_mapping: 'this ERP has no mapping',
  exhausted: 'it kept failing',
}
const WRITEBACK_REASON: Record<string, string> = {
  not_found: 'the ERP document was not found',
  unsupported: 'the ERP does not accept it',
  revision_conflict: 'the ERP document changed first',
  rate_limited: 'the ERP asked to slow down',
  vendor: 'the ERP answered with an error',
  disconnected: 'the connection was disconnected',
  nothing_writable: 'nothing this ERP can store',
  superseded: 'a newer result replaced it',
  deleted_in_erp: 'deleted in the ERP',
}
const WRITEBACK_RESULT: Record<string, string> = { accepted: 'ACCEPTED', rejected: 'REJECTED', failed: 'FAILED' }

// An unknown code reads as itself, so a server that learns a new one still shows something true.
export function activitySummary(c: ActivityCounts): string {
  if (c.pulled === 0) return ACTIVITY_NONE
  return `${c.pulled} pulled · ${c.drafted} new drafts · ${c.linked} linked · ${c.held} held · ${c.failed} failed · ${c.deleted} deleted in the ERP · ${c.pending} in progress`
}

export function documentOutcome(doc: Pick<SourceDocWire, 'state'>): string {
  return OUTCOME[doc.state] ?? doc.state
}

export function heldReasonText(reason: string, failure: string | null): string {
  if (reason !== 'failed') return HELD_REASON[reason] ?? reason
  return failure === null ? 'Failed' : `Failed: ${FAILURE[failure] ?? failure}`
}

export function writebackResultText(outcome: string): string {
  return WRITEBACK_RESULT[outcome] ?? outcome
}

export function writebackText(w: Pick<WritebackWire, 'state' | 'reason'>): string {
  const why = w.reason === null ? null : (WRITEBACK_REASON[w.reason] ?? w.reason)
  switch (w.state) {
    case 'succeeded':
      return 'Written back'
    case 'failed':
      return why === null ? 'Not written' : `Not written: ${why}`
    case 'skipped':
      return why === null ? 'Skipped' : `Skipped: ${why}`
    default:
      return w.state
  }
}

/**
 * The changed rows only. An edit of null is "Use default" (a client row only). A blank on a client or
 * defaulted row is null; a blank on an unmapped row is no change.
 */
export function mappingChanges(fields: FieldView[], edits: Record<string, string | null>): FieldChange[] {
  const out: FieldChange[] = []
  for (const f of fields) {
    if (!(f.target in edits)) continue
    const e = edits[f.target]
    const text = e === null ? '' : e.trim()
    if (e === null) {
      if (f.origin === 'client') out.push({ target: f.target, source_path: null })
    } else if (text === '') {
      if (f.origin !== 'none') out.push({ target: f.target, source_path: null })
    } else if (text !== (f.source_path ?? '')) {
      out.push({ target: f.target, source_path: text })
    }
  }
  return out
}

const sameLookup = (a: Lookup, b: Lookup) => LOOKUP_VALUE_KEYS.every((k) => (a[k] ?? null) === (b[k] ?? null))
const noValues = (l: Lookup) => LOOKUP_VALUE_KEYS.every((k) => (l[k] ?? null) === null)
const lookupId = (l: Lookup) => JSON.stringify([l.kind, l.erp_key])

/** Added and edited entries as they are; a removed one as its key with every value null; an unchanged one absent. */
export function lookupChanges(before: Lookup[], after: Lookup[]): Lookup[] {
  const was = new Map(before.map((l) => [lookupId(l), l]))
  const out: Lookup[] = []
  const kept = new Set<string>()
  for (const l of after) {
    const id = lookupId(l)
    kept.add(id)
    const old = was.get(id)
    if (old ? !sameLookup(old, l) : !noValues(l)) out.push(l)
  }
  for (const [id, old] of was) {
    if (kept.has(id)) continue
    out.push({ kind: old.kind, erp_key: old.erp_key, tax_category: null, exemption_code: null, hsn_code: null, service_code: null, tin: null, state: null, lga: null })
  }
  return out
}

export function changedAfterClearance(marks: DocumentMark[]): DocumentMark | null {
  return marks.find((m) => m.changed_after_clearance_at) ?? null
}

const entityPath = (base: string, entityId: string) =>
  `${base}/api/portfolio/v1/entities/${encodeURIComponent(entityId)}`
const lookupsPath = (base: string, entityId: string, system: string, externalKey: string) =>
  `${entityPath(base, entityId)}/erp-lookups/${encodeURIComponent(system)}?${new URLSearchParams({ external_key: externalKey })}`

export function getErpOverview(f: AuthedFetch, base: string, entityId: string, connectionId: string): Promise<ErpOverview> {
  return f<ErpOverview>(`${entityPath(base, entityId)}/erp-connections/${encodeURIComponent(connectionId)}/overview`)
}

export function getErpMapping(f: AuthedFetch, base: string, entityId: string, system: string): Promise<MappingView> {
  return f<MappingView>(`${entityPath(base, entityId)}/erp-mappings/${encodeURIComponent(system)}`)
}

export function saveErpMapping(
  f: AuthedFetch,
  base: string,
  entityId: string,
  system: string,
  fields: FieldChange[],
): Promise<MappingView> {
  return f<MappingView>(`${entityPath(base, entityId)}/erp-mappings/${encodeURIComponent(system)}`, {
    method: 'PUT',
    body: { fields },
  })
}

export function getErpLookups(
  f: AuthedFetch,
  base: string,
  entityId: string,
  system: string,
  externalKey: string,
): Promise<LookupsView> {
  return f<LookupsView>(lookupsPath(base, entityId, system, externalKey))
}

export function saveErpLookups(
  f: AuthedFetch,
  base: string,
  entityId: string,
  system: string,
  externalKey: string,
  lookups: Lookup[],
): Promise<LookupsView> {
  return f<LookupsView>(lookupsPath(base, entityId, system, externalKey), { method: 'PUT', body: { lookups } })
}

/** A 200 body without a marks array reads as no marks (D22): a loose test mock must not break the invoice view. */
export async function getInvoiceErpMarks(
  f: AuthedFetch,
  base: string,
  entityId: string,
  invoiceId: string,
): Promise<DocumentMark[]> {
  const r = await f<{ marks?: DocumentMark[] | null } | null>(
    `${entityPath(base, entityId)}/invoices/${encodeURIComponent(invoiceId)}/erp-marks`,
  )
  return Array.isArray(r?.marks) ? r.marks : []
}
