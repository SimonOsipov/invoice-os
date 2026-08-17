// APPR-14-07 (task-573) AC-8: proves [topology-never-publishes] (docs/e2e-convention.md)
// actually holds, rather than that no spec happens to call the banned verb today. Three
// rungs, none meaningful alone: (1) denies the raw client.ts writes by name, (2) allowlists
// ensureFirmPolicyActive as the sole contract-helpers.ts import a topology spec may take --
// closing the door on some future activating export slipping through under a name rung 1
// doesn't yet list, (3) proves that helper itself cannot be turned into a general-purpose
// publisher, which is what makes rung 2 an allowlist rather than a name-based loophole.
// Modelled on ../gitHistoryGuard.test.ts and ../api/no-db-access.test.ts's source-scan
// precedent.
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))
const CONTRACT_HELPERS = join(TOPOLOGY_DIR, '../api/contract-helpers.ts')

function listSpecs(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...listSpecs(full))
    } else if (entry.isFile() && entry.name.endsWith('.spec.ts')) {
      out.push(full)
    }
  }
  return out
}

// Splits a parameter/argument list on commas at nesting depth 0 only -- a default value
// shaped like an object or array literal carries commas of its own.
function splitTopLevel(list: string): string[] {
  const out: string[] = []
  let depth = 0
  let current = ''
  for (const ch of list) {
    if ('{[('.includes(ch)) depth++
    if ('}])'.includes(ch)) depth--
    if (ch === ',' && depth === 0) {
      out.push(current)
      current = ''
    } else {
      current += ch
    }
  }
  if (current.trim()) out.push(current)
  return out
}

// Every `import { ... } from '<module>'` statement (single- or multi-line), with its
// named-import list split apart and `X as Y` / `type X` reduced to the bound name.
function namedImportsFrom(source: string, moduleSuffix: string): string[] {
  const names: string[] = []
  const re = /import\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"]/g
  let m: RegExpExecArray | null
  while ((m = re.exec(source))) {
    if (!m[2].endsWith(moduleSuffix)) continue
    for (const raw of m[1].split(',')) {
      const name = raw
        .trim()
        .replace(/^type\s+/, '')
        .split(/\s+as\s+/)[0]
        .trim()
      if (name) names.push(name)
    }
  }
  return names
}

describe('[topology-never-publishes] guard', () => {
  const files = listSpecs(TOPOLOGY_DIR)

  it('has topology specs to scan', () => {
    // Guards against a future reshuffle emptying the directory and every check below
    // passing vacuously.
    expect(files.length).toBeGreaterThan(0)
  })

  // Rung 1: the raw writes stay banned, by name. deleteApprovalPolicy and
  // listApprovalPolicies are read/cleanup, not activation -- roles.spec.ts and
  // workflows.spec.ts already import them.
  const BANNED_CLIENT_IMPORTS = ['publishApprovalPolicy', 'createApprovalPolicy', 'putApprovalPolicyDraft']
  it('imports none of publishApprovalPolicy/createApprovalPolicy/putApprovalPolicyDraft from ../api/client', () => {
    const offenders: string[] = []
    for (const f of files) {
      const imported = namedImportsFrom(readFileSync(f, 'utf8'), '/api/client')
      if (imported.some((n) => BANNED_CLIENT_IMPORTS.includes(n))) offenders.push(f)
    }
    expect(offenders, `banned client.ts import found in: ${offenders.join(', ')}`).toEqual([])
  })

  // Rung 2: ensureFirmPolicyActive is the ONLY symbol a topology spec may take from
  // contract-helpers.ts.
  it('imports only ensureFirmPolicyActive from ../api/contract-helpers', () => {
    const offenders: { file: string; extra: string[] }[] = []
    for (const f of files) {
      const imported = namedImportsFrom(readFileSync(f, 'utf8'), '/api/contract-helpers')
      const extra = imported.filter((n) => n !== 'ensureFirmPolicyActive')
      if (extra.length > 0) offenders.push({ file: f, extra })
    }
    expect(offenders, `unexpected contract-helpers import(s): ${JSON.stringify(offenders)}`).toEqual([])
  })

  // Rung 3: what makes rung 2 safe rather than a name-based loophole -- the helper itself
  // cannot be pointed at an arbitrary policy, by any caller, topology or otherwise.
  it('ensureFirmPolicyActive takes no policy-selecting parameter and its policy name stays module-private', () => {
    const source = readFileSync(CONTRACT_HELPERS, 'utf8')
    const sig = /export async function ensureFirmPolicyActive\(([^)]*)\)/s.exec(source)
    expect(sig, 'ensureFirmPolicyActive signature not found').toBeTruthy()
    // Top-level commas only: the `transport` param's default object literal has commas of
    // its own (list/putDraft/publish), which a naive split would misread as more params.
    const params = splitTopLevel(sig![1])
      .map((p) => p.trim().split(/[:=]/)[0].trim())
      .filter(Boolean)
    expect(params, 'ensureFirmPolicyActive must take only (token, transport?)').toEqual(['token', 'transport'])

    expect(source, 'the seeded policy name must be a module-private const').toMatch(/^const FIRM_POLICY_NAME\s*=/m)
    expect(source, 'the policy name must not be exported').not.toMatch(/^export const FIRM_POLICY_NAME/m)
  })
})
