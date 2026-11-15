/// <reference types="node" />
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { contentSecurityPolicy, cspPlugin, inlineScriptHashes } from './csp'

const GW = 'https://gw.example'
const DSN = 'https://k@o1.ingest.de.sentry.io/2'
const SENTRY = 'https://o1.ingest.de.sentry.io'
const sha = (text: string) => `'sha256-${createHash('sha256').update(text).digest('base64')}'`

const dirs: string[] = []
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})
function appDir(html: string | null): string {
  const d = mkdtempSync(join(tmpdir(), 'csp-test-'))
  dirs.push(d)
  mkdirSync(join(d, 'dist'))
  if (html !== null) writeFileSync(join(d, 'dist', 'index.html'), html)
  return d
}

describe('contentSecurityPolicy', () => {
  it('policy_consoleShape', () => {
    expect(contentSecurityPolicy({ inlineHashes: [], gateway: GW, sentryDsn: DSN })).toBe(
      `script-src 'self'; connect-src 'self' ${GW} ${SENTRY}; object-src 'none'; base-uri 'none'; frame-ancestors 'none'`,
    )
  })

  it('policy_appShapeOrdersHashesThenExtras', () => {
    const p = contentSecurityPolicy({
      inlineHashes: ["'sha256-A'", "'sha256-B'"],
      gateway: GW,
      sentryDsn: DSN,
      extra: { script: ['https://a.example'], connect: ['https://b.example'], object: ['blob:'] },
    })
    expect(p).toBe(
      `script-src 'self' 'sha256-A' 'sha256-B' https://a.example; connect-src 'self' ${GW} ${SENTRY} https://b.example; object-src blob:; base-uri 'none'; frame-ancestors 'none'`,
    )
  })

  it('policy_dsnKeyNeverAppears', () => {
    const p = contentSecurityPolicy({ inlineHashes: [], sentryDsn: 'https://k3y@o1.ingest.de.sentry.io/2' })
    expect(p).not.toContain('k3y')
    expect(p).not.toContain('@')
  })

  it('policy_gatewayPathKeepsOriginOnly', () => {
    const p = contentSecurityPolicy({ inlineHashes: [], gateway: 'https://gw.example/api/' })
    expect(p).toContain("connect-src 'self' https://gw.example;")
  })

  it('policy_emptyAndBadInputsAreOmitted', () => {
    for (const gateway of ['', 'not a url', 'ftp://x']) {
      const p = contentSecurityPolicy({ inlineHashes: [], gateway, sentryDsn: '' })
      expect(p).toContain("connect-src 'self';")
    }
  })

  it('policy_hasNoUnsafeSourceOrLineBreak', () => {
    const p = contentSecurityPolicy({
      inlineHashes: [sha('x')],
      gateway: GW,
      sentryDsn: DSN,
      extra: { script: ['https://www.googletagmanager.com'], connect: ['wss://*.hubspot.com'], object: ['blob:'] },
    })
    const script = p.split('; ')[0] ?? ''
    expect(p).not.toContain("'unsafe-inline'")
    expect(p).not.toContain("'unsafe-eval'")
    expect(p).not.toMatch(/(^| )\*( |;|$)/)
    expect(p.split(/[ ;]+/)).not.toContain('https:')
    expect(script).not.toContain('data:')
    expect(p).not.toMatch(/[\r\n]/)
  })
})

describe('inlineScriptHashes', () => {
  it('inlineScriptHashes_hashesExactInlineText', () => {
    const text = '\n    window.__x = 1\n  '
    const html = `<html><head><script type="module" crossorigin src="/a.js"></script>
<script>${text}</script><script src="/b.js"></script></head></html>`
    expect(inlineScriptHashes(html)).toEqual([sha(text)])
  })

  it('inlineScriptHashes_noneAndMany', () => {
    expect(inlineScriptHashes('<script src="/a.js"></script>')).toEqual([])
    expect(inlineScriptHashes('<script>a()</script><script type="x">b()</script>')).toEqual([sha('a()'), sha('b()')])
  })
})

describe('cspPlugin', () => {
  it('plugin_writesThePolicyFile', async () => {
    const text = 'boot()'
    const dir = appDir(`<script>${text}</script>`)
    const plugin = cspPlugin(dir, {}, { VITE_GATEWAY_URL: GW })
    plugin.configResolved({ root: dir, build: { outDir: 'dist' } })
    await plugin.closeBundle()
    const written = readFileSync(join(dir, 'csp.txt'), 'utf8')
    expect(written).toBe(contentSecurityPolicy({ inlineHashes: [sha(text)], gateway: GW }))
    expect(written.endsWith('\n')).toBe(false)
  })

  it('plugin_failsWithoutIndexHtml', async () => {
    const dir = appDir(null)
    const plugin = cspPlugin(dir, {}, {})
    plugin.configResolved({ root: dir, build: { outDir: 'dist' } })
    await expect(plugin.closeBundle()).rejects.toThrow(/index\.html/)
    expect(existsSync(join(dir, 'csp.txt'))).toBe(false)
  })
})
