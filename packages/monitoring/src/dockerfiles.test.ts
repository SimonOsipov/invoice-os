/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { FRONTEND, monitoredSpas } from './monitoredSpas.testutil'

const codeLines = (dockerfile: string) =>
  dockerfile.split('\n').map((l) => l.trim()).filter((l) => l !== '' && !l.startsWith('#'))

// Monitored SPAs that never call the gateway, so their image bakes no VITE_GATEWAY_URL.
const NO_GATEWAY = ['library']

describe('dockerfiles', () => {
  it('dockerfiles_everyMonitoredSpaBakesTheDsnAndReleaseFallback', () => {
    const monitored = monitoredSpas()
    expect(monitored).toEqual(['app', 'landing', 'library', 'ops-console', 'support-console'])

    // Control: the line finder sees a pre-existing app ARG, so a miss below is a real miss.
    const appLines = codeLines(readFileSync(join(FRONTEND, 'app/Dockerfile'), 'utf8'))
    expect(appLines.indexOf('ARG VITE_GATEWAY_URL')).toBeGreaterThan(-1)

    for (const name of monitored) {
      const gateway = !NO_GATEWAY.includes(name)
      const lines = codeLines(readFileSync(join(FRONTEND, name, 'Dockerfile'), 'utf8'))
      const build = lines.findIndex((l) => /^RUN pnpm --filter/.test(l))
      expect(build, `${name}: no build RUN`).toBeGreaterThan(-1)
      // An ARG above the stage's FROM is out of scope for it, and an ENV above its ARG expands empty.
      const from = lines.findIndex((l) => /^FROM /.test(l))
      expect(from, `${name}: no FROM`).toBeGreaterThan(-1)
      for (const key of [
        'VITE_SENTRY_DSN=$VITE_SENTRY_DSN',
        'VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST',
        'VITE_RAILWAY_GIT_COMMIT_SHA=$RAILWAY_GIT_COMMIT_SHA',
        ...(gateway ? ['VITE_GATEWAY_URL=$VITE_GATEWAY_URL'] : []),
      ]) {
        const arg = lines.indexOf(`ARG ${key.split('=$')[1]}`)
        const env = lines.indexOf(`ENV ${key}`)
        expect(arg, `${name}: ARG for ${key} is not inside the build stage`).toBeGreaterThan(from)
        expect(env, `${name}: ENV ${key} is not after its ARG`).toBeGreaterThan(arg)
      }
      for (const want of [
        'ARG VITE_SENTRY_DSN',
        'ENV VITE_SENTRY_DSN=$VITE_SENTRY_DSN',
        'ARG VITE_SENTRY_TEST_DIGEST',
        'ENV VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST',
        'ARG RAILWAY_GIT_COMMIT_SHA',
        'ENV VITE_RAILWAY_GIT_COMMIT_SHA=$RAILWAY_GIT_COMMIT_SHA',
        ...(gateway ? ['ARG VITE_GATEWAY_URL', 'ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL'] : []),
      ]) {
        const at = lines.indexOf(want)
        expect(at, `${name}/Dockerfile lacks "${want}" before its build RUN`).toBeGreaterThan(-1)
        expect(at, `${name}/Dockerfile has "${want}" after its build RUN`).toBeLessThan(build)
      }
    }
  })

  it('dockerfiles_theLibraryBakesNoGatewayUrl', () => {
    const monitored = monitoredSpas()
    for (const name of NO_GATEWAY) expect(monitored, `${name} is not a monitored SPA`).toContain(name)

    // Control: the line finder sees a pre-existing app ARG.
    const appLines = codeLines(readFileSync(join(FRONTEND, 'app/Dockerfile'), 'utf8'))
    expect(appLines).toContain('ARG VITE_GATEWAY_URL')

    for (const name of NO_GATEWAY) {
      const lines = codeLines(readFileSync(join(FRONTEND, name, 'Dockerfile'), 'utf8'))
      expect(lines.filter((l) => l.includes('VITE_GATEWAY_URL')), `${name} names VITE_GATEWAY_URL`).toEqual([])
    }
  })

  it('dockerfiles_theAuthTokenReachesOnlyTheBuildStage', () => {
    const monitored = monitoredSpas()
    expect(monitored).toEqual(['app', 'landing', 'library', 'ops-console', 'support-console'])

    for (const name of monitored) {
      const lines = codeLines(readFileSync(join(FRONTEND, name, 'Dockerfile'), 'utf8'))
      const froms = lines.flatMap((l, i) => (/^FROM\s/i.test(l) ? [i] : []))
      expect(froms, `${name}: expected a build FROM and a serve FROM`).toHaveLength(2)
      const [buildFrom, serveFrom] = froms
      const build = lines.findIndex((l) => /^RUN\s+pnpm\s+--filter/i.test(l))
      expect(build, `${name}: no build RUN`).toBeGreaterThan(buildFrom)
      expect(build, `${name}: build RUN is not in the build stage`).toBeLessThan(serveFrom)

      // Control: the same finder locates a pre-existing ARG inside the build stage.
      const dsn = lines.findIndex((l) => /^ARG\s+VITE_SENTRY_DSN$/i.test(l))
      expect(dsn, `${name}: finder missed ARG VITE_SENTRY_DSN`).toBeGreaterThan(buildFrom)
      expect(dsn).toBeLessThan(build)

      const args = lines.flatMap((l, i) => (/^ARG\s+SENTRY_AUTH_TOKEN$/i.test(l) ? [i] : []))
      expect(args, `${name}/Dockerfile must declare ARG SENTRY_AUTH_TOKEN exactly once`).toHaveLength(1)
      expect(args[0], `${name}: ARG SENTRY_AUTH_TOKEN is before the build FROM`).toBeGreaterThan(buildFrom)
      expect(args[0], `${name}: ARG SENTRY_AUTH_TOKEN is after the build RUN`).toBeLessThan(build)

      // The token must not persist in image metadata or a default value.
      expect(lines.filter((l) => /^ENV\b.*SENTRY_AUTH_TOKEN/i.test(l)), `${name}: ENV names the token`).toEqual([])
      expect(lines.filter((l) => /SENTRY_AUTH_TOKEN=/i.test(l)), `${name}: token assigned`).toEqual([])

      const serve = lines.slice(serveFrom + 1)
      expect(serve.length, `${name}: serve stage is empty`).toBeGreaterThan(0)
      expect(serve.filter((l) => /SENTRY_AUTH_TOKEN/i.test(l)), `${name}: serve stage names the token`).toEqual([])
    }
  })
})
