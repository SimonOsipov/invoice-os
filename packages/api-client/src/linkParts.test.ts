import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { linkParts } from './linkParts'

const U = 'https://example.com/a'

describe('linkParts', () => {
  it.each([
    ['plain text', [{ text: 'plain text' }]],
    [`see ${U}.`, [{ text: 'see ' }, { text: U, href: U }, { text: '.' }]],
    [`(${U})`, [{ text: '(' }, { text: U, href: U }, { text: ')' }]],
    [`${U}?!,;:`, [{ text: U, href: U }, { text: '?!,;:' }]],
    ['https://en.wikipedia.org/wiki/A_(b)', [{ text: 'https://en.wikipedia.org/wiki/A_(b)', href: 'https://en.wikipedia.org/wiki/A_(b)' }]],
    ['javascript:alert(1) data:x mailto:a@b.c', [{ text: 'javascript:alert(1) data:x mailto:a@b.c' }]],
    ['https:// alone', [{ text: 'https:// alone' }]],
    [`a ${U} b http://x.io/y`, [{ text: 'a ' }, { text: U, href: U }, { text: ' b ' }, { text: 'http://x.io/y', href: 'http://x.io/y' }]],
    ['<script> ' + U, [{ text: '<script> ' }, { text: U, href: U }]],
    ['', []],
  ])('%j', (s, want) => expect(linkParts(s)).toEqual(want))
})

// Also read by internal/blogweb/linkparts_test.go; the Go and TypeScript splitters must agree.
const shared = JSON.parse(
  readFileSync(fileURLToPath(new URL('../../../internal/blogweb/testdata/link_parts.json', import.meta.url)), 'utf8'),
) as { name: string; in: string; parts: { text: string; href?: string }[] }[]

describe('linkParts shared case table', () => {
  it('has the cases', () => expect(shared.length).toBeGreaterThanOrEqual(30))
  it.each(shared.map((c) => [c.name, c] as const))('%s', (_n, c) => expect(linkParts(c.in)).toEqual(c.parts))
})
