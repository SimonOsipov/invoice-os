// e2e/personas.ts — the persona axis registry (PERSONA-01-01, Backlog task-270): persona
// records, the sidebar surface catalogue, the coverage map, and the pure sign-in/refusal
// functions.
//
// Deliberately imports NOTHING from @playwright/test: e2e/personas.test.ts runs this module
// under vitest in `node` (e2e/vitest.config.ts), and CI's `test:unit` job sets no deploy URLs
// (.github/workflows/ci.yml:471-483) — topology/targets.ts and smoke/apps.ts both resolve
// their targets at MODULE SCOPE and throw on import when unset, so this file must not
// transitively import either of them. ../targets.ts is safe: it exports resolveTarget and
// resolves nothing itself. signInUrl calls it lazily, inside the function body, for the same
// reason. See e2e/personaSession.ts for the Playwright-driving layer this registry
// deliberately does not contain.
//
// This is the SINGLE source of truth for the persona axis: smoke/apps.ts and
// topology/targets.ts derive their persona values from here. The dependency runs one way —
// those two import this module, never the reverse.

import { resolveTarget } from './targets'

// The four landing personas (frontend/landing/src/auth.ts:17). These ids are WIRE VALUES:
// each one is the `?persona=` param the landing hands off with, and the destination SPA's
// session gate checks it verbatim. Not to be conflated with frontend/app/src/auth.ts:14's
// unrelated, two-member `PersonaId` (firm | inhouse) — different package, different job.
export type PersonaId = 'developer' | 'support' | 'firm' | 'inhouse'

// The three deployed SPAs a persona can be routed to. Mirrors LandingPersona.target
// (frontend/landing/src/auth.ts:29) — `ops` is the ops-console service, and the `developer`
// persona opening it is the wire-value/display-name split documented there, not a mistake.
export type Destination = 'app' | 'ops' | 'support'

// Which environment variable carries each destination's base URL on this run's ephemeral
// Railway environment (M4-23). Resolved lazily — see the header note.
export const DESTINATION_ENV: Record<Destination, string> = {
  app: 'APP_URL',
  ops: 'OPS_CONSOLE_URL',
  support: 'SUPPORT_CONSOLE_URL',
}

export interface SurfaceDef {
  navConst: string
  label: string
}

// The COMPLETE catalogue of app-SPA nav surfaces: every constant reachable from
// Sidebar.tsx's navGroups (:115-127, 9 firm-mode items + 8 in-house), paired with the label
// glyphs.tsx renders for it (:63-110). Complete from this subtask onward, which is what lets
// personas.test.ts's G3 assert equality against the live Sidebar rather than containment.
//
// Note NAV_DASHBOARD's label is 'Overview', not 'Dashboard' — the constant kept the old name.
export const SURFACES: readonly SurfaceDef[] = [
  { navConst: 'NAV_DASHBOARD', label: 'Overview' },
  { navConst: 'NAV_INVOICES', label: 'Invoices' },
  { navConst: 'NAV_VALIDATION', label: 'Validation' },
  { navConst: 'NAV_WORKFLOWS', label: 'Workflows' },
  { navConst: 'NAV_RULES', label: 'Rules' },
  { navConst: 'NAV_APPROVALS', label: 'Approvals' },
  { navConst: 'NAV_CUSTOMERS', label: 'Customers' },
  { navConst: 'NAV_REPORTS', label: 'Reports' },
  { navConst: 'NAV_CLIENTS', label: 'Clients' },
  { navConst: 'NAV_SETTINGS', label: 'Settings' },
] as const

// Exactly two members. No `pending`/`planned`/`todo` third state ([no-pending-grade]): a
// cell either exists and names a real spec, or it does not exist. A "planned" grade would
// let a surface be recorded as covered by a file nobody has written, which is the exact
// bookkeeping this registry exists to make impossible.
export type Grade = 'drives' | 'nav-only'

export interface Cell {
  navConst: string
  grade: Grade
  coveredBy: string // repo-relative path
}

export interface PersonaDef {
  id: PersonaId
  destination: Destination
  displayName: string // LANDING_PERSONAS[].name
  // The token a covering spec must literally contain to prove it drives THIS persona.
  // Deliberately not the bare persona id: `firm` matches spec PROSE (portfolio.spec.ts's
  // only lowercase `firm` is inside a comment), so an id match would both pass on a
  // reformatted comment and let a spec that signs in as the wrong persona still qualify.
  specToken: string
  tenantName?: string // app personas only — the two console personas have no tenant
  coverage?: Cell[] // app personas only
}

export const PERSONAS: Record<PersonaId, PersonaDef> = {
  developer: {
    id: 'developer',
    destination: 'ops',
    displayName: 'Amara Okafor',
    specToken: '?persona=developer',
  },
  support: {
    id: 'support',
    destination: 'support',
    displayName: 'Emeka Iroha',
    specToken: '?persona=support',
  },
  firm: {
    id: 'firm',
    destination: 'app',
    displayName: 'Chinedu Okafor',
    specToken: 'FIRM_PERSONA',
    tenantName: 'Okafor & Partners',
    // Every surface here is DRIVEN as the firm persona except NAV_RULES, which is only
    // proven to EXIST for it (see its own note below). Approvals stays absent at every
    // grade -- it is not a firm-mode surface at all. Each cell is added in the SAME commit
    // as the spec that covers it, never ahead of one.
    coverage: [
      { navConst: 'NAV_DASHBOARD', grade: 'drives', coveredBy: 'e2e/topology/invoice-surfaces.spec.ts' },
      { navConst: 'NAV_INVOICES', grade: 'drives', coveredBy: 'e2e/topology/invoice-surfaces.spec.ts' },
      { navConst: 'NAV_VALIDATION', grade: 'drives', coveredBy: 'e2e/topology/validation.spec.ts' },
      { navConst: 'NAV_CLIENTS', grade: 'drives', coveredBy: 'e2e/topology/portfolio.spec.ts' },
      // PERSONA-01-04: the firm policy LIST plus the per-workspace proof (delete a policy,
      // switch client, assert the MUTATED list survived). That file never signs in as
      // in-house, which is why the in-house NAV_WORKFLOWS cell points elsewhere.
      { navConst: 'NAV_WORKFLOWS', grade: 'drives', coveredBy: 'e2e/topology/workflows.spec.ts' },
      { navConst: 'NAV_CUSTOMERS', grade: 'drives', coveredBy: 'e2e/topology/invoice-surfaces.spec.ts' },
      // nav-only, and honestly so: persona-surfaces.spec.ts's roster test asserts the firm
      // sidebar's nav labels as an EXACT ordered list, so it pins that this surface is
      // present (and that Approvals is absent) for this persona -- but it never opens it as
      // the firm. Its firm-mode CONTENT is unproven, which is exactly what `nav-only` states.
      // Three surfaces have left this group as specs arrived to drive them: NAV_WORKFLOWS,
      // then NAV_CUSTOMERS, then NAV_SETTINGS.
      { navConst: 'NAV_RULES', grade: 'nav-only', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_REPORTS', grade: 'drives', coveredBy: 'e2e/topology/invoice-surfaces.spec.ts' },
      // roles.spec.ts signs in as the firm persona, opens Settings and asserts the Roles
      // tab's rendered content plus the Members tab's roster column -- what `drives` means.
      { navConst: 'NAV_SETTINGS', grade: 'drives', coveredBy: 'e2e/topology/roles.spec.ts' },
    ],
  },
  inhouse: {
    id: 'inhouse',
    destination: 'app',
    displayName: 'Ngozi Balogun',
    specToken: 'INHOUSE_PERSONA',
    tenantName: 'Honeywell Group',
    // ALL EIGHT of the in-house sidebar's surfaces, every one DRIVEN (rendered content
    // asserted, not a mount) by persona-surfaces.spec.ts -- seven by PERSONA-01-03's sweep,
    // and NAV_WORKFLOWS by the Workflows block inside that same sweep. That cell points HERE
    // and not at topology/workflows.spec.ts, which never signs in as in-house: filing it
    // there would claim coverage that file does not provide, which is the exact bookkeeping
    // this registry exists to prevent ([coverage-honesty]). Listed in sidebar-roster order.
    //
    // NAV_WORKFLOWS' cell was justified by the depth [PERSONA-01-04] added -- a count, status
    // pills and `scope · summary` lines. APPR-09-08 deleted all of it: it was transcribed from
    // a frontend fixture, and nothing seeds approval_policies. The grade stays `drives` on
    // what replaced it -- the h1, the tenant-driven subtitle, and a settle on either terminal
    // arm of the policies list. That last one is why this is still not a mount: the first two
    // render above the ladder and pass on a fetch that never landed, and the settle does not.
    coverage: [
      { navConst: 'NAV_DASHBOARD', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_INVOICES', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_VALIDATION', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_WORKFLOWS', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_RULES', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_APPROVALS', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_REPORTS', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
      { navConst: 'NAV_SETTINGS', grade: 'drives', coveredBy: 'e2e/topology/persona-surfaces.spec.ts' },
    ],
  },
}

// LANDING_PERSONAS' own order (frontend/landing/src/auth.ts:34-87).
export const PERSONA_IDS: readonly PersonaId[] = ['developer', 'support', 'firm', 'inhouse']

export const DESTINATIONS: readonly Destination[] = ['app', 'ops', 'support']

// The landing hand-off URL for a persona: `<its destination's base>?persona=<id>`, exactly
// what landing/src/auth.ts's destUrl() builds. resolveTarget is called HERE, not at module
// scope, so importing this module never requires a deployed environment (see the header).
// It throws naming the missing variable rather than defaulting ([fail-loud-targets]).
export function signInUrl(id: PersonaId): string {
  const base = resolveTarget(DESTINATION_ENV[PERSONAS[id].destination])
  return `${base}?persona=${id}`
}

// Whether a destination's session gate lets this persona in. Each persona opens exactly one
// destination, so this is a lookup against the registry rather than a second list of pairs.
// Mirrors the three live product gates:
//   app     -> shouldAutoSignIn        (frontend/app/src/lib/session.ts:110-111)
//   ops     -> OPS_OPERATORS           (frontend/ops-console/src/session.ts:31-33)
//   support -> SUPPORT_OPERATORS       (frontend/support-console/src/session.ts:26-28)
export function accepts(destination: Destination, id: PersonaId): boolean {
  return PERSONAS[id].destination === destination
}

// Every (persona, destination) pair and what the destination's gate does with it: 4 accepts,
// 8 refuses. HAND-WRITTEN on purpose. Derived from accepts() it would be 12-long and
// duplicate-free by construction, so G5 would assert a tautology; written out, adding a
// fifth persona leaves the matrix at 12 rows and turns G5 red until all three of its new
// pairs have been stated. G5 also cross-checks every row against accepts().
export const BOUNDARY_MATRIX: readonly { persona: PersonaId; destination: Destination; verdict: 'accepts' | 'refuses' }[] = [
  { persona: 'developer', destination: 'app', verdict: 'refuses' },
  { persona: 'developer', destination: 'ops', verdict: 'accepts' },
  { persona: 'developer', destination: 'support', verdict: 'refuses' },
  { persona: 'support', destination: 'app', verdict: 'refuses' },
  { persona: 'support', destination: 'ops', verdict: 'refuses' },
  { persona: 'support', destination: 'support', verdict: 'accepts' },
  { persona: 'firm', destination: 'app', verdict: 'accepts' },
  { persona: 'firm', destination: 'ops', verdict: 'refuses' },
  { persona: 'firm', destination: 'support', verdict: 'refuses' },
  { persona: 'inhouse', destination: 'app', verdict: 'accepts' },
  { persona: 'inhouse', destination: 'ops', verdict: 'refuses' },
  { persona: 'inhouse', destination: 'support', verdict: 'refuses' },
]
