/// <reference types="node" />
import { readdirSync, readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect } from 'vitest'

export const FRONTEND = fileURLToPath(new URL('../../../frontend', import.meta.url))

const stripJsComments = (src: string) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')

// SPAs under frontend/ whose src/instrument.ts calls initMonitoring(.
export function monitoredSpas(): string[] {
  const spas = readdirSync(FRONTEND, { withFileTypes: true })
    .filter((e) => e.isDirectory() && existsSync(join(FRONTEND, e.name, 'src/main.tsx')))
    .map((e) => e.name)
    .sort()
  // Floor: the walk found the SPAs; all five must be monitored below.
  expect(spas, 'the walk found no SPA').toContain('landing')

  const initsIn = (n: string) => {
    const f = join(FRONTEND, n, 'src/instrument.ts')
    return existsSync(f) && stripJsComments(readFileSync(f, 'utf8')).includes('initMonitoring(')
  }
  return spas.filter(initsIn)
}
