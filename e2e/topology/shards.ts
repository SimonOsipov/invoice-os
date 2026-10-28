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
      'audit.spec.ts',
      'audit-settings-surfaces.spec.ts',
      'isolation.spec.ts',
      'persona-surfaces.spec.ts',
      'portfolio-workflow-surfaces.spec.ts',
      'roles.spec.ts',
      'support-console.spec.ts',
      'workflows.spec.ts',
    ],
    reasons: {
      'audit.spec.ts':
        'Reads both persona tenants\' audit trails, which need rows the seed and the earlier suites wrote ("the seed alone writes audit rows"; the pager needs more than one page).',
      'audit-settings-surfaces.spec.ts':
        'Reads the seeded firm and in-house rosters and roles of 1111 / 2222 and both audit trails; creates one entity and one invoice in 1111 and prepares one evidence bundle, which appends audit rows.',
      'isolation.spec.ts':
        'Asserts the exact seeded membership subsets (firm: 7 seeded members, plus the e2e member of each role that exists) and tenant identities of 1111 and 2222.',
      'persona-surfaces.spec.ts':
        "Builds the in-house approval queue and badge on the active policy `internal/demopolicy` seeds only on 1111 / 2222. Needs 2222's seeded `Honeywell Group` entity as its first client (the subtitle assertion).",
      'portfolio-workflow-surfaces.spec.ts':
        'Reads the `Standard approval policy` that `internal/demopolicy` seeds only on 1111 / 2222 (`planFor`), without saving it; creates its own entity and invoice in 1111. Downloads one invoice register, which appends one audit row in 1111.',
      'roles.spec.ts':
        'Asserts the exact seeded roles, staffing and rosters of both tenants (the seeded rows plus the e2e member of each role that exists), derived by hand from `db/seed.dev.sql`, and the demopolicy-sealed policies. Its last test invites on 1111, whose invitations every deploy purges; no other spec in the lane reads them.',
      'support-console.spec.ts':
        'Reads the real rule list with a staff account and flips then restores one global rule (`no-duplicate-line-items`), so it must not share a lane with a spec that depends on that rule; none does. It also opens or reuses the one global rule draft and adds rules to it; no other topology spec reads the draft. The API suite publishes that draft, but runs in the `e2e` job that the topology jobs wait for. Reads every tenant and the system health through the staff routes. It re-drives one seeded tenant-2222 dead-letter job (`DEMO-2026-7004`, else `-7006`) and cancels it in the same test; no other topology spec reads those jobs. It also reads the audit log of every tenant, the AI calls and totals of every tenant, and downloads one bundle; it writes nothing more.',
      'workflows.spec.ts':
        "Creates and deletes one firm policy in 1111 and asserts the list count as `baseline` / `baseline + 1`, where the baseline includes the demopolicy-seeded firm policy. The count holds only while no other spec writes 1111's policies at the same time, which the lane guarantees. It never publishes (`[topology-never-publishes]`).",
    },
  },
  {
    name: 'auth',
    specs: ['auth.spec.ts'],
    tenants: {
      firm: '11111111-1111-1111-1111-00000000e2e5',
      inHouse: '22222222-2222-2222-2222-00000000e2e5',
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
    name: 'import-wizard-2',
    specs: ['import-wizard-2.spec.ts'],
    tenants: {
      firm: '11111111-1111-1111-1111-00000000e2e3',
      inHouse: '22222222-2222-2222-2222-00000000e2e3',
    },
  },
  {
    name: 'invoice-surfaces',
    specs: ['invoice-surfaces.spec.ts', 'rules.spec.ts'],
    tenants: {
      firm: '11111111-1111-1111-1111-00000000e2e2',
      inHouse: '22222222-2222-2222-2222-00000000e2e2',
    },
  },
  {
    name: 'ui-surfaces',
    specs: [
      'account-deletion.spec.ts',
      'app-consent.spec.ts',
      'app-shell.spec.ts',
      'blog-editor.spec.ts',
      'design-system.spec.ts',
      'environment-posture.spec.ts',
      'member-access-role.spec.ts',
      'ops-console.spec.ts',
      'overview-register-approvals.spec.ts',
      'portfolio.spec.ts',
      'staff-deletion.spec.ts',
      'workspace-deletion.spec.ts',
    ],
    tenants: {
      firm: '11111111-1111-1111-1111-00000000e2e4',
      inHouse: '22222222-2222-2222-2222-00000000e2e4',
    },
  },
  { name: 'library', specs: ['library.spec.ts'] },
  { name: 'blog', specs: ['blog.spec.ts', 'blog-subscribe.spec.ts'] },
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

/** shardTenantError: why a spec in unit `project` may not sign in on `tenantId`; undefined when it may. */
export function shardTenantError(project: string, tenantId: string | undefined): string | undefined {
  const t = UNITS.find((u) => u.name === project)?.tenants
  if (!t || tenantId === t.firm || tenantId === t.inHouse) return undefined
  return `${project} is a dedicated shard: sign in on ${t.firm} or ${t.inHouse}, not ${tenantId ?? 'the 1111 / 2222 default'}`
}

// Script entry: the CI matrix is this line. import.meta.main needs Node >= 22.18.
if (import.meta.main) {
  console.log(JSON.stringify(UNITS.map((u) => u.name)))
}
