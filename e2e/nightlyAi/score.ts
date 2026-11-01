// Scoring of the extractions against the key (internal/extraction/testdata/nightly_key.json).
export type KeyRow = { file: string; kind: string; fields: Record<string, string[]> }
export type Values = Record<string, string | null>
export type Miss = { field: string; want: string[]; got: string | null }
export type DocScore = { hits: number; cells: number; misses: Miss[] }

// A listed value hits on a trimmed exact match. An empty list hits when nothing was read. A failed document (null values) misses its non-empty cells.
export function scoreDocument(row: KeyRow, values: Values | null): DocScore {
  const misses: Miss[] = []
  const names = Object.keys(row.fields)
  for (const field of names) {
    const want = row.fields[field]
    const got = values === null ? null : (values[field] ?? null)
    const seen = got === null ? '' : got.trim()
    const hit = want.length === 0 ? seen === '' : want.includes(seen)
    if (!hit) misses.push({ field, want, got })
  }
  return { hits: names.length - misses.length, cells: names.length, misses }
}

export function accuracy(results: DocScore[]): number {
  const cells = results.reduce((n, r) => n + r.cells, 0)
  if (cells === 0) return 0
  return results.reduce((n, r) => n + r.hits, 0) / cells
}
