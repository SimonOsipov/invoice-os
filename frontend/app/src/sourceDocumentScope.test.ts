// Scope-fence guards (DOC-02-09): mechanical source scans proving the Out-of-Scope fences
// hold. Extends lib/sourceDocument.test.ts:589-602's guard idiom -- which scans
// sourceDocument.ts alone -- to the six SourceDocument*.tsx components, the CSV/XLSX-only
// picker, and a repo-wide check for the prototype's state-switcher strip.
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const SRC_DIR = dirname(fileURLToPath(import.meta.url))
const COMPONENTS_DIR = join(SRC_DIR, 'components')

const SIX_COMPONENTS = [
  'SourceDocumentCard.tsx',
  'SourceDocumentModal.tsx',
  'SourceDocumentSheet.tsx',
  'SourceDocumentPages.tsx',
  'SourceDocumentRail.tsx',
  'SourceDocumentStates.tsx',
]

// The design's cut button label, "Download original", also appears verbatim inside this
// story's own explanatory comments (SourceDocumentModal.tsx, SourceDocumentStates.tsx)
// saying it was cut. Strip comments first so the needle proving its absence from CODE
// doesn't self-match the prose explaining that absence.
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
}

// Never a glob over `>= 6`: a stray match would satisfy that trivially. This IS the
// enumeration the vacuity floor below checks against the exact six-name set.
function resolveSixComponents(): string[] {
  return readdirSync(COMPONENTS_DIR).filter((f) => /^SourceDocument.*\.tsx$/.test(f) && !f.endsWith('.test.tsx'))
}

describe('no download affordance in the source-document components', () => {
  const files = resolveSixComponents()
  // Regexes, not substrings: ReviewUnreadableTab.tsx's `a.download = ` (with spaces) would
  // never match a bare `download=` literal, and scoping must not miss that exact shape.
  const NEEDLES = [/downloadGlyph/, /Download original/, /\.download\s*=/, /\bdownload\s*=/, /\.click\s*\(/]

  it('no download affordance in the source-document components', () => {
    for (const name of files) {
      const src = stripComments(readFileSync(join(COMPONENTS_DIR, name), 'utf8'))
      for (const needle of NEEDLES) {
        expect(src, `${name} must not match ${needle}`).not.toMatch(needle)
      }
    }
  })

  it('vacuity floor: the six components were actually read', () => {
    expect(files.slice().sort()).toEqual(SIX_COMPONENTS.slice().sort())
    for (const name of files) {
      const raw = readFileSync(join(COMPONENTS_DIR, name), 'utf8')
      expect(raw.length, `${name} must be non-empty`).toBeGreaterThan(0)
      // Positive control: proves the scan read real source-document components, not empty
      // stubs that would vacuously pass every negative needle above.
      expect(
        /data-testid="source-document/.test(raw) || /export function SourceDocument/.test(raw),
        `${name} must carry a source-document marker`,
      ).toBe(true)
    }
  })
})

describe('the import picker is unchanged', () => {
  const src = readFileSync(join(COMPONENTS_DIR, 'CreateUpload.tsx'), 'utf8')

  it('the import picker is unchanged', () => {
    expect(src.length, 'CreateUpload.tsx must be non-empty').toBeGreaterThan(0)
    // Positive control runs before the negatives: proves the read landed on the real picker.
    expect(src).toContain('accept=".csv,.xlsx"')
    for (const needle of [/\.pdf/, /\.png/, /\.jpe?g/, /\.webp/, /image\//]) {
      expect(src, `CreateUpload.tsx must not match ${needle}`).not.toMatch(needle)
    }
  })
})

describe('no prototype state strip ships', () => {
  // Built at runtime, never written as one contiguous literal -- this file lives under
  // frontend/app/src, so the walk below covers its own source too, and a literal here
  // would self-match the needle meant to prove the strip's absence everywhere else.
  const NEEDLE = ['PROTOTYPE', 'STATE'].join(' ')

  function walkSrc(): Array<{ path: string; content: string }> {
    const entries = readdirSync(SRC_DIR, { recursive: true, withFileTypes: true })
    return entries
      .filter((e) => e.isFile() && /\.(ts|tsx)$/.test(e.name))
      .map((e) => {
        const full = join(e.parentPath, e.name)
        return { path: full, content: readFileSync(full, 'utf8') }
      })
  }

  it('no prototype state strip ships', () => {
    for (const f of walkSrc()) {
      expect(f.content, `${f.path} must not contain the prototype state strip`).not.toContain(NEEDLE)
    }
  })

  it('vacuity floor: the walk read the tree', () => {
    const files = walkSrc()
    expect(files.length, 'the walk must have visited at least 100 files').toBeGreaterThanOrEqual(100)
    // Positive control from the SAME walk: proves file contents were read, not just names.
    expect(files.some((f) => f.content.includes('SourceDocumentCard'))).toBe(true)
  })
})
