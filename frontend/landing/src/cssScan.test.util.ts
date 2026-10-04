// Shared source-scan helpers for the landing's CSS and build-input tests (D-41).
// The `.test.` in the name keeps this file out of every build-input scan.
/// <reference types="node" />
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, normalize } from 'node:path'
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

/** Every v2 stylesheet the landing loads (the relative `@import`s of `v2/styles.css`), comment-stripped, keyed relative to the v2 dir. */
export function readV2Css(): Record<string, string> {
  const out: Record<string, string> = { 'styles.css': stripSource('styles.css', readFileSync(join(V2_DIR, 'styles.css'), 'utf8')) }
  for (const m of out['styles.css'].matchAll(/@import\s+url\(\s*['"]?(\.?\/?[^'")]+\.css)['"]?\s*\)/g)) {
    const key = normalize(m[1]).split('\\').join('/')
    out[key] = stripSource(key, readFileSync(join(V2_DIR, key), 'utf8'))
  }
  return out
}

/** Hex a custom property resolves to through `var()` aliases, or null. */
export function hexOf(name: string, values: Map<string, string>): string | null {
  let v = values.get(name)
  for (let hops = 0; v && hops < 10; hops++) {
    if (/^#[0-9a-f]{6}$/i.test(v)) return v.toLowerCase()
    const m = /^var\(\s*(--[\w-]+)\s*\)$/.exec(v)
    v = m ? values.get(m[1]) : undefined
  }
  return null
}

export function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** WCAG contrast ratio of two hex colours. */
export function contrast(fg: string, bg: string): number {
  const [hi, lo] = [luminance(fg), luminance(bg)].sort((a, b) => b - a)
  return (hi + 0.05) / (lo + 0.05)
}

export type ContrastRow = { el: Element; text: string; fg: string; bg: string; ratio: number; ambiguous: boolean }

function hexOfValue(v: string | undefined, values: Map<string, string>): string {
  let cur = v?.trim().replace(/\s*!important$/i, '')
  for (let hops = 0; cur && hops < 10; hops++) {
    if (/^#[0-9a-f]{3}$/i.test(cur)) return '#' + [...cur.slice(1)].map((c) => c + c).join('').toLowerCase()
    if (/^#[0-9a-f]{6}$/i.test(cur)) return cur.toLowerCase()
    const m = /^var\(\s*(--[\w-]+)\s*\)$/.exec(cur)
    cur = m ? values.get(m[1]) : undefined
  }
  return ''
}

/** One row per element under `root` with a direct non-blank text node, resolved from the CSS texts in `css` and the ancestors above `root`. Single-class and `.band-x .y` selectors only; custom properties are read from rule declarations. */
export function resolveTextContrast(root: Element, css: readonly string[]): ContrastRow[] {
  const values = new Map<string, string>()
  for (const c of css)
    for (const r of parseRules(c))
      for (const d of declarations(r.body)) if (d.prop.startsWith('--')) values.set(d.prop, d.value)
  type R = { sel: string; prop: string; value: string; important: boolean }
  const rules: R[] = []
  for (const c of css)
    for (const r of parseRules(c)) {
      if (r.at.length) continue
      for (const sel of selectorParts(r))
        for (const d of declarations(r.body)) rules.push({ sel, prop: d.prop, value: d.value, important: /!\s*important/i.test(d.value) })
    }
  const matches = (el: Element, sel: string): number => {
    let m = /^\.([\w-]+)$/.exec(sel)
    if (m) return el.classList.contains(m[1]) ? 1 : 0
    m = /^\.(band-[\w]+) \.([\w-]+)$/.exec(sel)
    if (m) return el.classList.contains(m[2]) && el.parentElement?.closest('.' + m[1]) ? 2 : 0
    return 0
  }
  const decl = (el: Element, prop: string): { value: string; ambiguous: boolean } | null => {
    const hit = rules.map((r) => ({ r, spec: r.prop === prop ? matches(el, r.sel) : 0 })).filter((x) => x.spec > 0)
    if (!hit.length) return null
    const imp = hit.filter((x) => x.r.important)
    const pool = imp.length ? imp : hit
    const top = Math.max(...pool.map((x) => x.spec))
    const best = pool.filter((x) => x.spec === top)
    const vals = new Set(best.map((x) => hexOfValue(x.r.value, values)))
    return { value: best[0].r.value, ambiguous: vals.size > 1 }
  }
  const colour = (el: Element | null): { hex: string; ambiguous: boolean } => {
    for (let e = el; e; e = e.parentElement) {
      const inline = (e as HTMLElement).style?.color
      if (inline) return { hex: hexOfValue(inline, values), ambiguous: false }
      const d = decl(e, 'color')
      if (d) return { hex: hexOfValue(d.value, values), ambiguous: d.ambiguous }
    }
    return { hex: '', ambiguous: false }
  }
  const background = (el: Element): string => {
    for (let e: Element | null = el; e; e = e.parentElement) {
      const st = (e as HTMLElement).style
      const inline = st?.background || st?.backgroundColor
      if (inline && inline !== 'transparent') return hexOfValue(inline, values)
      for (const prop of ['background', 'background-color']) {
        const d = decl(e, prop)
        if (d && d.value !== 'transparent') return hexOfValue(d.value, values)
      }
    }
    return ''
  }
  const rows: ContrastRow[] = []
  for (const el of [root, ...Array.from(root.querySelectorAll('*'))]) {
    const text = Array.from(el.childNodes).filter((n) => n.nodeType === 3).map((n) => n.textContent ?? '').join('').replace(/\s+/g, ' ').trim()
    if (!text) continue
    const fg = colour(el)
    const bg = background(el)
    const ratio = fg.ambiguous ? NaN : contrast(fg.hex || '#000000', bg || '#ffffff')
    rows.push({ el, text, fg: fg.hex, bg, ratio, ambiguous: fg.ambiguous })
  }
  return rows
}
