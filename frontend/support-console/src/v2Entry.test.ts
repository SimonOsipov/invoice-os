/// <reference types="node" />
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { stripComments } from '@invoice-os/api-client/strip-comments'

import { BrandMark } from './icons'

const HERE = dirname(fileURLToPath(import.meta.url))
const TOKENS = join(HERE, '../../../packages/design-tokens')
const read = (p: string) => readFileSync(p, 'utf8')
const stripCss = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '')

const MAIN = read(join(HERE, 'main.tsx'))
const ICONS = read(join(HERE, 'icons.tsx'))
const INDEX_HTML = read(join(HERE, '../index.html'))
const SUPPORT_CSS = read(join(HERE, 'styles/support.css'))
const DS = '@invoice-os/design-tokens'

const SRC_FILES = readdirSync(HERE, { recursive: true, encoding: 'utf8' })
  .filter((f) => /\.(ts|tsx|css)$/.test(f) && !/\.test\.tsx?$/.test(f) && !/\.d\.ts$/.test(f))
  .sort()
const stripSrc = (f: string) => (f.endsWith('.css') ? stripCss(read(join(HERE, f))) : stripComments(read(join(HERE, f))))

const ruleBodies = (css: string, selector: string) =>
  [...stripCss(css).matchAll(/([^{}]+)\{([^{}]*)\}/g)]
    .filter((m) => m[1].split(',').some((sel) => sel.trim() === selector))
    .map((m) => m[2])
const declarations = (body: string) =>
  new Map(
    body
      .split(';')
      .map((d) => d.trim())
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()] as const),
  )

// Source scans for v1 drift: v1 vocabulary renders the same under v2 (radius-input resolves to radius-btn),
// so no runtime read sees it. Each scan is a pure function over a { path: source } map.
const stripKeepLines = (src: string, re: RegExp) => src.replace(re, (m) => m.replace(/[^\n]/g, ''))
const stripFile = (file: string, src: string) =>
  file.endsWith('.css')
    ? stripKeepLines(src, /\/\*[\s\S]*?\*\//g)
    : file.endsWith('.html')
      ? stripKeepLines(src, /<!--[\s\S]*?-->/g)
      : stripComments(src)
const lineOf = (src: string, index: number) => src.slice(0, index).split('\n').length

const V1_NEEDLES = [/oklch\(/i, /fraunces/i, /\binter\b/i, /--gradient-/i, /radius-pill/i, /radius-input/i, /radius-xs/i, /shadow-soft/i, /shadow-elegant/i]
const scanVocabulary = (files: Record<string, string>) =>
  Object.entries(files).flatMap(([file, raw]) =>
    stripFile(file, raw)
      .split('\n')
      .flatMap((text, i) => V1_NEEDLES.filter((re) => re.test(text)).map((re) => `${file}:${i + 1} ${re}`)),
  )

const CORNERS_ALLOWED = ["'var(--radius-sm)'", "'var(--radius-md)'", "'var(--radius-lg)'", "'var(--radius-btn)'", "'50%'", '2']
const TOGGLE_FILE = 'components/Rules.tsx'
const scanCorners = (files: Record<string, string>) => {
  const values = Object.entries(files).flatMap(([file, raw]) => {
    const src = stripFile(file, raw)
    return [...src.matchAll(/borderRadius\s*:\s*([^,}]+)/g)].map((m) => ({ file, line: lineOf(src, m.index), value: m[1].trim() }))
  })
  const toggles = values.filter((v) => v.value === '99' && v.file === TOGGLE_FILE)
  const bad = values.filter((v) => !CORNERS_ALLOWED.includes(v.value) && !(v.value === '99' && v === toggles[0]))
  return {
    values,
    violations: [
      ...bad.map((v) => `${v.file}:${v.line} borderRadius ${v.value}`),
      ...(toggles.length === 0 ? [`${TOGGLE_FILE} has no 99 (the toggle track keeps it)`] : []),
    ],
  }
}

const FG4_STAYS: [file: string, needle: string][] = [
  ['components/Submissions.tsx', '{CHEVRON_RIGHT_ICON}'],
  ['components/Submissions.tsx', 'All tenants <span'],
  ['components/Submissions.tsx', 'Last 24h <span'],
  ['components/Submissions.tsx', "=== '—' ? 'var(--fg-4)'"],
  ['components/Audit.tsx', '{CHEVRON_RIGHT_ICON}'],
]
const scanFg4 = (files: Record<string, string>, stays: [string, string][]) => {
  const lines = Object.entries(files).flatMap(([file, raw]) =>
    stripFile(file, raw)
      .split('\n')
      .flatMap((text, i) => (/fg-4/i.test(text) ? [{ file, at: `${file}:${i + 1}`, text }] : [])),
  )
  const hit = (l: { file: string; text: string }, [file, needle]: [string, string]) => l.file === file && l.text.includes(needle)
  return {
    lines,
    unmatched: lines.filter((l) => !stays.some((s) => hit(l, s))).map((l) => `${l.at}: ${l.text.trim()}`),
    stale: stays.filter((s) => !lines.some((l) => hit(l, s))).map(([f, n]) => `${f}: ${n}`),
  }
}

const SCAN_FILES: Record<string, string> = Object.fromEntries(
  SRC_FILES.map((f) => [f, read(join(HERE, f))]),
)
const TSX_FILES = Object.fromEntries(Object.entries(SCAN_FILES).filter(([f]) => f.endsWith('.tsx')))

describe('v2 entry', () => {
  it('VE-01 main.tsx loads the v2 entry, then the layer, then support.css', () => {
    const specifiers = [...stripComments(MAIN).matchAll(/^\s*import\s+['"]([^'"]+)['"]/gm)].map((m) => m[1])
    expect(specifiers.length).toBeGreaterThan(0)
    const at = (s: string) => specifiers.indexOf(s)

    expect(at(`${DS}/v2/styles.css`), 'v2/styles.css imported').toBeGreaterThanOrEqual(0)
    expect(at(`${DS}/v2/app-layer.css`), 'v2/app-layer.css after v2/styles.css').toBeGreaterThan(at(`${DS}/v2/styles.css`))
    expect(at('./styles/support.css'), 'support.css after the layer').toBeGreaterThan(at(`${DS}/v2/app-layer.css`))
    expect(specifiers).not.toContain(`${DS}/styles.css`)
    const designTokens = specifiers.filter((s) => s.startsWith(`${DS}/`))
    expect(designTokens.length).toBeGreaterThan(0)
    for (const s of designTokens) expect(s, 'only v2 entries').toContain('/v2/')

    const everywhere = SRC_FILES.flatMap((f) =>
      [...stripSrc(f).matchAll(/['"]@invoice-os\/design-tokens\/([^'"]+)['"]/g)].map((m) => `${f}: ${m[1]}`),
    )
    expect(everywhere.length, 'design-tokens specifiers across src').toBeGreaterThanOrEqual(3)
    expect(everywhere.filter((e) => !e.split(': ')[1].startsWith('v2/')), 'no v1 design-tokens specifier in any src file').toEqual([])
  })

  it('VE-02 every design-tokens specifier is exported (pin, green at write)', () => {
    const exported = Object.keys(JSON.parse(read(join(TOKENS, 'package.json'))).exports)
    const specifiers = [MAIN, ICONS].flatMap((src) =>
      [...stripComments(src).matchAll(/['"]@invoice-os\/design-tokens\/([^'"]+)['"]/g)].map((m) => `./${m[1]}`),
    )
    expect(specifiers.length).toBeGreaterThan(0)
    for (const s of specifiers) expect(exported, `${s} is a key of package.json exports`).toContain(s)
  })

  it('VE-03 index.html links Manrope and IBM Plex Mono only', () => {
    const html = INDEX_HTML.replace(/<!--[\s\S]*?-->/g, '').replace(/&amp;/g, '&')
    const links = [...html.matchAll(/<link\b[^>]*>/gi)].map((m) => m[0])
    const hrefs = links
      .filter((tag) => /\brel\s*=\s*["']?stylesheet/i.test(tag))
      .map((tag) => tag.match(/\bhref\s*=\s*["']([^"']*)["']/i)?.[1] ?? '')
    expect(hrefs).toHaveLength(2)

    const importOf = (css: string) => {
      const urls = [...css.matchAll(/@import\s+url\('([^']+)'\)/g)].map((m) => m[1])
      expect(urls).toHaveLength(1)
      return urls[0]
    }
    const manrope = importOf(read(join(TOKENS, 'v2/tokens/typography.css')))
    const plex = importOf(read(join(TOKENS, 'v2/app-layer.css')))
    expect([...hrefs].sort(), 'stylesheet hrefs equal the two token @import URLs').toEqual([manrope, plex].sort())

    const retired = [/\binter\b/i, /fraunces/i]
    expect(
      retired.map((re) => re.test('family=INTER:wght@400&family=FRAUNCES')),
      'control: each needle matches in any case',
    ).toEqual([true, true])
    expect(retired.some((re) => re.test('interval internal')), 'control: inter inside a word is no hit').toBe(false)
    for (const re of retired) expect(html, `index.html names ${re}`).not.toMatch(re)

    expect(links.filter((tag) => /\brel\s*=\s*["']?preconnect/i.test(tag))).toHaveLength(2)
  })

  it('VE-04 BrandMark renders the v2 mark at 26 with a 6px corner', () => {
    const imgOf = (size?: number) => {
      const html = renderToStaticMarkup(createElement(BrandMark, size === undefined ? {} : { size }))
      const img = html.match(/<img\b[^>]*>/)?.[0]
      expect(img, `no <img> in ${html}`).toBeDefined()
      return (name: string) => img!.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1]
    }

    const attr = imgOf()
    expect.soft(attr('src')).toMatch(/v2\/assets\/mark\.png(\?.*)?$/)
    expect.soft(attr('width')).toBe('26')
    expect.soft(attr('height')).toBe('26')
    expect.soft(attr('alt')).toBe('')
    expect.soft(attr('aria-hidden')).toBe('true')
    expect.soft(attr('style')).toContain('display:block')
    expect.soft(attr('style')).toContain('border-radius:var(--radius-md)')

    const small = imgOf(20)
    expect.soft(small('width'), 'an explicit size still wins').toBe('20')
    expect.soft(small('height'), 'an explicit size wins on height too').toBe('20')
    expect.soft(small('style'), 'an explicit size keeps the corner').toContain('border-radius:var(--radius-md)')
  })

  it('VE-05 the dead .ops-input:focus rule names --ring (source pin)', () => {
    // The rule matches no element under the layer, so no runtime read can see it.
    const blocks = ruleBodies(SUPPORT_CSS, '.ops-input:focus')
    expect(blocks, 'support.css has a .ops-input:focus block').toHaveLength(1)

    const body = blocks[0]
    expect(body, '.ops-input:focus declares border-color: var(--ring)').toMatch(/(^|[;\s])border-color\s*:\s*var\(--ring\)\s*(;|$)/i)
    expect(body, '.ops-input:focus no longer reads --accent').not.toMatch(/var\(--accent\)/i)
  })

  it('VE-06 every owned var() resolves under v2 (pin, green at write)', () => {
    const cssDeclared = ['tokens/colors.css', 'tokens/hero-grid.css', 'tokens/spacing.css', 'tokens/typography.css', 'utilities.css', 'app-layer.css']
    const declared = new Set(
      cssDeclared.flatMap((f) => [...stripCss(read(join(TOKENS, 'v2', f))).matchAll(/(?:^|[\s;{])(--[\w-]+)\s*:/g)].map((m) => m[1])),
    )
    expect(declared.size, 'declared custom properties').toBeGreaterThan(30)
    expect(declared.has('--ring')).toBe(true)

    expect(SRC_FILES).toContain('styles/support.css')
    expect(SRC_FILES).toContain('icons.tsx')

    const used = new Map<string, string[]>()
    for (const f of SRC_FILES) {
      for (const m of stripSrc(f).matchAll(/var\(\s*(--[\w-]+)/g)) used.set(m[1], [...(used.get(m[1]) ?? []), f])
    }
    expect(used.size, 'var() names collected').toBeGreaterThan(30)

    const undeclared = [...used].filter(([name]) => !declared.has(name)).map(([name, fs]) => `${name} in ${[...new Set(fs)].join(', ')}`)
    expect(undeclared, 'var() names no v2 file declares').toEqual([])
  })

  it('VE-10 support.css field rules, input border and corners (source pin: jsdom applies no CSS; resolved reads are SUP-03 and SUP-04)', () => {
    const decl = (selector: string, prop: string) => {
      const bodies = ruleBodies(SUPPORT_CSS, selector)
      expect(bodies, `support.css has one ${selector} block`).toHaveLength(1)
      return declarations(bodies[0]).get(prop)
    }

    expect(decl('.ops-input', 'border')).toBe('1px solid var(--input)')
    expect(decl('.ops-input', 'border-radius')).toBe('var(--radius-btn)')
    expect(decl('pre.ops-json', 'border-radius')).toBe('var(--radius-md)')
    expect(decl('.ops-field:focus-within', 'border-color')).toBe('var(--ring) !important')
    expect(decl('.ops-field:focus-within', 'box-shadow')).toBe('0 0 0 2px var(--ring) !important')
    expect(decl('.asc-app .ops-field input:focus', 'box-shadow')).toBe('none !important')
  })

  it('VE-07 no owned file carries v1 vocabulary (source scan: v1 names render the same under v2, so no runtime test sees them)', () => {
    const planted = {
      'a.tsx': ['OKLCH(0.5 0 0)', 'Fraunces', 'Inter', '--Gradient-x', 'RADIUS-PILL', 'Radius-Input', 'Radius-XS', 'Shadow-Soft', 'Shadow-Elegant'].join('\n'),
      'a2.tsx': '/* one\ntwo */\nconst x = "radius-input"',
      'b.tsx': V1_NEEDLES.map((re) => `// ${re.source}`).join('\n') + '\n/* oklch( fraunces Inter radius-pill */',
      'c.css': '/* oklch( fraunces Inter --gradient- radius-pill radius-input radius-xs shadow-soft shadow-elegant */',
      'd.html': '<!-- Inter fraunces -->\n<p>ok</p>',
      'e.tsx': 'interval internal',
    }
    const found = scanVocabulary(planted)
    expect(found.filter((h) => h.startsWith('a.tsx')), 'each needle hits its planted line in mixed case').toEqual(
      V1_NEEDLES.map((re, i) => `a.tsx:${i + 1} ${re}`),
    )
    expect(found, 'a hit after a multi-line comment keeps its line').toContain('a2.tsx:3 /radius-input/i')
    expect(found.filter((h) => !/^a2?\.tsx/.test(h)), 'comments and interval/internal are no hit').toEqual([])

    const scanned = { ...SCAN_FILES, '../index.html': INDEX_HTML }
    const names = Object.keys(scanned)
    expect(names.length, 'owned files scanned').toBeGreaterThanOrEqual(25)
    for (const f of ['components/Submissions.tsx', 'components/Health.tsx', 'styles/support.css', 'icons.tsx', '../index.html']) {
      expect(names, `${f} is scanned`).toContain(f)
    }
    expect(scanVocabulary(scanned), 'v1 vocabulary (file:line needle)').toEqual([])
  })

  it('VE-08 every TSX corner is a v2 corner (source scan: a pill renders as a pill under v2, so no runtime test sees the value)', () => {
    const ok = "export const a = { borderRadius: 'var(--radius-md)' }\nexport const b = { borderRadius: 99 }\nexport const c = { borderRadius: 2 }\n"
    expect(scanCorners({ [TOGGLE_FILE]: ok }).violations, 'control: one 99 in Rules.tsx and the allowed corners pass').toEqual([])
    expect(scanCorners({ [TOGGLE_FILE]: ok + 'const d = { borderRadius: 999 }' }).violations, 'a planted 999 fails').toEqual([`${TOGGLE_FILE}:4 borderRadius 999`])
    expect(scanCorners({ [TOGGLE_FILE]: ok + 'const d = { borderRadius: 99 }' }).violations, 'a second 99 in Rules.tsx fails').toEqual([`${TOGGLE_FILE}:4 borderRadius 99`])
    expect(scanCorners({ [TOGGLE_FILE]: ok, 'components/Audit.tsx': 'const d = { borderRadius: 99 }' }).violations, 'a 99 in another file fails').toEqual([
      'components/Audit.tsx:1 borderRadius 99',
    ])
    expect(scanCorners({ [TOGGLE_FILE]: ok + "const d = { borderRadius: 'var(--radius-input)' }" }).violations, 'a v1 token fails').toHaveLength(1)
    expect(scanCorners({ [TOGGLE_FILE]: ok + '// borderRadius: 99\n/* borderRadius: 999 */' }).violations, 'a comment is no corner').toEqual([])
    expect(scanCorners({ [TOGGLE_FILE]: 'const a = { borderRadius: 2 }' }).violations, 'Rules.tsx without its toggle 99 fails').toHaveLength(1)

    const { values, violations } = scanCorners(TSX_FILES)
    expect(Object.keys(TSX_FILES), 'Rules.tsx is scanned').toContain(TOGGLE_FILE)
    expect(values.length, 'corner values collected').toBeGreaterThan(40)
    expect(violations, 'corners outside the v2 set (file:line value)').toEqual([])
  })

  it('VE-09 --fg-4 stays only on icons and glyphs (source scan: the colour renders, so only the source names the use)', () => {
    const stays: [string, string][] = [['a.tsx', '{ICON}']]
    const planted = {
      'a.tsx': "<span style={{ color: 'var(--fg-4)' }}>{ICON}</span>\n<span style={{ color: 'var(--fg-4)' }}>words</span>\n// var(--fg-4)",
    }
    const found = scanFg4(planted, stays)
    expect(found.lines, 'control: both uses are read, the comment is not').toHaveLength(2)
    expect(found.unmatched, 'a planted --fg-4 on a text span is reported').toEqual(["a.tsx:2: <span style={{ color: 'var(--fg-4)' }}>words</span>"])
    expect(scanFg4(planted, [...stays, ['a.tsx', 'gone']]).stale, 'a needle that matches no line is reported').toEqual(['a.tsx: gone'])

    const { lines, unmatched, stale } = scanFg4(SCAN_FILES, FG4_STAYS)
    expect(lines.length, '--fg-4 lines found').toBeGreaterThanOrEqual(FG4_STAYS.length)
    expect(unmatched, '--fg-4 on enabled text (move to --fg-3)').toEqual([])
    expect(stale, 'stale allowlist needles').toEqual([])
  })
})
