// Top-validation-failures list, ported from the prototype (Platform.dc.html ~L1101-1116).
// Consumed by buildClients -> dash.failures -> ReportsView (the last mock-dashboard surface).

import type { FailureRow, Invoice, ValidationResult } from '../types'

const FAILURE_MAP: Record<string, [string, string]> = {
  tin: ['Buyer TIN missing or malformed', 'MBS-TIN-01'],
  addr: ['Billing address incomplete', 'MBS-ADR-02'],
}

export function failuresFrom(inv: Invoice[], errs: ValidationResult[]): FailureRow[] {
  const tally: Record<string, number> = {}
  inv.forEach((_it, k) => errs[k].errors.forEach((e) => (tally[e.id] = (tally[e.id] || 0) + 1)))
  const list: FailureRow[] = Object.keys(tally).map((id) => ({
    label: FAILURE_MAP[id][0],
    rule: FAILURE_MAP[id][1],
    glyphId: 'cross',
    count: tally[id],
    bar: '0%',
  }))
  list.sort((a, b) => b.count - a.count)
  const fmax = Math.max(1, ...list.map((f) => f.count))
  list.forEach((f) => (f.bar = Math.round((f.count / fmax) * 100) + '%'))
  return list
}
