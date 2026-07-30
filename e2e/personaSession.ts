// e2e/personaSession.ts — the Playwright-driving layer for the persona axis (PERSONA-01-01,
// Backlog task-270). Split out of e2e/personas.ts because personas.ts must stay importable
// from e2e/personas.test.ts, which runs under vitest in `node` and would break if the pure
// registry pulled in @playwright/test.
//
// STUB (Stage 2.5 / Mode A RED, task-270, Phase B): every export below is correctly typed
// but throws. The executor (Stage 3) fills in the real sign-in / refusal / roster / error-
// collection logic per the implementation plan in Backlog task-270.

import type { Page } from '@playwright/test'

import type { Destination, PersonaId } from './personas'

export async function signInAs(_page: Page, _id: PersonaId): Promise<void> {
  throw new Error('not implemented')
}

export async function expectRefused(_page: Page, _id: PersonaId, _destination: Destination): Promise<void> {
  throw new Error('not implemented')
}

export async function sidebarRoster(_page: Page): Promise<string[]> {
  throw new Error('not implemented')
}

export function collectErrors(_page: Page): string[] {
  throw new Error('not implemented')
}
