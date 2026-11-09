/// <reference types="node" />
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { CHECK_BASE } from '../../landing/src/sitePaths'
import { CHECK_DESCRIPTION, CHECK_H1, CHECK_INTRO, CHECK_TITLE } from './copy'

const HERE = dirname(fileURLToPath(import.meta.url))
const read = (p: string) => readFileSync(p, 'utf8')
const stripComments = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')

const DS = '@invoice-os/design-tokens'
const sideEffectImports = (src: string) =>
  [...stripComments(src).matchAll(/^\s*import\s+['"]([^'"]+)['"]/gm)].map((m) => m[1])
const dsImports = (src: string) => sideEffectImports(src).filter((s) => s.startsWith(DS))

const sources = () =>
  readdirSync(HERE, { recursive: true, encoding: 'utf8' })
    .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f) && !/\.d\.ts$/.test(f))
    .map((f) => ({ f, src: stripComments(read(join(HERE, f))) }))
const landingImports = (src: string) => [...src.matchAll(/(?:from|import)\s+['"]([^'"]*landing\/[^'"]*)['"]/g)]
const codeLines = (d: string) => d.split('\n').map((l) => l.trim()).filter((l) => l !== '' && !l.startsWith('#'))

describe('check package', () => {
  it('vite_buildsUnderTheCheckBase', async () => {
    const config = (await import('../vite.config')).default as { base?: string }
    expect(CHECK_BASE).toBe('/check')
    expect(config.base).toBe('/check/')
  })

  it('main_importsV2TokensInTheSupportConsoleOrder', () => {
    const want = [`${DS}/v2/styles.css`, `${DS}/v2/app-layer.css`, `${DS}/v2/mark.css`]
    expect(dsImports(read(join(HERE, '../../support-console/src/main.tsx')))).toEqual(want)
    const mine = dsImports(read(join(HERE, 'main.tsx')))
    expect(mine).toEqual(want)
    for (const s of mine) expect(s).toContain('/v2/')
  })

  it('dockerfile_servesTheBuildUnderTheBase', () => {
    const lines = codeLines(read(join(HERE, '../Dockerfile')))
    const serve = lines.findIndex((l) => /^FROM caddy:/.test(l))
    expect(serve).toBeGreaterThan(-1)
    const copies = lines.slice(serve).filter((l) => l.startsWith('COPY'))
    const copy = (re: RegExp) => expect(copies.some((l) => re.test(l)), String(re)).toBe(true)
    copy(/^COPY --from=build \/repo\/frontend\/check\/dist \/srv\/check$/)
    copy(/^COPY --from=build \/repo\/frontend\/check\/dist\/index\.html \/srv\/index\.html$/)
    copy(/^COPY --from=build \S*\/buildsha\.txt \/srv\/build\.txt$/)
    copy(/^COPY --from=build \S*\/buildsha\.txt \/srv\/check\/build\.txt$/)
    copy(/^COPY --from=build \S*\/check-404\/404\.html \/srv\/404\.html$/)
    copy(/^COPY --from=build \S*\/check-404\/assets \/srv\/check\/_landing\/assets$/)
    copy(/ \/srv\/check\/_landing\/$/)
    expect(lines.some((l) => /^RUN pnpm --filter @invoice-os\/landing exec vite build --base=\/check\/_landing\//.test(l))).toBe(true)
  })

  it('dockerfile_bakesEveryViteVarBeforeTheBuild', () => {
    const d = read(join(HERE, '../Dockerfile'))
    const stage = d.indexOf('AS build')
    const run = d.indexOf('RUN pnpm --filter @invoice-os/check build')
    expect(stage).toBeGreaterThan(-1)
    expect(run).toBeGreaterThan(stage)
    for (const v of ['VITE_GATEWAY_URL', 'VITE_LANDING_URL', 'VITE_GA_MEASUREMENT_ID', 'VITE_HUBSPOT_PORTAL_ID', 'VITE_SENTRY_DSN', 'VITE_SENTRY_TEST_DIGEST']) {
      const at = d.indexOf(`ARG ${v}\nENV ${v}=$${v}`)
      expect(at, v).toBeGreaterThan(stage)
      expect(at, v).toBeLessThan(run)
    }
    expect(d).toMatch(/^ARG SENTRY_AUTH_TOKEN$/m)
    expect(d).not.toMatch(/^ENV SENTRY_AUTH_TOKEN/m)
  })

  it('indexHtml_carriesTheCrawlerStringsOfCopy', () => {
    const html = read(join(HERE, '../index.html')).replace(/<script[\s\S]*?<\/script>/g, '')
    expect(/<title>([^<]*)<\/title>/.exec(html)?.[1]).toBe(CHECK_TITLE)
    expect(/<meta name="description" content="([^"]*)"/.exec(html)?.[1]).toBe(CHECK_DESCRIPTION)
    const root = /<div id="root">([\s\S]*)<\/div>/.exec(html)![1]
    expect([...root.matchAll(/<h1>([^<]*)<\/h1>/g)].map((m) => m[1])).toEqual([CHECK_H1])
    expect(/<p>([^<]*)<\/p>/.exec(root)?.[1]).toBe(CHECK_INTRO)
  })

  it('seam_onlyAnalyticsImportsLandingSource', () => {
    expect(
      sources()
        .filter(({ src }) => landingImports(src).length > 0)
        .map(({ f }) => f),
    ).toEqual(['analytics.ts'])
    const seam = sources().find(({ f }) => f === 'analytics.ts')!
    expect(landingImports(seam.src).length).toBeGreaterThanOrEqual(4)
  })

  it('seam_noSecondCopyOfTheConsentOrTagCode', () => {
    const needle = /asc_consent|googletagmanager|dataLayer|\b_ga\b/
    for (const { f, src } of sources().filter(({ f }) => f !== 'analytics.ts')) expect(src, f).not.toMatch(needle)
    expect(stripComments(read(join(HERE, '../../landing/src/analytics.ts')))).toMatch(needle)
  })

  // Every <form open tag must opt out; an open tag the matcher cannot read counts as a failure.
  const formsOptOut = (src: string) =>
    [...src.matchAll(/<form\b/g)].every((m) =>
      /^<form\b(?:[^>{]|\{[^}]*\})*>/.exec(src.slice(m.index))?.[0].includes('data-hs-do-not-collect="true"'),
    )

  it('form_everyOpenTagOptsOutOfHubSpotCapture', () => {
    expect(sources().map(({ f }) => f), 'floor: the source list is read').toContain('App.tsx')
    expect(formsOptOut('<form onSubmit={x}>'), 'control: a bare form is rejected').toBe(false)
    expect(formsOptOut('<form data-hs-do-not-collect="true">')).toBe(true)
    for (const { f, src } of sources()) expect(formsOptOut(src), f).toBe(true)
  })
})
