// Review · "Already imported" tab (BUG-08-03). Empty shell only -- correct props
// signature so ReviewBatch.tsx and this file's own RED specs can import/typecheck
// against it; the real body is Mode B's job. Renders nothing meaningful.
import type { AlreadyImportedRowAll } from '../lib/reviewBatch'

export function ReviewAlreadyImportedTab({
  rows: _rows,
  rowsTotal: _rowsTotal,
  batchIds: _batchIds,
  onOpenInvoice: _onOpenInvoice,
}: {
  rows: AlreadyImportedRowAll[]
  rowsTotal: number
  batchIds: string[]
  onOpenInvoice: (id: string) => void
}) {
  return null
}
