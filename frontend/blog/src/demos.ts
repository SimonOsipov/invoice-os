import type { FlowScene, ListScene, Scene } from '../../library/src/types'

export type BlogDemo =
  | ({ title: string } & Omit<ListScene, 'kind'> & { kind: 'list' })
  | ({ title: string } & Omit<FlowScene, 'kind' | 'nodes'> & { kind: 'flow'; nodes: string[] })

export const DEMOS: Record<'import' | 'approval' | 'clearance', BlogDemo> = {
  import: { title: 'Import from CSV or Excel', kind: 'list', win: 'Import · sahara-foods-june.csv', cols: ['Row', 'Customer', 'Amount', 'Status'], grid: '0.5fr 2fr 1.2fr 1.2fr', show0: 0, rows: [['1', 'Lagos Freight & Logistics Ltd', '₦ 4,820,000', 'new'], ['2', 'Honeywell Group', '₦ 12,450,000', 'new'], ['3', 'Nigerian Delta Supplies Co.', '₦ 960,500', 'new'], ['4', 'Adeyemi & Sons Trading', '₦ 2,115,000', 'new']], steps: [
    { cap: 'Upload the export from your accounting system', banner: 'sahara-foods-june.csv · 42 rows' },
    { cap: 'Columns are matched to invoice fields', banner: 'Mapped 9 of 9 columns', show: 2, focus: 0 },
    { cap: 'One invoice is created per row', show: 4, focus: 3, set: { 0: 'valid', 1: 'valid', 2: 'valid' } },
    { cap: 'Rows that cannot be read are flagged with a reason', focus: 3, set: { 3: 'error' } }] },
  approval: { title: 'One queue for approvals', kind: 'list', win: 'Approvals · Awaiting you', cols: ['Invoice', 'Customer', 'Amount', 'Status'], grid: '1.2fr 2fr 1.2fr 1.4fr', rows: [['INV-2026-0412', 'Lagos Freight & Logistics Ltd', '₦ 4,820,000', 'pending'], ['INV-2026-0413', 'Honeywell Group', '₦ 12,450,000', 'pending'], ['INV-2026-0416', 'Nigerian Delta Supplies Co.', '₦ 960,500', 'pending']], steps: [
    { cap: 'Invoices waiting for your decision are queued', focus: 0 },
    { cap: 'Open an invoice and review its checks', focus: 1 },
    { cap: 'Approve it, and it moves to the next step', focus: 1, set: { 1: 'approved' } },
    { cap: 'Or reject with a reason; the submitter is notified', focus: 2, set: { 2: 'rejected' } }] },
  clearance: { title: 'Submit for FIRS clearance', kind: 'flow', win: 'Submission · INV-2026-0412', nodes: ['Approved', 'Prepared', 'Submitted', 'Response', 'Cleared'], steps: [
    { cap: 'The approved invoice is prepared in the required format', at: 1, out: 'Invoice converted and signed' },
    { cap: 'It is submitted to FIRS', at: 2, out: 'Submitted · waiting for a response' },
    { cap: 'The response is picked up automatically', at: 3, out: 'Response received from FIRS' },
    { cap: 'The clearance outcome is recorded on the invoice', at: 4, out: 'Cleared · reference stored on the invoice' }] },
}

// The library flow node is [label, sub]; the demos carry labels only.
export function toScene(d: BlogDemo): Scene {
  const { title: _title, ...rest } = d
  return rest.kind === 'flow' ? { ...rest, nodes: rest.nodes.map((l): [string, string] => [l, '']) } : rest
}
