// e2e/personas.ts — the persona axis registry (PERSONA-01-01, Backlog task-270): persona
// records, the sidebar surface catalogue, the coverage map, and the pure sign-in/refusal
// functions.
//
// Deliberately imports NOTHING from @playwright/test: e2e/personas.test.ts runs this module
// under vitest in `node` (e2e/vitest.config.ts), and CI's `test:unit` job sets no deploy URLs
// (.github/workflows/ci.yml:471-483) — topology/targets.ts and smoke/apps.ts both resolve
// their targets at MODULE SCOPE and throw on import when unset, so this file must not
// transitively import either of them. signInUrl resolves its target lazily, inside the
// function body, for the same reason. See e2e/personaSession.ts for the Playwright-driving
// layer this registry deliberately does not contain.
//
// STUB (Stage 2.5 / Mode A RED, task-270): every array/record below is empty and
// signInUrl/accepts throw. This is Phase B of the RED sequence in the implementation plan —
// it exists so e2e/personas.test.ts's guards can be proven to fail on their OWN assertions
// against empty data, not merely on a missing import. The executor (Stage 3) fills in the
// real data and logic per that plan; nothing in this file's shape should need to change.

export type PersonaId = 'developer' | 'support' | 'firm' | 'inhouse'
export type Destination = 'app' | 'ops' | 'support'

export const DESTINATION_ENV: Record<Destination, string> = {
  app: 'APP_URL',
  ops: 'OPS_CONSOLE_URL',
  support: 'SUPPORT_CONSOLE_URL',
}

export interface SurfaceDef {
  navConst: string
  label: string
}

// STUB: complete from Stage 3 onward — all 10 nav surfaces found in Sidebar.tsx's navGroups.
export const SURFACES: readonly SurfaceDef[] = []

// Exactly two members. No `pending`/`planned`/`todo` third state ([no-pending-grade]).
export type Grade = 'drives' | 'nav-only'

export interface Cell {
  navConst: string
  grade: Grade
  coveredBy: string // repo-relative path
}

export interface PersonaDef {
  id: PersonaId
  destination: Destination
  displayName: string
  specToken: string
  tenantName?: string // app personas only
  coverage?: Cell[] // app personas only
}

// STUB: empty record. Real data (4 persona defs, the firm persona's 4-cell coverage map)
// lands in Stage 3.
export const PERSONAS: Record<PersonaId, PersonaDef> = {} as Record<PersonaId, PersonaDef>

// STUB: empty. Real order is LANDING_PERSONAS' order: developer, support, firm, inhouse.
export const PERSONA_IDS: readonly PersonaId[] = []

// STUB: empty. Real value is ['app', 'ops', 'support'].
export const DESTINATIONS: readonly Destination[] = []

export function signInUrl(_id: PersonaId): string {
  throw new Error('not implemented')
}

export function accepts(_destination: Destination, _id: PersonaId): boolean {
  throw new Error('not implemented')
}

// STUB: empty. Real value is a hand-written 12-row literal (4 accepts, 8 refuses) that
// e2e/personas.test.ts's G5 also cross-checks against accepts() above.
export const BOUNDARY_MATRIX: readonly { persona: PersonaId; destination: Destination; verdict: 'accepts' | 'refuses' }[] = []
