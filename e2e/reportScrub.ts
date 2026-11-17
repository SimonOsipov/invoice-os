// Redacts credentials from Playwright outputs before they are uploaded, then checks nothing survived.
// Usage: node e2e/reportScrub.ts <dir>...
// Node >= 22.18 runs this file without flags, so keep it to erasable TypeScript.

import { existsSync, readdirSync, realpathSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { unzipSync, zipSync } from 'fflate'
import type { Zippable } from 'fflate'

const R = '[redacted]'
// A JSON string body, plain or backslash-escaped; stops at the closing quote or backslash-quote.
const BODY = String.raw`(?:[^"\\]|\\[^"])*`
const Q = String.raw`\\*"`
// Typed-value bodies that also take an escaped quote inside the value: `\"` plain, `\\\"` in escaped JSON.
const BODY_PLAIN = String.raw`(?:[^"\\]|\\.)*`
const BODY_ESC = String.raw`(?:\\{3}"|[^"\\]|\\[^"])*`
const FIELDS = 'password|new_password|current_password|access_token|refresh_token|id_token|provider_token|token'
// Credential header names: the standard three plus every `x-*-token|key|secret|authorization` the e2e clients send.
const HDR = String.raw`(?:authorization|cookie|set-cookie|apikey|api-key|x-[\w-]*(?:token|key|secret|authorization))`
const HEADER_NAME = String.raw`(?:authorization|cookie|set-cookie|__Host-[^"\\]*|[^"\\]*(?:token|secret|key|session)[^"\\]*)`

// Raw header line up to its value: a name not glued to a word, or right after a JSON-escaped `\n`; an optional opening quote.
const LINE = String.raw`(?:(?<![\w-])|(?<=\\[nrt]))(?:proxy-)?${HDR}[ \t]*:[ \t]*(?:\\*")?`

const RULES: [RegExp, string][] = [
  [/eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*/g, R],
  [/eyJ[A-Za-z0-9_-]{10,}/g, R],
  // HAR shape: "name":"<header>","value":"<secret>"
  [new RegExp(`(${Q}name${Q}\\s*:\\s*${Q}${HEADER_NAME}${Q}\\s*,\\s*${Q}value${Q}\\s*:\\s*${Q})${BODY}`, 'gi'), `$1${R}`],
  [new RegExp(`(${Q}(?:proxy-)?${HDR}${Q}\\s*:\\s*${Q})${BODY}`, 'gi'), `$1${R}`],
  [new RegExp(`("(?:${FIELDS})"\\s*:\\s*")${BODY_PLAIN}`, 'gi'), `$1${R}`],
  [new RegExp(`(\\\\"(?:${FIELDS})\\\\"\\s*:\\s*\\\\")${BODY_ESC}`, 'gi'), `$1${R}`],
  // Raw header line: Authorization: Bearer <opaque>
  [new RegExp(`(${LINE})[^\\r\\n"\\\\]+`, 'gi'), `$1${R}`],
  [new RegExp(`(?<![A-Za-z0-9])((?:${FIELDS})=)[^&"'\\\\\\s]*`, 'gi'), `$1${R}`],
  // Step title `Fill "x"`, log line `fill("x")`.
  [new RegExp(`\\b((?:fill|type)[ (]${Q})${BODY}`, 'gi'), `$1${R}`],
  // Trace action: "method":"fill" ... "value":"x"
  [new RegExp(`(${Q}method${Q}\\s*:\\s*${Q}(?:fill|type)${Q}[^\\n]*?${Q}(?:value|text)${Q}\\s*:\\s*${Q})${BODY}`, 'g'), `$1${R}`],
]

export function scrubText(s: string): string {
  return RULES.reduce((acc, [re, to]) => acc.replace(re, to), s)
}

const BINARY = /\.(png|jpe?g|gif|webp|ico|webm|mp4|woff2?|ttf|otf|pdf)$/i
const PAYLOAD = /(data:application\/zip;base64,)([^"'<\s]*)/
const RESIDUALS: [string, RegExp][] = [['jwt', /eyJ[A-Za-z0-9_-]{10,}/], ['member-password', /e2e-member-pw-/], ['password-field', new RegExp(`${Q}password${Q}\\s*:\\s*${Q}(?!\\[redacted\\]\\\\*")`, 'i')],
  ['credential-header-key', new RegExp(`${Q}(?:proxy-)?${HDR}${Q}\\s*:\\s*${Q}(?!\\[redacted\\]\\\\*")`, 'i')],
  ['credential-header-line', new RegExp(`${LINE}(?!\\[redacted\\])[^\\s"\\\\]`, 'i')],
]

// latin1 keeps every byte, so an unchanged file round-trips exactly.
const toText = (b: Uint8Array) => Buffer.from(b).toString('latin1')
const fromText = (s: string) => new Uint8Array(Buffer.from(s, 'latin1'))

function decodePayload(p: string): Uint8Array {
  const buf = Buffer.from(p, 'base64')
  if (buf.toString('base64') !== p) throw new Error('bad base64')
  return new Uint8Array(buf)
}

/** scrubZip rewrites the text entries of a zip, recursing into nested zips. Returns the input when nothing changed. */
export function scrubZip(bytes: Uint8Array): Uint8Array {
  const entries = unzipSync(bytes)
  const out: Zippable = {}
  let changed = false
  for (const [name, data] of Object.entries(entries)) {
    const next = scrubEntry(name, data)
    if (next !== data) changed = true
    out[name] = [next, { level: BINARY.test(name) ? 0 : 6 }]
  }
  return changed ? zipSync(out) : bytes
}

function scrubEntry(name: string, data: Uint8Array): Uint8Array {
  if (BINARY.test(name)) return data
  if (/\.zip$/i.test(name)) return scrubZip(data)
  const text = toText(data)
  const next = scrubText(text)
  return next === text ? data : fromText(next)
}

function scrubHtml(bytes: Uint8Array): Uint8Array {
  let text = toText(bytes)
  const m = PAYLOAD.exec(text)
  if (m) {
    const inner = decodePayload(m[2])
    const next = scrubZip(inner)
    if (next !== inner) text = text.replace(PAYLOAD, (_, head: string) => head + Buffer.from(next).toString('base64'))
  }
  return text === toText(bytes) ? bytes : fromText(text)
}

function files(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? files(join(dir, e.name)) : [join(dir, e.name)]))
}

// Playwright's static trace-viewer bundle (`trace/` in an HTML report) holds no run data and breaks if rewritten.
// A file is skipped only when byte-identical to the same path in the installed playwright-core viewer.
let coreDir: string | null | undefined
export const viewerRoot = (): string | null => {
  if (coreDir === undefined) {
    try {
      // Follow the reporter's own chain: pnpm also holds other playwright-core copies, and a hoisted one is not the reporter's.
      const hop = (from: string, pkg: string) => realpathSync(createRequire(from).resolve(`${pkg}/package.json`))
      coreDir = dirname(hop(hop(hop(import.meta.url, '@playwright/test'), 'playwright'), 'playwright-core'))
    } catch {
      coreDir = null
    }
  }
  return coreDir && join(coreDir, 'lib/vite/traceViewer')
}
// The report viewer's code (report.js, report.css) is inlined into index.html; it holds no run data.
// Throws when the installed copy cannot be read: a silent miss would check the viewer code as run data.
let reportAssets: string[] | undefined
const reportViewer = (): string[] => {
  if (reportAssets) return reportAssets
  if (!viewerRoot()) throw new Error('playwright-core not found from @playwright/test; the report viewer code cannot be told from run data (pnpm install?)')
  reportAssets = ['report.js', 'report.css'].map((f) => {
    const path = join(coreDir!, 'lib/vite/htmlReport', f)
    if (!existsSync(path)) throw new Error(`report viewer asset missing: ${path}`)
    return readFileSync(path, 'latin1')
  })
  return reportAssets
}
const isViewerAsset = (root: string, file: string, bytes: Uint8Array): boolean => {
  const rel = relative(root, file).split(sep)
  const at = rel.indexOf('trace')
  const base = viewerRoot()
  if (at < 0 || at === rel.length - 1 || !base) return false
  const ref = join(base, ...rel.slice(at + 1))
  return existsSync(ref) && statSync(ref).isFile() && Buffer.from(bytes).equals(readFileSync(ref))
}

function scrubFile(file: string): void {
  const bytes = new Uint8Array(readFileSync(file))
  const next = /\.html?$/i.test(file) ? scrubHtml(bytes) : scrubEntry(file, bytes)
  if (next !== bytes) writeFileSync(file, next)
}

/** scrubDir rewrites every file under dir in place. Throws, naming the file, when one cannot be scrubbed. */
export function scrubDir(dir: string): void {
  for (const f of files(dir)) {
    try {
      if (isViewerAsset(dir, f, new Uint8Array(readFileSync(f)))) continue
      scrubFile(f)
    } catch {
      throw new Error(`${f}: cannot be scrubbed`)
    }
  }
}

type Hit = { file: string; rule: string; offset: number; region: string }

// The first match in data (zip contents included), located but never quoted.
function firstHit(name: string, data: Uint8Array, region: string): Omit<Hit, 'file'> | null {
  if (/\.zip$/i.test(name)) {
    for (const [n, d] of Object.entries(unzipSync(data))) {
      const hit = firstHit(n, d, `${region} > ${n}`)
      if (hit) return hit
    }
    return null
  }
  let text = toText(data)
  if (/\.html?$/i.test(name)) {
    const m = PAYLOAD.exec(text)
    if (m) {
      const hit = firstHit('payload.zip', decodePayload(m[2]), 'payload')
      if (hit) return hit
      text = text.replace(PAYLOAD, (_, head: string) => head + ' '.repeat(m[2].length))
    }
    // Blank (same length, so offsets hold) only text byte-identical to the viewer code; the rest is still checked.
    for (const asset of reportViewer()) text = text.split(asset).join(' '.repeat(asset.length))
    region = 'page text'
  }
  for (const [rule, re] of RESIDUALS) {
    const at = text.search(re)
    if (at >= 0) return { rule, offset: at, region }
  }
  return null
}

/** residualHits locates, per file under dir, the first credential left: rule, byte offset, region. Never the value. */
export function residualHits(dir: string): Hit[] {
  return files(dir).flatMap((f) => {
    try {
      const hit = firstHit(f, new Uint8Array(readFileSync(f)), 'file')
      return hit ? [{ file: f, ...hit }] : []
    } catch {
      throw new Error(`${f}: cannot be checked`)
    }
  })
}

/** residuals lists the files under dir (zip contents included) that still hold a credential. */
export function residuals(dir: string): string[] {
  return residualHits(dir).map((h) => h.file)
}

function main(dirs: string[]): number {
  if (dirs.length === 0) {
    console.error('usage: node e2e/reportScrub.ts <dir>...')
    return 2
  }
  const present = dirs.filter((d) => existsSync(d) && statSync(d).isDirectory())
  try {
    if (present.some((d) => files(d).some((f) => /\.html?$/i.test(f)))) reportViewer()
    present.forEach(scrubDir)
    const left = present.flatMap((d) => residualHits(d))
    if (left.length === 0) return 0
    for (const h of left) console.error(`::error::${h.file}: a credential survived the scrub (rule ${h.rule}, offset ${h.offset}, in ${h.region})`)
  } catch (err) {
    console.error(`::error::${err instanceof Error ? err.message : 'scrub failed'}`)
  }
  return 1
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exit(main(process.argv.slice(2)))
}
