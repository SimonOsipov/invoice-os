/// <reference types="node" />
import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import type { Plugin } from 'vite'
import { NOT_FOUND_PAGE } from './route'

const PAGE_RE = /^(\/[a-z0-9-]+)+$/

// Each pattern must match exactly once, so a template edit fails the build, not the 404 page.
function replaceOnce(html: string, re: RegExp, repl: string, what: string): string {
  const n = html.match(new RegExp(re.source, 'g'))?.length ?? 0
  if (n !== 1) throw new Error(`notFoundHtml: expected exactly one ${what}, found ${n}`)
  return html.replace(re, () => repl)
}

export function notFoundHtml(indexHtml: string): string {
  let out = replaceOnce(indexHtml, /<html(?=[\s>])/, `<html data-page="${NOT_FOUND_PAGE}"`, '<html> tag')
  out = replaceOnce(out, /<title>[\s\S]*?<\/title>/, '<title>Page not found — ASComply Africa</title>', '<title>')
  return replaceOnce(out, /<\/head>/, '  <meta name="robots" content="noindex" />\n  </head>', '</head>')
}

/** Writes an index.html copy per real page (and 404.html) so the host answers 200 only for them. */
export function sitePages(pages: readonly string[], opts: { notFound: boolean }): Plugin {
  for (const p of pages) if (!PAGE_RE.test(p)) throw new Error(`sitePages: bad page path ${JSON.stringify(p)}`)
  if (new Set(pages).size !== pages.length) throw new Error('sitePages: duplicate page path')
  return {
    name: 'site-pages',
    apply: 'build',
    writeBundle(options) {
      const dir = options.dir
      if (!dir) throw new Error('sitePages: no output dir')
      const index = join(dir, 'index.html')
      if (!existsSync(index)) throw new Error(`sitePages: ${index} is missing`)
      for (const p of pages) {
        const dest = join(dir, p, 'index.html')
        mkdirSync(dirname(dest), { recursive: true })
        copyFileSync(index, dest)
      }
      if (opts.notFound) writeFileSync(join(dir, '404.html'), notFoundHtml(readFileSync(index, 'utf8')))
    },
  }
}
