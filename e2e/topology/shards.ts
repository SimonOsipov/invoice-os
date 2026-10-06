// Topology shard map: which spec files run in which Playwright project, and on which tenants.
// Node >= 22.18 runs this file without flags (`node e2e/topology/shards.ts` prints the matrix),
// so keep it to erasable TypeScript with no imports.

export type Unit = {
  name: string
  specs: string[]
  /** serial-lane only: spec file -> why it cannot move to a shard of its own. */
  reasons?: Record<string, string>
  /** dedicated shards only: seeded by db/seed.e2e-shards.sql. */
  tenants?: { firm: string; inHouse: string }
}

export const UNITS: Unit[] = [
  {
    name: 'serial-lane',
    specs: [
      'app-shell.spec.ts',
      'audit.spec.ts',
      'audit-settings-surfaces.spec.ts',
      'auth.spec.ts',
      'design-system.spec.ts',
      'environment-posture.spec.ts',
      'isolation.spec.ts',
      'ops-console.spec.ts',
      'overview-register-approvals.spec.ts',
      'persona-surfaces.spec.ts',
      'portfolio.spec.ts',
      'portfolio-workflow-surfaces.spec.ts',
      'roles.spec.ts',
      'support-console.spec.ts',
      'workflows.spec.ts',
    ],
    reasons: {
      'app-shell.spec.ts':
        'Cost, not shared state ([fork-lane-by-cost]): signs in the firm and in-house e2e members; writes no tenant data.',
      'audit.spec.ts':
        'Reads both persona tenants\' audit trails, which need rows the seed and the earlier suites wrote ("the seed alone writes audit rows"; the pager needs more than one page).',
      'audit-settings-surfaces.spec.ts':
        'Reads the seeded firm and in-house rosters and roles of 1111 / 2222 and both audit trails; creates one entity and one invoice in 1111 and prepares one evidence bundle, which appends audit rows.',
      'auth.spec.ts':
        'Tests the real sign-in front door and stored-session rules, which the SPA binds to the seeded tenants 1111 / 2222 (the e2e members it signs in as).',
      'design-system.spec.ts':
        'Cost, not shared state ([fork-lane-by-cost]): reads no tenant data beyond the firm e2e member and two provisioned staff sign-ins and writes none.',
      'environment-posture.spec.ts':
        'Cost, not shared state ([fork-lane-by-cost]): reads and writes no tenant data. A shard costs ~1 min of runner setup for one 1.4 s test.',
      'isolation.spec.ts':
        'Asserts the exact seeded membership subsets (firm: 6 seeded members, plus at most its e2e member) and tenant identities of 1111 and 2222.',
      'ops-console.spec.ts':
        'Cost, not shared state ([fork-lane-by-cost]): signs a provisioned staff account in to the mock-backed Ops Console; reads and writes no tenant data.',
      'overview-register-approvals.spec.ts':
        'Cost, not shared state ([fork-lane-by-cost]): signs in the firm and in-house e2e members and reaches every state with page.route; writes no tenant data.',
      'persona-surfaces.spec.ts':
        "Builds the in-house approval queue and badge on the active policy `internal/demopolicy` seeds only on 1111 / 2222. Needs 2222's seeded `Honeywell Group` entity as its first client (the subtitle assertion).",
      'portfolio.spec.ts':
        'Cost, not shared state ([fork-lane-by-cost]): creates its own entities and scopes each assertion to their rows. A shard costs ~1 min of runner setup for ~37 s of tests.',
      'portfolio-workflow-surfaces.spec.ts':
        'Reads the `Standard approval policy` that `internal/demopolicy` seeds only on 1111 / 2222 (`planFor`), without saving it; creates its own entity and invoice in 1111.',
      'roles.spec.ts':
        'Asserts the exact seeded roles, staffing and rosters of both tenants (the seeded rows plus exactly one e2e member row), derived by hand from `db/seed.dev.sql`, and the demopolicy-sealed policies.',
      'support-console.spec.ts':
        'Cost, not shared state ([fork-lane-by-cost]): signs a provisioned staff account in to the mock-backed Support Console; reads and writes no tenant data.',
      'workflows.spec.ts':
        "Creates and deletes one firm policy in 1111 and asserts the list count as `baseline` / `baseline + 1`, where the baseline includes the demopolicy-seeded firm policy. The count holds only while no other spec writes 1111's policies at the same time, which the lane guarantees. It never publishes (`[topology-never-publishes]`).",
    },
  },
  {
    name: 'import-wizard',
    specs: ['import-wizard.spec.ts', 'import-review-surfaces.spec.ts'],
    tenants: {
      firm: '11111111-1111-1111-1111-00000000e2e1',
      inHouse: '22222222-2222-2222-2222-00000000e2e1',
    },
  },
  {
    name: 'invoice-surfaces',
    specs: ['invoice-surfaces.spec.ts'],
    tenants: {
      firm: '11111111-1111-1111-1111-00000000e2e2',
      inHouse: '22222222-2222-2222-2222-00000000e2e2',
    },
  },
]

/** shardOf returns the unit that owns a spec file (base name), or undefined. */
export function shardOf(specFile: string): Unit | undefined {
  return UNITS.find((u) => u.specs.includes(specFile))
}

/** partitionErrors compares the map with the spec files on disk; empty means a clean partition. */
export function partitionErrors(specFiles: string[]): string[] {
  const errors: string[] = []
  const owners = new Map<string, string[]>()
  for (const u of UNITS) {
    if (u.specs.length === 0) errors.push(`unit ${u.name} has no spec`)
    for (const s of u.specs) owners.set(s, [...(owners.get(s) ?? []), u.name])
  }
  for (const [s, units] of owners) {
    if (units.length > 1) errors.push(`${s} is in two units: ${units.join(', ')}`)
    if (!specFiles.includes(s)) errors.push(`${s} is named by unit ${units[0]} but does not exist`)
  }
  for (const f of specFiles) {
    if (!owners.has(f)) errors.push(`${f} is in no unit`)
  }
  return errors
}

// Script entry: the CI matrix is this line. import.meta.main needs Node >= 22.18.
if (import.meta.main) {
  console.log(JSON.stringify(UNITS.map((u) => u.name)))
}
