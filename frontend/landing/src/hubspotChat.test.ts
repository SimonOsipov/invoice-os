// Node-environment specs for hubspotChat.ts: the pure gate and the source scan.
// The DOM behaviour is hubspotChat.dom.test.ts.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { BLOG_HOSTNAMES } from './hubspot'
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

describe('the portal id', () => {
  it('AC1: the portal id is one literal in landing source', () => {
    const files = sourceFiles(HERE)
    expect(files.length, 'the scan read too few files').toBeGreaterThanOrEqual(20)

    const hits = files.flatMap((path) => {
      const count = readFileSync(path, 'utf8').split('148915098').length - 1
      return count > 0 ? [{ file: path.slice(HERE.length + 1), count }] : []
    })
    expect(hits).toEqual([{ file: 'hubspotChat.ts', count: 1 }])
  })
})

describe('shouldLoadChat', () => {
  it('AC2: shouldLoadChat truth table', () => {
    expect(shouldLoadChat('www.ascomply.com', true)).toBe(true)
    expect(shouldLoadChat('www.ascomply.com', false)).toBe(false)
    expect(shouldLoadChat('landing-pr-42.up.railway.app', true)).toBe(false)
    expect(shouldLoadChat('blog.ascomply.com', true, BLOG_HOSTNAMES)).toBe(true)
  })
})
