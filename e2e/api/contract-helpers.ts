import { expect } from '@playwright/test'

// The raw result shape rawFetch() returns and every contract assertion consumes.
export type RawResult = { status: number; body: unknown }

// assertErrorEnvelope(): the shared error-path assertion — a rejected request must
// carry the EXPECTED status and a body that is EXACTLY the shared envelope shape
// {error: <string>}: a plain object with one key, `error`, whose value is a string.
// Extracted VERBATIM from the five contract specs (M4-16-01) — behaviour + messages unchanged.
export function assertErrorEnvelope(result: RawResult, expectedStatus: number, label: string): void {
  expect(result.status, `${label}: expected HTTP ${expectedStatus}`).toBe(expectedStatus)
  expect(result.body, `${label}: expected a parsed JSON object body`).toBeInstanceOf(Object)
  const body = result.body as Record<string, unknown>
  expect(Object.keys(body), `${label}: expected exactly one key, 'error'`).toEqual(['error'])
  expect(typeof body.error, `${label}: expected body.error to be a string`).toBe('string')
}

// assertUnauthorizedEnvelope(): the 401 specialization auth-contract.spec.ts used
// (its message was "expected HTTP 401" — identical to delegating with expectedStatus=401).
export function assertUnauthorizedEnvelope(result: RawResult, label: string): void {
  assertErrorEnvelope(result, 401, label)
}
