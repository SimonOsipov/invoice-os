/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { FRONTEND, monitoredSpas } from './monitoredSpas.testutil'

const codeLines = (dockerfile: string) =>
  dockerfile.split('\n').map((l) => l.trim()).filter((l) => l !== '' && !l.startsWith('#'))

describe('dockerfiles', () => {
  it('dockerfiles_everyMonitoredSpaBakesTheDsnAndReleaseFallback', () => {
    const monitored = monitoredSpas()
    expect(monitored).toEqual(['app', 'blog', 'landing', 'library', 'ops-console', 'support-console'])

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
        'VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST',
        'VITE_GATEWAY_URL=$VITE_GATEWAY_URL',
      ]) {
        const arg = lines.indexOf(`ARG ${key.split('=$')[1]}`)
        const env = lines.indexOf(`ENV ${key}`)
        expect(arg, `${name}: ARG for ${key} is not inside the build stage`).toBeGreaterThan(from)
        expect(env, `${name}: ENV ${key} is not after its ARG`).toBeGreaterThan(arg)
      }
      const sha = lines.indexOf('ENV VITE_BUILD_SHA=${BUILD_SHA}')
      const shaArg = lines.indexOf('ARG BUILD_SHA')
      expect(shaArg, `${name}: ARG BUILD_SHA is not inside the build stage`).toBeGreaterThan(from)
      expect(sha, `${name}: ENV VITE_BUILD_SHA is not after ARG BUILD_SHA`).toBeGreaterThan(shaArg)
      for (const want of [
        'ARG VITE_SENTRY_DSN',
        'ENV VITE_SENTRY_DSN=$VITE_SENTRY_DSN',
        'ARG VITE_SENTRY_TEST_DIGEST',
        'ENV VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST',
        'ARG BUILD_SHA',
        'ENV VITE_BUILD_SHA=${BUILD_SHA}',
        'ARG VITE_GATEWAY_URL',
        'ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL',
      ]) {
        const at = lines.indexOf(want)
        expect(at, `${name}/Dockerfile lacks "${want}" before its build RUN`).toBeGreaterThan(-1)
        expect(at, `${name}/Dockerfile has "${want}" after its build RUN`).toBeLessThan(build)
      }
    }

    // The library image builds the landing not-found page, so it bakes every VITE_* the landing does.
    const landingLines = codeLines(readFileSync(join(FRONTEND, 'landing/Dockerfile'), 'utf8'))
    const landingArgs = landingLines.filter((l) => /^ARG VITE_/.test(l)).map((l) => l.slice(4))
    expect(landingArgs.length).toBeGreaterThanOrEqual(10)
    const lib = codeLines(readFileSync(join(FRONTEND, 'library/Dockerfile'), 'utf8'))
    const libFrom = lib.findIndex((l) => /^FROM /.test(l))
    const libServe = lib.findIndex((l, i) => i > libFrom && /^FROM /.test(l))
    const libBuild = lib.findIndex((l) => /^RUN pnpm --filter/.test(l))
    for (const key of landingArgs) {
      const arg = lib.indexOf(`ARG ${key}`)
      expect(arg, `library: ARG ${key} is not inside the build stage`).toBeGreaterThan(libFrom)
      expect(lib.indexOf(`ENV ${key}=$${key}`), `library: ENV ${key} is not after its ARG`).toBeGreaterThan(arg)
      expect(arg, `library: ARG ${key} is after the first build RUN`).toBeLessThan(libBuild)
    }
    expect(lib.some((l) => /^RUN pnpm --filter @invoice-os\/landing exec vite build --base=\/library\/_landing\//.test(l))).toBe(true)
    const copy404 = lib.findIndex((l) => /^COPY --from=build \S*\/404\.html \/srv\/404\.html$/.test(l))
    expect(copy404, 'library: no COPY of 404.html to /srv/404.html in the serve stage').toBeGreaterThan(libServe)
  })

  it('dockerfiles_theAuthTokenReachesOnlyTheBuildStage', () => {
    const monitored = monitoredSpas()
    expect(monitored).toEqual(['app', 'blog', 'landing', 'library', 'ops-console', 'support-console'])

    for (const name of monitored) {
      const lines = codeLines(readFileSync(join(FRONTEND, name, 'Dockerfile'), 'utf8'))
      const froms = lines.flatMap((l, i) => (/^FROM\s/i.test(l) ? [i] : []))
      expect(froms.length, `${name}: expected a build FROM and a serve FROM`).toBeGreaterThanOrEqual(2)
      const buildFrom = froms[0]
      const build = lines.findIndex((l) => /^RUN\s+pnpm\s+--filter/i.test(l))
      expect(build, `${name}: no build RUN`).toBeGreaterThan(buildFrom)
      expect(build, `${name}: build RUN is not in the build stage`).toBeLessThan(froms[1])

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

      const serve = lines.slice(froms[1] + 1)
      expect(serve.length, `${name}: serve stage is empty`).toBeGreaterThan(0)
      expect(serve.filter((l) => /SENTRY_AUTH_TOKEN/i.test(l)), `${name}: serve stage names the token`).toEqual([])
    }
  })
})
