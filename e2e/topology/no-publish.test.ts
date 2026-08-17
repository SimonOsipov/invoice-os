// APPR-14-07 (task-573) AC-8: proves [topology-never-publishes] (docs/e2e-convention.md)
// actually holds, rather than that no spec happens to call the banned verb today. Three
// rungs, none meaningful alone: (1) denies the raw client.ts writes by name, (2) allowlists
// ensureFirmPolicyActive as the sole contract-helpers.ts import a topology spec may take --
// closing the door on some future activating export slipping through under a name rung 1
// doesn't yet list, (3) proves that helper itself cannot be turned into a general-purpose
// publisher, which is what makes rung 2 an allowlist rather than a name-based loophole.
// Modelled on ../gitHistoryGuard.test.ts and ../api/no-db-access.test.ts's source-scan
// precedent.
//
// HARDENED after a QA pass found the original regex-based version had three confirmed
// bypasses and no control needle: (1) the signature regex stopped at the first `)`, so an
// inner arrow-function default in the parameter list hid a genuine third parameter; (2) the
// name-leak check only matched a literal `export const FIRM_POLICY_NAME` line, so exporting
// the same value under a different name slipped through; (3) the scanner only walked
// `*.spec.ts` and only matched `import {...} from` specifiers literally ending in
// `/api/client` or `/api/contract-helpers`, so one hop of re-export through any same-
// directory non-spec module defeated all three rungs at once. This version replaces every
// regex-over-source-text check with the TypeScript compiler API, walks every `.ts` file
// under e2e/topology (not just specs), and follows re-exports/namespace-imports/dynamic
// import() transitively by RESOLVED FILE PATH rather than by specifier string, so which
// directory the indirection lives in no longer matters. See the KNOWN LIMITATIONS block at
// the end of this file for what is still not, and cannot cheaply be, closed.
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))
const E2E_ROOT = dirname(TOPOLOGY_DIR)
const CLIENT_TS = join(E2E_ROOT, 'api/client.ts')
const CONTRACT_HELPERS = join(E2E_ROOT, 'api/contract-helpers.ts')

function listTopologyFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...listTopologyFiles(full))
    } else if (entry.isFile() && entry.name.endsWith('.ts')) {
      out.push(full)
    }
  }
  return out
}

function parseFile(filePath: string): ts.SourceFile {
  return ts.createSourceFile(filePath, readFileSync(filePath, 'utf8'), ts.ScriptTarget.Latest, true)
}

function isExported(node: ts.Node): boolean {
  return ts.canHaveModifiers(node) ? (ts.getModifiers(node)?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword) ?? false) : false
}

// Real parameter list of an exported function, read from the AST -- not a regex over
// source text. A regex's `[^)]*` stops at the FIRST `)`, so a default value that is itself
// a call or arrow function (which owns a close-paren of its own) truncates the match before
// the real parameter list ends. The parser has no such blind spot: it tracks nesting, not
// character position.
function exportedFunctionParams(filePath: string, functionName: string): string[] {
  const file = parseFile(filePath)
  const fn = file.statements.find(
    (s): s is ts.FunctionDeclaration => ts.isFunctionDeclaration(s) && s.name?.text === functionName,
  )
  return fn ? fn.parameters.map((p) => p.name.getText(file)) : []
}

// Every top-level VALUE binding a file exports -- functions, classes, consts, and named
// re-exports, by their externally visible name. Type-only exports are skipped: a type
// cannot carry a runtime string value out of a module. A wildcard or namespace re-export
// ("export * from" / "export * as ns from") can't be enumerated member-by-member, so it
// fails loudly instead of silently reporting an incomplete surface as complete.
function exportedValueNames(filePath: string): string[] {
  const file = parseFile(filePath)
  const names: string[] = []
  for (const stmt of file.statements) {
    if ((ts.isFunctionDeclaration(stmt) || ts.isClassDeclaration(stmt)) && isExported(stmt) && stmt.name) {
      names.push(stmt.name.text)
    } else if (ts.isVariableStatement(stmt) && isExported(stmt)) {
      for (const decl of stmt.declarationList.declarations) {
        if (ts.isIdentifier(decl.name)) names.push(decl.name.text)
      }
    } else if (ts.isExportDeclaration(stmt) && !stmt.isTypeOnly) {
      if (!stmt.exportClause) {
        throw new Error(`${filePath}: wildcard export ('export * from ...') -- its surface cannot be verified statically`)
      }
      if (ts.isNamedExports(stmt.exportClause)) {
        for (const el of stmt.exportClause.elements) if (!el.isTypeOnly) names.push(el.name.text)
      } else {
        throw new Error(`${filePath}: namespace re-export ('export * as ... from ...') -- its surface cannot be verified statically`)
      }
    }
  }
  return names
}

// Every relative module reference a file makes -- named import, named re-export
// ("export {...} from"), namespace import, wildcard/namespace re-export, and dynamic
// import() -- as {spec, names}. `names` holds the ORIGINAL bound name(s) from the source
// module (before any `as` alias, since the alias is cosmetic and irrelevant to what left
// the module), or the sentinel '*' for a form that grants access to the whole module
// through property reads a text scan can't enumerate member-by-member.
type ModuleRef = { spec: string; names: string[] }

function moduleReferences(file: ts.SourceFile): ModuleRef[] {
  const refs: ModuleRef[] = []

  for (const stmt of file.statements) {
    if (ts.isImportDeclaration(stmt) && ts.isStringLiteralLike(stmt.moduleSpecifier)) {
      const spec = stmt.moduleSpecifier.text
      const bindings = stmt.importClause?.namedBindings
      if (bindings && ts.isNamespaceImport(bindings)) {
        refs.push({ spec, names: ['*'] })
      } else if (bindings && ts.isNamedImports(bindings)) {
        refs.push({ spec, names: bindings.elements.filter((e) => !e.isTypeOnly).map((e) => (e.propertyName ?? e.name).text) })
      }
    } else if (ts.isExportDeclaration(stmt) && stmt.moduleSpecifier && ts.isStringLiteralLike(stmt.moduleSpecifier) && !stmt.isTypeOnly) {
      const spec = stmt.moduleSpecifier.text
      if (!stmt.exportClause) {
        refs.push({ spec, names: ['*'] }) // export * from '...'
      } else if (ts.isNamedExports(stmt.exportClause)) {
        refs.push({ spec, names: stmt.exportClause.elements.filter((e) => !e.isTypeOnly).map((e) => (e.propertyName ?? e.name).text) })
      } else {
        refs.push({ spec, names: ['*'] }) // export * as ns from '...'
      }
    }
  }

  // Dynamic import('...') is an expression, not a top-level statement -- walk the whole
  // tree for it. A resolved dynamic import grants the same whole-module access as a
  // namespace import, so it gets the same '*' sentinel.
  const visit = (node: ts.Node) => {
    if (ts.isCallExpression(node) && node.expression.kind === ts.SyntaxKind.ImportKeyword) {
      const arg = node.arguments[0]
      if (arg && ts.isStringLiteralLike(arg)) refs.push({ spec: arg.text, names: ['*'] })
    }
    ts.forEachChild(node, visit)
  }
  visit(file)

  return refs.filter((r) => r.spec.startsWith('.'))
}

function resolveRelative(fromFile: string, spec: string): string | undefined {
  const base = join(dirname(fromFile), spec)
  for (const candidate of [base, `${base}.ts`, join(base, 'index.ts')]) {
    if (existsSync(candidate)) return candidate
  }
  return undefined
}

// Does `startFile` reach a banned symbol from `targetPath` -- directly, or through any
// chain of relative imports/re-exports? Matched by RESOLVED FILE PATH, not by specifier
// string, so it doesn't matter whether the indirection sits in e2e/topology (a same-
// directory `./consoleGate`) or one hop further out in e2e/api (a sibling `./leaker`
// importing client.ts as `./client`, not `../api/client`) -- both resolve to the same file
// and are caught the same way. Cycle-safe via `seen`; bounded to e2e/ -- see KNOWN
// LIMITATIONS at the end of this file.
//
// `stopAt`: files the walk must not recurse INTO, because what they do with the target
// module is governed by a different, more specific check rather than by this one. The only
// current use is contract-helpers.ts itself against the client.ts walk -- it is the one
// audited file [topology-never-publishes] exempts, and rungs 2+3 police it directly; without
// this it would report itself as a "topology file reaching the banned verb", which is the
// same false alarm the doc's own scoping amendment exists to settle.
function reachesBannedSymbol(
  startFile: string,
  targetPath: string,
  isBanned: (name: string) => boolean,
  stopAt: ReadonlySet<string> = new Set(),
): string | undefined {
  const seen = new Set<string>()
  function walk(file: string): string | undefined {
    if (seen.has(file)) return undefined
    seen.add(file)
    for (const ref of moduleReferences(parseFile(file))) {
      const resolved = resolveRelative(file, ref.spec)
      if (!resolved) continue
      if (resolved === targetPath) {
        const bad = ref.names.find(isBanned)
        if (bad) return `${file} pulls "${bad}" from ${targetPath}`
      }
      if (stopAt.has(resolved)) continue
      if (resolved.startsWith(E2E_ROOT)) {
        const hit = walk(resolved)
        if (hit) return hit
      }
    }
    return undefined
  }
  return walk(startFile)
}

describe('[topology-never-publishes] guard', () => {
  const files = listTopologyFiles(TOPOLOGY_DIR)
  const fileNames = files.map((f) => basename(f))

  it('has topology files to scan', () => {
    // Guards against a future reshuffle emptying the directory and every check below
    // passing vacuously.
    expect(files.length).toBeGreaterThan(0)
  })

  // Control needle (workspaceCoverage.test.ts:9-12 / :76-80 precedent): proves the walk
  // actually visited a known SPEC and a known non-spec HELPER, not just that it found
  // *something*. Without the second name, a walk that silently reverted to *.spec.ts only
  // -- reopening the exact hole bypass 3 exploited -- would still report a non-empty,
  // plausible-looking file list.
  it('scanned its control needles: a known spec and a known non-spec helper', () => {
    expect(fileNames, 'invoice-surfaces.spec.ts not found -- the spec walk is broken').toContain('invoice-surfaces.spec.ts')
    expect(fileNames, 'consoleGate.ts not found -- the walk is scanning *.spec.ts only again').toContain('consoleGate.ts')
  })

  // Rung 1: the raw writes stay banned, by name, reached from ANY topology file, directly
  // or through a chain of relative imports/re-exports/namespace-imports/dynamic imports.
  // deleteApprovalPolicy and listApprovalPolicies are read/cleanup, not activation --
  // roles.spec.ts and workflows.spec.ts already import them.
  const BANNED_CLIENT_IMPORTS = ['publishApprovalPolicy', 'createApprovalPolicy', 'putApprovalPolicyDraft']
  it('no topology file reaches publishApprovalPolicy/createApprovalPolicy/putApprovalPolicyDraft from ../api/client', () => {
    const offenders: string[] = []
    for (const f of files) {
      const hit = reachesBannedSymbol(f, CLIENT_TS, (n) => n === '*' || BANNED_CLIENT_IMPORTS.includes(n), new Set([CONTRACT_HELPERS]))
      if (hit) offenders.push(hit)
    }
    expect(offenders, offenders.join('\n')).toEqual([])
  })

  // Rung 2: ensureFirmPolicyActive is the ONLY symbol any topology file may reach from
  // contract-helpers.ts, by the same direct-or-transitive definition as rung 1.
  it('no topology file reaches anything but ensureFirmPolicyActive from ../api/contract-helpers', () => {
    const offenders: string[] = []
    for (const f of files) {
      const hit = reachesBannedSymbol(f, CONTRACT_HELPERS, (n) => n !== 'ensureFirmPolicyActive')
      if (hit) offenders.push(hit)
    }
    expect(offenders, offenders.join('\n')).toEqual([])
  })

  // Rung 3: what makes rung 2 safe rather than a name-based loophole -- the helper itself
  // cannot be pointed at an arbitrary policy, by any caller, topology or otherwise. Two
  // independent assertions: its declared parameter list carries no policy-selecting
  // parameter, and the seeded policy's name cannot be read out of the module under any
  // exported name.
  it('ensureFirmPolicyActive takes no policy-selecting parameter', () => {
    const params = exportedFunctionParams(CONTRACT_HELPERS, 'ensureFirmPolicyActive')
    expect(params, 'ensureFirmPolicyActive must take only (token, transport?)').toEqual(['token', 'transport'])
  })

  it("contract-helpers.ts's exported surface is exactly the known set -- FIRM_POLICY_NAME stays unreachable under any export name", () => {
    const KNOWN_EXPORTS = ['assertErrorEnvelope', 'assertUnauthorizedEnvelope', 'mapApprovalSteps', 'ensureFirmPolicyActive']
    const actual = exportedValueNames(CONTRACT_HELPERS)
    expect(
      [...actual].sort(),
      'contract-helpers.ts exports something not on the known list -- if this is intentional, add it here explicitly; ' +
        'if not, it may be a new route to a policy-selecting or name-leaking symbol',
    ).toEqual([...KNOWN_EXPORTS].sort())
  })
})

// KNOWN LIMITATIONS -- read before trusting this guard as total. Each of these was
// considered during hardening and left open deliberately, not by oversight.
//
// 1. BEHAVIOR INSIDE AN ALLOWED EXPORT IS NOT INSPECTED. Rung 3's exported-surface check
//    proves contract-helpers.ts exposes no binding beyond the known four -- it does not
//    read what those four functions' BODIES do. An author willing to hide a leak inside
//    legitimate-looking code (for example, changing mapApprovalSteps to also write
//    FIRM_POLICY_NAME somewhere, or to smuggle it inside an otherwise-normal return value)
//    would not be caught here; only a human reading the diff would catch it. Verified: a
//    mutant along exactly these lines was written and run against this suite during
//    hardening, and it survived (all tests stayed green). No source-scanning check closes
//    this class -- it needs real data-flow/taint analysis, which is out of scope for a
//    guard test.
//
// 2. ARGUMENTS-OBJECT SMUGGLING PAST THE DECLARED SIGNATURE. The signature check reads
//    ensureFirmPolicyActive's DECLARED parameter list only. It is a `function`, not an
//    arrow function, so its body has access to the raw `arguments` object -- a changed
//    body could read a third value that never appears in the declared signature.
//    TypeScript blocks a normal call site from passing a third argument ("Expected 1-2
//    arguments") -- but a caller willing to bypass the type checker with an `as any` cast
//    could still smuggle one through at runtime. This guard does not inspect the function
//    body for `arguments` usage, nor does it scan the repo for a suspicious cast at a call
//    site.
//
// 3. THE REACHABILITY WALK IS BOUNDED TO e2e/. Rungs 1 and 2 follow every relative ('./',
//    '../') import, re-export, namespace import, and dynamic import(), to arbitrary depth,
//    resolved by file path -- but only inside e2e/. An indirection routed through a file
//    outside it (for example packages/api-client or frontend/app) and pulled back in via a
//    bare package specifier is not walked: bare specifiers are deliberately not resolved,
//    since doing so would mean walking the whole monorepo's dependency graph from a guard
//    test. Nothing today imports client.ts or contract-helpers.ts from outside e2e/, so
//    this is assessed as unlikely, not impossible.
//
// 4. ONLY STATIC STRING SPECIFIERS ARE SEEN. `import(somePathBuiltAtRuntime)`, or any other
//    form where the module path is not a literal string, cannot be resolved by this scan.
//    Nothing in this repo does that today.
//
// If any of these is ever exploited, the fix belongs in code review and/or a follow-up to
// this file -- not in silently trusting a guard that has already been shown, in writing, to
// have a ceiling.
