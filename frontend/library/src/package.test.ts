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
const dsImports = (src: string) => sideEffectImports(src).filter((s) => s.startsWith(DS))

describe('library package', () => {
  it('main_importsV2TokensInTheSupportConsoleOrder', () => {
    const want = [`${DS}/v2/styles.css`, `${DS}/v2/app-layer.css`]
    const control = dsImports(read(join(HERE, '../../support-console/src/main.tsx')))
    expect(control).toEqual(want)

    const lib = dsImports(read(join(HERE, 'main.tsx')))
    expect(lib).toEqual(want)
    expect(lib).not.toContain(`${DS}/styles.css`)
    for (const s of lib) expect(s).toContain('/v2/')
  })

  it('package_dependsOnNoApiClient', () => {
    const pkg = JSON.parse(read(join(HERE, '../package.json')))
    const keys = ['dependencies', 'devDependencies', 'peerDependencies', 'optionalDependencies'].flatMap((k) =>
      Object.keys(pkg[k] ?? {}),
    )
    expect(keys).toContain('react')
    expect(keys).not.toContain('@invoice-os/api-client')
  })

  it('src_makesNoNetworkCallAndImportsNoApiClient', () => {
    const files = readdirSync(HERE, { recursive: true, encoding: 'utf8' }).filter(
      (f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f) && !/\.d\.ts$/.test(f),
    )
    const sources = files.map((f) => stripComments(read(join(HERE, f))))
    expect(files).toContain('main.tsx')
    expect(sources.join('\n')).toContain('createRoot')
    const transport = /\bfetch\s*\(|XMLHttpRequest|sendBeacon|WebSocket|EventSource|axios|@invoice-os\/api-client/
    for (const [i, s] of sources.entries()) expect(s, files[i]).not.toMatch(transport)
  })
})
