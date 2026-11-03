// Node-environment specs for hubspotChat.ts: the pure gate and the source scan.
// The DOM behaviour is hubspotChat.dom.test.ts.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { stripComments } from '@invoice-os/api-client/strip-comments'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { BLOG_HOSTNAMES, LIBRARY_HOSTNAMES } from './hubspot'
import { shouldLoadChat } from './hubspotChat'

const HERE = dirname(fileURLToPath(import.meta.url))

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...sourceFiles(path))
    else if (/\.tsx?$/.test(entry.name) && !/\.test\./.test(entry.name)) out.push(path)
  }
  return out
}

const FRONTEND = join(HERE, '..', '..')

/** Lines of a Dockerfile that break ARG < ENV < build RUN inside the build stage; [] when it holds. */
function dockerfileProblems(text: string): string[] {
  const lines = text.split('\n')
  const from = lines.findIndex((l) => /^FROM\b/.test(l))
  const arg = lines.findIndex((l) => l === 'ARG VITE_HUBSPOT_PORTAL_ID')
  const env = lines.findIndex((l) => l === 'ENV VITE_HUBSPOT_PORTAL_ID=$VITE_HUBSPOT_PORTAL_ID')
  const run = lines.findIndex((l) => /^RUN .*\bbuild\b/.test(l))
  const problems: string[] = []
  if (arg <= from) problems.push('ARG missing or before the build stage')
  if (env <= arg) problems.push('ENV missing or before ARG')
  if (run < 0 || env >= run) problems.push('ENV missing or after the build RUN')
  return problems
}

describe('the portal id', () => {
  it('the portal id is read in one place', () => {
    const files = sourceFiles(HERE).filter((p) => !p.endsWith('.d.ts'))
    expect(files.length, 'the scan read too few files').toBeGreaterThanOrEqual(20)

    const hits = files.flatMap((path) => {
      const count = stripComments(readFileSync(path, 'utf8')).split('import.meta.env.VITE_HUBSPOT_PORTAL_ID').length - 1
      return count > 0 ? [{ file: path.slice(HERE.length + 1), count }] : []
    })
    expect(hits).toEqual([{ file: 'hubspot.ts', count: 1 }])
  })

  it('every SPA that bundles the chat bakes the portal id', () => {
    const spas = readdirSync(FRONTEND, { withFileTypes: true }).filter((e) => e.isDirectory()).map((e) => e.name)
    const importsChat = (spa: string) => {
      const src = join(FRONTEND, spa, 'src')
      return existsSync(src) && sourceFiles(src).some((p) => /from\s+['"][^'"]*hubspotChat['"]/.test(stripComments(readFileSync(p, 'utf8'))))
    }
    const chatSpas = spas.filter(importsChat)
    for (const spa of ['app', 'blog', 'landing', 'library']) expect(chatSpas, 'control: ' + spa).toContain(spa)
    for (const spa of chatSpas) {
      expect(dockerfileProblems(readFileSync(join(FRONTEND, spa, 'Dockerfile'), 'utf8')), spa).toEqual([])
    }

    const late = 'FROM node AS build\nARG VITE_HUBSPOT_PORTAL_ID\nRUN pnpm build\nENV VITE_HUBSPOT_PORTAL_ID=$VITE_HUBSPOT_PORTAL_ID\n'
    expect(dockerfileProblems(late), 'control: ENV after RUN is reported').not.toEqual([])
  })
})

describe('shouldLoadChat', () => {
  it('AC2: shouldLoadChat truth table', () => {
    expect(shouldLoadChat('www.ascomply.com', true)).toBe(true)
    expect(shouldLoadChat('www.ascomply.com', false)).toBe(false)
    expect(shouldLoadChat('landing-pr-42-944095690109.africa-south1.run.app', true)).toBe(false)
    expect(shouldLoadChat('blog.ascomply.com', true, BLOG_HOSTNAMES)).toBe(true)
    expect(shouldLoadChat('www.ascomply.com', true, LIBRARY_HOSTNAMES)).toBe(true)
    expect(shouldLoadChat('www.ascomply.com', true, BLOG_HOSTNAMES)).toBe(true)
    expect(shouldLoadChat('blog.ascomply.com', true, LIBRARY_HOSTNAMES)).toBe(false)
  })
})

describe('form collection opt-out', () => {
  const OPENS_FORM = /<form\b[^>]*>/g
  const opted = (tag: string) => tag.includes('data-hs-do-not-collect="true"')

  it('every landing form tells HubSpot not to collect it', () => {
    const tags = sourceFiles(HERE).flatMap((path) => stripComments(readFileSync(path, 'utf8')).match(OPENS_FORM) ?? [])
    expect(tags.length, 'control: at least one form found').toBeGreaterThan(0)
    expect(tags.filter((tag) => !opted(tag))).toEqual([])
    expect(opted('<form noValidate>'), 'control: the predicate rejects a bare form').toBe(false)
  })
})
