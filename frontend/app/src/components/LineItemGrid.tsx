// The line-item grid: every parsed row, its arithmetic flag, the role remap and the empty
// state. Stateless -- 08 owns the draft and passes it down, the way ExtractionFields holds
// none above ExtractionReview.
//
// STUB for task-813 Stage 2.5 (RED tests first): declares the contract so
// LineItemGrid.test.tsx can import it. The render body -- the table, the empty panel, the
// filter mount into ExtractionFields -- is Stage 3's, not this commit's.

import type { LineRole, LineRow } from '../lib/lineItems'

export function LineItemGrid(_props: {
  rows: LineRow[]
  wireRows: LineRow[]
  subtotal: string | null
  selected: string | null
  onSelectCell: (wireName: string) => void
  onEditCell: (at: number, role: LineRole, value: string) => void
  onAddRow: () => void
  onRemoveRow: (at: number) => void
  onRemapRoles: (from: LineRole, to: LineRole) => void
}) {
  return null
}
