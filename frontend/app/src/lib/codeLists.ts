import type { AuthedFetch } from './portfolio'

// Mirrors internal/invoice/codes.go's codeSearchLists (wireMirrors.test.ts).
export const CODE_LISTS = ['states', 'lgas', 'hs-codes', 'services-codes', 'invoice-quantity-codes', 'tax-categories'] as const
export type CodeList = (typeof CODE_LISTS)[number]

// The fields that pick from a registry list; country and currency stay text inputs.
export const CODE_LIST_OF = {
  hsn_code: 'hs-codes',
  isic_code: 'services-codes',
  price_unit: 'invoice-quantity-codes',
  supplier_state: 'states',
  buyer_state: 'states',
  supplier_lga: 'lgas',
  buyer_lga: 'lgas',
  tax_category: 'tax-categories',
} as const satisfies Record<string, CodeList>

export type CodeListField = keyof typeof CODE_LIST_OF

// CodeEntry mirrors internal/invoice/codes.go's CodeEntry; percent is set only for a numeric tax-categories percent.
export interface CodeEntry {
  code: string
  label: string
  percent: string | null
}

export async function searchCodes(
  authedFetch: AuthedFetch,
  base: string,
  list: CodeList,
  q: string,
  limit: number,
): Promise<CodeEntry[]> {
  const res = await authedFetch<{ codes: CodeEntry[] }>(
    `${base}/api/invoice/v1/code-lists/${list}?q=${encodeURIComponent(q)}&limit=${limit}`,
  )
  return res.codes
}

// A code is stored as published, so the match is exact: no case folding, no trimming.
export function commitCode(text: string, results: CodeEntry[]): CodeEntry | null {
  return results.find((e) => e.code === text) ?? null
}
