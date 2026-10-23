/// <reference types="node" />
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
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

const srcFiles = () =>
  readdirSync(HERE, { recursive: true, encoding: 'utf8' })
    .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f) && !/\.d\.ts$/.test(f))
    .map((f) => join(HERE, f))

describe('blog package', () => {
  it('main_importsV2TokensInTheSupportConsoleOrder', () => {
    const want = [`${DS}/v2/styles.css`, `${DS}/v2/app-layer.css`]
    const ds = (f: string) => sideEffectImports(read(f)).filter((s) => s.startsWith(DS))
    expect(ds(join(HERE, '../../support-console/src/main.tsx'))).toEqual(want)
    expect(ds(join(HERE, 'main.tsx'))).toEqual(want)
  })

  // Relative imports from `file`, resolved to .ts/.tsx files.
  const relImports = (file: string) =>
    importSpecifiers(read(file))
      .filter((s) => s.startsWith('.'))
      .map((s) => {
        const base = join(dirname(file), s.replace(/\.tsx?$/, ''))
        return ['.ts', '.tsx'].map((e) => base + e).find((c) => existsSync(c))
      })
      .filter((c): c is string => c !== undefined)

  const LIBRARY = join(HERE, '../../library/') // trailing slash: a prefix test
  const blogFiles = () => [...srcFiles(), join(HERE, '../vite.config.ts'), join(HERE, '../vitest.config.ts')]

  it('src_importsOnlyTheRendererAndClockFromTheLibrary', () => {
    const direct = blogFiles().flatMap((f) =>
      relImports(f)
        .filter((t) => t.startsWith(LIBRARY))
        .map((t) => `${relative(HERE, f)} -> ${relative(LIBRARY, t)}`),
    )
    expect(direct.sort()).toEqual([
      'components/DemoBlock.tsx -> src/components/SceneView.tsx',
      'components/DemoBlock.tsx -> src/player.ts',
      'components/DemoBlock.tsx -> src/timing.ts',
      'demos.ts -> src/types.ts',
    ])
  })

  it('src_libraryImportsAreTheFilesLaneShSendsToTheFullLane', () => {
    const seen = new Set<string>()
    const walk = (f: string) => {
      if (seen.has(f)) return
      if (f.startsWith(LIBRARY)) seen.add(f)
      relImports(f).forEach(walk)
    }
    blogFiles().forEach(walk)
    const closure = [...seen].map((f) => 'frontend/library/' + relative(LIBRARY, f)).sort()
    const lane = read(join(HERE, '../../../scripts/ci/lane.sh'))
    const block = /^SHARED_WITH_BLOG=\(\n([\s\S]*?)^\)/m.exec(lane)
    const shared = (block?.[1] ?? '').split('\n').map((l) => l.trim()).filter(Boolean).sort()
    // Control: the closure holds the renderer.
    expect(closure).toContain('frontend/library/src/components/SceneView.tsx')
    expect(closure).toEqual(shared)
  })

  it('seam_onlyAnalyticsImportsLandingSource', () => {
    const importers = srcFiles()
      .filter((f) => importSpecifiers(read(f)).some((s) => s.includes('landing/')))
      .map((f) => relative(HERE, f))
    expect(importers).toEqual(['analytics.ts'])
    expect(importSpecifiers(read(join(HERE, 'analytics.ts'))).filter((s) => s.includes('landing/')).length).toBeGreaterThanOrEqual(4)
  })

  it('seam_noSecondCopyOfTheConsentOrTagCode', () => {
    const needle = /asc_consent|googletagmanager|dataLayer|\b_ga\b/
    const others = srcFiles().filter((f) => relative(HERE, f) !== 'analytics.ts')
    expect(others.length).toBeGreaterThan(3)
    expect(others.filter((f) => needle.test(stripComments(read(f)))).map((f) => relative(HERE, f))).toEqual([])
    // Control: the scan matches the landing's own tag code.
    expect(needle.test(read(join(HERE, '../../landing/src/analytics.ts')))).toBe(true)
  })

  it('dockerfile_bakesTheGaMeasurementId', () => {
    const lines = stripComments(read(join(HERE, '../Dockerfile'))).split('\n').map((l) => l.trim()).filter(Boolean)
    const from = lines.findIndex((l) => /AS build$/.test(l))
    const build = lines.findIndex((l) => l === 'RUN pnpm --filter @invoice-os/blog build')
    const arg = lines.indexOf('ARG VITE_GA_MEASUREMENT_ID')
    expect(lines.indexOf('ENV VITE_GA_MEASUREMENT_ID=$VITE_GA_MEASUREMENT_ID')).toBeGreaterThan(arg)
    expect(arg).toBeGreaterThan(from)
    expect(lines.indexOf('ENV VITE_GA_MEASUREMENT_ID=$VITE_GA_MEASUREMENT_ID')).toBeLessThan(build)
  })

  it('dockerfile_bakesEveryViteVarTheBlogReadsBeforeTheBuild', () => {
    const files = readdirSync(HERE, { recursive: true, encoding: 'utf8' })
      .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f) && !/\.d\.ts$/.test(f))
      .map((f) => join(HERE, f))
    const read_ = new Set(
      files.flatMap((f) => [...stripComments(read(f)).matchAll(/import\.meta\.env\.(VITE_[A-Z0-9_]+)/g)].map((m) => m[1])),
    )
    // Control: the scan sees the var links.ts reads.
    expect(read_.has('VITE_LANDING_URL')).toBe(true)

    const lines = stripComments(read(join(HERE, '../Dockerfile'))).split('\n').map((l) => l.trim()).filter(Boolean)
    const build = lines.findIndex((l) => /^RUN pnpm --filter/.test(l))
    expect(build).toBeGreaterThan(-1)
    // GA4 and the gateway URL are read by modules of later subtasks; their pairs ship with the image now.
    for (const v of new Set([...read_, 'VITE_GA_MEASUREMENT_ID', 'VITE_GATEWAY_URL'])) {
      const arg = lines.indexOf(`ARG ${v}`)
      const env = lines.indexOf(`ENV ${v}=$${v}`)
      expect(arg, `ARG ${v}`).toBeGreaterThan(-1)
      expect(env, `ENV ${v}`).toBeGreaterThan(arg)
      expect(env, `${v} before the build RUN`).toBeLessThan(build)
    }
    expect(lines.filter((l) => /VITE_APP_URL/.test(l))).toEqual([])
  })

  it('dockerfile_runsTheBlogBinaryOverTheBuiltSpa', () => {
    const lines = stripComments(read(join(HERE, '../Dockerfile'))).split('\n').map((l) => l.trim()).filter(Boolean)
    const froms = lines.flatMap((l, i) => (/^FROM\s/.test(l) ? [i] : []))
    expect(froms).toHaveLength(3)
    expect(lines[froms[0]]).toMatch(/^FROM node:\S+ AS build$/)
    expect(lines[froms[1]]).toMatch(/^FROM \S*golang:\S+ AS server$/)
    expect(lines[froms[2]]).toMatch(/^FROM \S*distroless\/static\S*:nonroot AS serve$/)

    const server = lines.slice(froms[1] + 1, froms[2])
    for (const copy of ['COPY go.mod go.sum ./', 'COPY cmd/blog cmd/blog', 'COPY internal/blogweb internal/blogweb', 'COPY internal/platform internal/platform']) {
      expect(server, copy).toContain(copy)
    }
    expect(server.filter((l) => /^COPY\s/.test(l))).toHaveLength(4)
    expect(server).toContain('RUN CGO_ENABLED=0 go build -o /out/blog ./cmd/blog')

    const serve = lines.slice(froms[2] + 1)
    for (const l of [
      'COPY --from=server /out/blog /blog',
      'COPY --from=build /repo/frontend/blog/dist /srv',
      'COPY --from=build /repo/internal/platform/buildsha.txt /srv/build.txt',
      'EXPOSE 8080',
      'USER nonroot:nonroot',
      'ENTRYPOINT ["/blog"]',
    ]) {
      expect(serve, l).toContain(l)
    }
  })

  it('indexHtml_keepsTheServerMarkers', () => {
    const html = read(join(HERE, '../index.html'))
    expect(html.match(/<title>/g)).toHaveLength(1)
    expect(html.match(/<\/title>/g)).toHaveLength(1)
    expect(html.match(/<div id="root"><\/div>/g)).toHaveLength(1)
    for (const tag of ['name="description"', 'rel="canonical"', 'name="robots"', 'property="og:']) {
      expect(html, `index.html carries ${tag}`).not.toContain(tag)
    }
  })

  // Every <form open tag must opt out; an open tag the matcher cannot read counts as a failure.
  const formsOptOut = (src: string) =>
    [...src.matchAll(/<form\b/g)].every((m) =>
      /^<form\b(?:[^>{]|\{[^}]*\})*>/.exec(src.slice(m.index))?.[0].includes('data-hs-do-not-collect="true"'),
    )

  it('form_everyOpenTagOptsOutOfHubSpotCapture', () => {
    const files = srcFiles()
    expect(files.length, 'control: the source list is read').toBeGreaterThanOrEqual(20)
    expect(files.map((f) => relative(HERE, f))).toContain('components/Subscribe.tsx')
    expect(formsOptOut('<form onSubmit={x}>'), 'control: a bare form is rejected').toBe(false)
    expect(formsOptOut('<form data-hs-do-not-collect="true">')).toBe(true)
    for (const f of files) expect(formsOptOut(stripComments(read(f))), relative(HERE, f)).toBe(true)
  })
})
