import { describe, expect, it } from 'vitest'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { notFoundHtml, sitePages } from './sitePages'

const INDEX = readFileSync(fileURLToPath(new URL('../index.html', import.meta.url)), 'utf8')

const run = (pages: string[], notFound: boolean, withIndex = true) => {
  const dir = mkdtempSync(join(tmpdir(), 'site-pages-'))
  if (withIndex) writeFileSync(join(dir, 'index.html'), INDEX)
  const plugin = sitePages(pages, { notFound })
  const hook = plugin.writeBundle as unknown as { handler?: Function } | Function
  const fn = (typeof hook === 'function' ? hook : hook.handler!) as (o: { dir: string }) => void
  fn.call({}, { dir })
  return dir
}

describe('sitePages', () => {
  it('sitePages_writesOneCopyPerPage', () => {
    const dir = run(['/privacy', '/invite'], true)
    for (const p of ['privacy', 'invite']) expect(readFileSync(join(dir, p, 'index.html'), 'utf8')).toBe(INDEX)
    expect(existsSync(join(dir, '404.html'))).toBe(true)
  })
  it('sitePages_writesNoNotFoundWhenOff', () => {
    expect(existsSync(join(run(['/privacy'], false), '404.html'))).toBe(false)
  })
  it('sitePages_nestedPage', () => {
    expect(existsSync(join(run(['/invoices/import-files'], false), 'invoices/import-files/index.html'))).toBe(true)
  })
  it('sitePages_rejectsBadPaths', () => {
    for (const p of ['', 'privacy', '/Privacy', '/a//b', '/a/', '/../x']) {
      expect(() => sitePages([p], { notFound: false }), p).toThrow()
    }
  })
  it('sitePages_rejectsDuplicates', () => {
    expect(() => sitePages(['/privacy', '/privacy'], { notFound: false })).toThrow()
  })
  it('sitePages_missingIndexThrows', () => {
    expect(() => run(['/privacy'], false, false)).toThrow(/index\.html/)
  })
})

describe('notFoundHtml', () => {
  it('notFoundHtml_marksTitlesAndNoindexes', () => {
    const out = notFoundHtml(INDEX)
    expect(out.match(/<html data-page="not-found"/g)).toHaveLength(1)
    expect(out).toContain('<title>Page not found — ASComply Africa</title>')
    expect(out.match(/<meta name="robots" content="noindex" \/>/g)).toHaveLength(1)
    expect(out.indexOf('name="robots"')).toBeLessThan(out.indexOf('</head>'))
    const urls = (h: string) => [...h.matchAll(/(?:src|href)="([^"]*)"/g)].map((m) => m[1])
    expect(urls(out)).toEqual(urls(INDEX))
  })
  it('notFoundHtml_throwsOnZeroOrTwoMatches', () => {
    expect(() => notFoundHtml(INDEX.replace('</head>', ''))).toThrow()
    expect(() => notFoundHtml(INDEX.replace('</head>', '<title>x</title></head>'))).toThrow()
    expect(() => notFoundHtml(INDEX.replace('<html', '<div'))).toThrow()
  })
})
