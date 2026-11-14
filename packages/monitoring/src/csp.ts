/// <reference types="node" />
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

export interface CspExtra {
  script?: readonly string[]
  connect?: readonly string[]
  object?: readonly string[]
}

export interface CspOptions {
  inlineHashes: readonly string[]
  gateway?: string
  sentryDsn?: string
  extra?: CspExtra
}

// Structural subset of Vite's Plugin; this package does not depend on vite.
export interface CspPlugin {
  name: string
  apply: 'build'
  configResolved(config: { root: string; build: { outDir: string } }): void
  closeBundle(): Promise<void>
}

// Node-only: SPA vite configs import this by relative path; keep it out of index.ts.
export function inlineScriptHashes(html: string): string[] {
  const hashes: string[] = []
  for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi)) {
    if (/\bsrc\s*=/i.test(m[1] ?? '')) continue
    hashes.push(`'sha256-${createHash('sha256').update(m[2] ?? '').digest('base64')}'`)
  }
  return hashes
}

// The origin of an http(s) URL, or '' for anything else. Drops userinfo (the Sentry key) and path.
function originOf(url: string | undefined): string {
  try {
    const u = new URL(url ?? '')
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.origin : ''
  } catch {
    return ''
  }
}

export function contentSecurityPolicy(o: CspOptions): string {
  const extra = o.extra ?? {}
  const list = (...parts: (readonly string[] | string | undefined)[]) =>
    parts.flat().filter((s): s is string => !!s).join(' ')
  return [
    `script-src ${list("'self'", o.inlineHashes, extra.script)}`,
    `connect-src ${list("'self'", originOf(o.gateway), originOf(o.sentryDsn), extra.connect)}`,
    `object-src ${list(extra.object?.length ? extra.object : ["'none'"])}`,
    "base-uri 'none'",
    "frame-ancestors 'none'",
  ].join('; ')
}

export function cspPlugin(
  appDir: string,
  extra: CspExtra = {},
  env: Record<string, string | undefined> = process.env,
): CspPlugin {
  let outDir = join(appDir, 'dist')
  return {
    name: 'asc-csp',
    apply: 'build',
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir)
    },
    async closeBundle() {
      const index = join(outDir, 'index.html')
      if (!existsSync(index)) throw new Error(`cspPlugin: ${index} not found`)
      const policy = contentSecurityPolicy({
        inlineHashes: inlineScriptHashes(readFileSync(index, 'utf8')),
        gateway: env.VITE_GATEWAY_URL,
        sentryDsn: env.VITE_SENTRY_DSN,
        extra,
      })
      writeFileSync(join(appDir, 'csp.txt'), policy)
    },
  }
}
