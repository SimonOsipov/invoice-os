/// <reference types="node" />
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const HERE = dirname(fileURLToPath(import.meta.url))
const read = (p: string) => readFileSync(p, 'utf8')
const stripComments = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')

const DS = '@invoice-os/design-tokens'
const sideEffectImports = (src: string) =>
  [...stripComments(src).matchAll(/^\s*import\s+['"]([^'"]+)['"]/gm)].map((m) => m[1])
const importSpecifiers = (src: string) =>
  [...stripComments(src).matchAll(/(?:\bfrom|\bimport)\s*\(?\s*['"]([^'"]+)['"]/g)].map((m) => m[1])

describe('blog package', () => {
  it('main_importsV2TokensInTheSupportConsoleOrder', () => {
    const want = [`${DS}/v2/styles.css`, `${DS}/v2/app-layer.css`]
    const ds = (f: string) => sideEffectImports(read(f)).filter((s) => s.startsWith(DS))
    expect(ds(join(HERE, '../../support-console/src/main.tsx'))).toEqual(want)
    expect(ds(join(HERE, 'main.tsx'))).toEqual(want)
  })

  it('src_importsNothingFromTheLibrary', () => {
    const srcFiles = readdirSync(HERE, { recursive: true, encoding: 'utf8' })
      .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f) && !/\.d\.ts$/.test(f))
      .map((f) => join(HERE, f))
    const files = [...srcFiles, join(HERE, '../vite.config.ts'), join(HERE, '../vitest.config.ts')]
    const specs = files.flatMap((f) => importSpecifiers(read(f)).map((s) => ({ f, s })))
    // Control: the scan reads main.tsx and sees its `./instrument` import.
    expect(specs.filter(({ f }) => f.endsWith('main.tsx')).map(({ s }) => s)).toContain('./instrument')
    expect(specs.filter(({ s }) => s.includes('library/'))).toEqual([])
  })
})
