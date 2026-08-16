// Stub for LAND-03-02 (Test-Spec Mode A). Real exported names/types; bodies land in
// Stage 3. Deliberately does not yet import isProductionHost from ./hubspot — that
// import is part of the implementation, and its absence is what keeps
// "the gate reuses hubspot.ts's predicate rather than a copy" red.
import type { ConsentRecord } from './consent'

export function measurementId(): string | null {
  throw new Error('not implemented')
}

export function shouldLoadTag(_hostname: string, _allowed: boolean, _id: string | null): boolean {
  throw new Error('not implemented')
}

export function tagSrc(_id: string): string {
  throw new Error('not implemented')
}

export function ensureTag(_hostname: string, _record: ConsentRecord | null): boolean {
  throw new Error('not implemented')
}

export function bootAnalytics(): boolean {
  throw new Error('not implemented')
}
