import type { View } from '../types'

// STUB (Stage 2.5/Mode A) -- routePath/parseRoute throw until Stage 3 implements them.
// ROUTE_PATHS starts empty rather than value-wrong, so the totality/distinctness specs
// go red honestly instead of passing on placeholder values.
export const ROUTE_PATHS = {} as Record<View, string>

export function routePath(_view: View): string {
  throw new Error('not implemented')
}

export function parseRoute(_pathname: string): View | null {
  throw new Error('not implemented')
}
