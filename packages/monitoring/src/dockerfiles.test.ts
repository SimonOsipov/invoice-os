/// <reference types="node" />
import { readdirSync, readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const FRONTEND = fileURLToPath(new URL('../../../frontend', import.meta.url))

const stripJsComments = (src: string) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
const codeLines = (dockerfile: string) =>
  dockerfile.split('\n').map((l) => l.trim()).filter((l) => l !== '' && !l.startsWith('#'))

describe('dockerfiles', () => {
  it('dockerfiles_everyMonitoredSpaBakesTheDsnAndReleaseFallback', () => {
    const spas = readdirSync(FRONTEND, { withFileTypes: true })
      .filter((e) => e.isDirectory() && existsSync(join(FRONTEND, e.name, 'src/main.tsx')))
      .map((e) => e.name)
      .sort()
    // Floor and control: landing has a main.tsx and a Dockerfile but never calls initMonitoring.
    expect(spas, 'the walk found no SPA').toContain('landing')

    const initsIn = (n: string) => {
      const f = join(FRONTEND, n, 'src/instrument.ts')
      return existsSync(f) && stripJsComments(readFileSync(f, 'utf8')).includes('initMonitoring(')
    }
    const monitored = spas.filter(initsIn)
    expect(monitored).toEqual(['app', 'ops-console', 'support-console'])

    // Control: the line finder sees a pre-existing app ARG, so a miss below is a real miss.
    const appLines = codeLines(readFileSync(join(FRONTEND, 'app/Dockerfile'), 'utf8'))
    expect(appLines.indexOf('ARG VITE_GATEWAY_URL')).toBeGreaterThan(-1)

    for (const name of monitored) {
      const lines = codeLines(readFileSync(join(FRONTEND, name, 'Dockerfile'), 'utf8'))
      const build = lines.findIndex((l) => /^RUN pnpm --filter/.test(l))
      expect(build, `${name}: no build RUN`).toBeGreaterThan(-1)
      // An ARG above the stage's FROM is out of scope for it, and an ENV above its ARG expands empty.
      const from = lines.findIndex((l) => /^FROM /.test(l))
      expect(from, `${name}: no FROM`).toBeGreaterThan(-1)
      for (const key of [
        'VITE_SENTRY_DSN=$VITE_SENTRY_DSN',
        'VITE_RAILWAY_GIT_COMMIT_SHA=$RAILWAY_GIT_COMMIT_SHA',
        'VITE_GATEWAY_URL=$VITE_GATEWAY_URL',
      ]) {
        const arg = lines.indexOf(`ARG ${key.split('=$')[1]}`)
        const env = lines.indexOf(`ENV ${key}`)
        expect(arg, `${name}: ARG for ${key} is not inside the build stage`).toBeGreaterThan(from)
        expect(env, `${name}: ENV ${key} is not after its ARG`).toBeGreaterThan(arg)
      }
      for (const want of [
        'ARG VITE_SENTRY_DSN',
        'ENV VITE_SENTRY_DSN=$VITE_SENTRY_DSN',
        'ARG RAILWAY_GIT_COMMIT_SHA',
        'ENV VITE_RAILWAY_GIT_COMMIT_SHA=$RAILWAY_GIT_COMMIT_SHA',
        'ARG VITE_GATEWAY_URL',
        'ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL',
      ]) {
        const at = lines.indexOf(want)
        expect(at, `${name}/Dockerfile lacks "${want}" before its build RUN`).toBeGreaterThan(-1)
        expect(at, `${name}/Dockerfile has "${want}" after its build RUN`).toBeLessThan(build)
      }
    }
  })
})
