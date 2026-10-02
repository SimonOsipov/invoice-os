// Shared source-scan helpers for the landing's CSS and build-input tests (D-41).
// The `.test.` in the name keeps this file out of every build-input scan.
/// <reference types="node" />
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { stripComments } from '@invoice-os/api-client/strip-comments'

export const LANDING_SRC = dirname(fileURLToPath(import.meta.url))
export const REPO_ROOT = join(LANDING_SRC, '..', '..', '..')
export const V2_DIR = join(REPO_ROOT, 'packages', 'design-tokens', 'v2')

export type CssRule = { selector: string; body: string; at: string[] }

/** Flat scanner: comments and strings skipped, `@`-preludes pushed on an at-rule stack, a statement `;` ends a prelude. */
export function parseRules(css: string): CssRule[] {
  const rules: CssRule[] = []
  const at: string[] = []
  let prelude = ''
  let i = 0
  while (i < css.length) {
    const ch = css[i]
    if (ch === '/' && css[i + 1] === '*') {
      const end = css.indexOf('*/', i + 2)
      i = end === -1 ? css.length : end + 2
      continue
    }
    if (ch === '"' || ch === "'") {
      const end = css.indexOf(ch, i + 1)
      const stop = end === -1 ? css.length : end + 1
      prelude += css.slice(i, stop)
      i = stop
      continue
    }
    if (ch === ';') {
      prelude = ''
      i += 1
      continue
    }
    if (ch === '{') {
      const head = prelude.trim()
      prelude = ''
      if (head.startsWith('@')) {
        at.push(head.replace(/\s+/g, ' '))
        i += 1
        continue
      }
      const close = css.indexOf('}', i)
      const end = close === -1 ? css.length : close
      rules.push({ selector: head, body: css.slice(i + 1, end), at: [...at] })
      i = end + 1
      continue
    }
    if (ch === '}') {
      at.pop()
      prelude = ''
      i += 1
      continue
    }
    prelude += ch
    i += 1
  }
  return rules
}

export function selectorParts(rule: CssRule): string[] {
  return rule.selector
    .split(',')
    .map((s) => s.trim().replace(/\s+/g, ' '))
    .filter(Boolean)
}

export function declarations(body: string): { prop: string; value: string }[] {
  return body
    .split(';')
    .map((d) => d.trim())
    .filter(Boolean)
    .flatMap((d) => {
      const idx = d.indexOf(':')
      return idx > 0 ? [{ prop: d.slice(0, idx).trim().toLowerCase(), value: d.slice(idx + 1).trim() }] : []
    })
}

/** Block comments only for CSS (it has no `//`), `<!-- -->` for HTML, stripComments for TS/TSX. */
export function stripSource(file: string, src: string): string {
  if (file.endsWith('.html')) return src.replace(/<!--[\s\S]*?-->/g, '')
  if (file.endsWith('.css')) return src.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ''))
  return stripComments(src)
}

/** Custom properties declared in `src`: `--x:` in CSS and `'--x':` in a style object. */
export function customPropNames(src: string): string[] {
  return [...src.matchAll(/(--\w[\w-]*)['"]?\s*:/g)].map((m) => m[1])
}

/** `--x: value` pairs, for resolving alias chains. */
export function customPropValues(css: string): Map<string, string> {
  const out = new Map<string, string>()
  for (const m of css.matchAll(/(--\w[\w-]*)\s*:\s*([^;}]+)/g)) out.set(m[1], m[2].trim())
  return out
}

/** Names referenced through `var(--x…)`. */
export function varRefs(src: string): string[] {
  return [...src.matchAll(/var\(\s*(--\w[\w-]*)/gi)].map((m) => m[1])
}

function skipBraces(s: string, i: number): number {
  let depth = 0
  for (let j = i; j < s.length; j++) {
    const c = s[j]
    if (c === '"' || c === "'" || c === '`') {
      j = skipLiteral(s, j) - 1
    } else if (c === '{') {
      depth++
    } else if (c === '}' && --depth === 0) {
      return j + 1
    }
  }
  return s.length
}

function skipLiteral(s: string, i: number): number {
  const q = s[i]
  let j = i + 1
  while (j < s.length) {
    const c = s[j]
    if (c === '\\') j += 2
    else if (q === '`' && c === '$' && s[j + 1] === '{') j = skipBraces(s, j + 1)
    else if (c === q) return j + 1
    else if (c === '\n' && q !== '`') return j
    else j++
  }
  return s.length
}

/** The text of every quoted or template literal in comment-stripped source. */
export function stringLiterals(src: string): string[] {
  const out: string[] = []
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (c !== '"' && c !== "'" && c !== '`') continue
    const end = skipLiteral(src, i)
    out.push(src.slice(i + 1, src[end - 1] === c ? end - 1 : end))
    i = end - 1
  }
  return out
}

const DYNAMIC = '\u0000'

const COMPARED = /[!=]==?\s*(['"`])(?:(?!\1).)*\1|(['"`])(?:(?!\2).)*\2\s*[!=]==?/g

/** Static class tokens of every `className=` value: "…", '…', {'…'}, {`…${x}…`}, {a ? 'b' : 'c'}. A token touching `${…}` is dynamic and dropped; a literal compared with `==`, `!=`, `===` or `!==` is not a class. */
export function classNameTokens(src: string): string[] {
  const out: string[] = []
  for (const m of src.matchAll(/\bclassName\s*=\s*/g)) {
    const start = m.index + m[0].length
    const first = src[start]
    let expr: string
    if (first === '"' || first === "'") expr = src.slice(start, skipLiteral(src, start))
    else if (first === '{') expr = src.slice(start, skipBraces(src, start))
    else continue
    expr = expr.replace(COMPARED, '')
    for (let lit of stringLiterals(expr)) {
      for (let at = lit.indexOf('${'); at !== -1; at = lit.indexOf('${')) {
        lit = lit.slice(0, at) + DYNAMIC + lit.slice(skipBraces(lit, at + 1))
      }
      for (const tok of lit.split(/\s+/)) if (tok && !tok.includes(DYNAMIC)) out.push(tok)
    }
  }
  return out
}

/** Class names selected by the rules of a CSS text. */
export function classSelectors(css: string): string[] {
  return parseRules(css).flatMap((r) =>
    selectorParts(r).flatMap((p) =>
      [...p.replace(/\[[^\]]*\]/g, '').matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)].map((m) => m[1]),
    ),
  )
}

/** Class names defined by the CSS inside a source file's string literals (`<style>` strings, CSS constants). */
export function classSelectorsInStrings(src: string): string[] {
  return stringLiterals(src)
    .filter((lit) => lit.includes('{') && lit.includes(':'))
    .flatMap((lit) => classSelectors(lit))
}

const TEXT_EXT = /\.(?:tsx?|css)$/

function walk(dir: string): string[] {
  return readdirSync(dir, { recursive: true, encoding: 'utf8' })
    .filter((f) => !/\.test\./.test(f) && TEXT_EXT.test(f) && statSync(join(dir, f)).isFile())
    .map((f) => f.split('\\').join('/'))
}

/**
 * Build inputs (D-14): non-test .ts/.tsx/.css under src (keys relative to src) plus `../index.html`.
 * `withMonitoring` adds the non-test .tsx of packages/monitoring/src when the landing depends on it (D-38 rule 1).
 */
export function landingBuildInput(opts: { withMonitoring?: boolean } = {}): Record<string, string> {
  const files: Record<string, string> = {}
  for (const f of walk(LANDING_SRC)) files[f] = readFileSync(join(LANDING_SRC, f), 'utf8')
  files['../index.html'] = readFileSync(join(LANDING_SRC, '..', 'index.html'), 'utf8')
  if (opts.withMonitoring) {
    const pkg = JSON.parse(readFileSync(join(LANDING_SRC, '..', 'package.json'), 'utf8')) as {
      dependencies?: Record<string, string>
      devDependencies?: Record<string, string>
    }
    const deps = { ...pkg.dependencies, ...pkg.devDependencies }
    const dir = join(REPO_ROOT, 'packages', 'monitoring', 'src')
    if ('@invoice-os/monitoring' in deps && existsSync(dir)) {
      for (const f of walk(dir).filter((x) => x.endsWith('.tsx'))) {
        files[`packages/monitoring/src/${f}`] = readFileSync(join(dir, f), 'utf8')
      }
    }
  }
  return files
}

/** One entry `file: /needle/flags` per match, after comment stripping. */
export function scanBuildInput(files: Readonly<Record<string, string>>, needles: readonly RegExp[]): string[] {
  const hits: string[] = []
  for (const [file, raw] of Object.entries(files)) {
    const src = stripSource(file, raw)
    for (const n of needles) {
      const re = new RegExp(n.source, n.flags.includes('g') ? n.flags : `${n.flags}g`)
      const count = [...src.matchAll(re)].length
      for (let k = 0; k < count; k++) hits.push(`${file}: ${String(n)}`)
    }
  }
  return hits
}

/** Every v2 stylesheet, comment-stripped, keyed relative to the v2 dir. */
export function readV2Css(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of readdirSync(V2_DIR, { recursive: true, encoding: 'utf8' })) {
    if (f.endsWith('.css')) out[f.split('\\').join('/')] = stripSource(f, readFileSync(join(V2_DIR, f), 'utf8'))
  }
  return out
}
