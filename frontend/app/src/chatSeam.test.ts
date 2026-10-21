import { readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = __dirname

function walk(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name)
    return e.isDirectory() ? walk(p) : [p]
  })
}

const nonTest = (ext: RegExp) => walk(SRC).filter((f) => ext.test(f) && !/\.test\.|\.test\.util\./.test(f))
const stripComments = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
const landingImports = (s: string) =>
  [...stripComments(s).matchAll(/(?:from|import)\s*\(?\s*['"]([^'"]*landing\/[^'"]*)['"]/g)].map((m) => m[1])

describe('app chat seam', () => {
  it('seam_onlyChatImportsLandingSource', () => {
    const files = nonTest(/\.tsx?$/)
    const importers = files.filter((f) => landingImports(readFileSync(f, 'utf8')).length > 0).map((f) => relative(SRC, f))
    expect(importers).toEqual(['chat.ts'])
    expect(landingImports(readFileSync(join(SRC, 'chat.ts'), 'utf8')).length).toBeGreaterThanOrEqual(3)
  })

  it('seam_chatImportsNoAnalyticsModule', () => {
    const paths = landingImports(readFileSync(join(SRC, 'chat.ts'), 'utf8'))
    for (const p of paths) {
      expect(p).toMatch(/(?:\/consent|\/hubspot|\/hubspotChat|\/gaCookies|components\/CookieNotice)$/)
      expect(p).not.toMatch(/(?:analytics|consentActions)$/)
    }
  })

  it('every app form tells HubSpot not to collect it', () => {
    const openTags = (s: string) => [...s.matchAll(/<form\b(?:[^>]|=>)*>/g)].map((m) => m[0])
    const ok = (tag: string) => tag.includes('data-hs-do-not-collect="true"')
    const tags = nonTest(/\.tsx$/).flatMap((f) => openTags(stripComments(readFileSync(f, 'utf8'))))
    expect(tags.length).toBeGreaterThanOrEqual(5)
    expect(tags.filter((t) => !ok(t))).toEqual([])
    expect(ok('<form onSubmit={x}>')).toBe(false)
  })
})
