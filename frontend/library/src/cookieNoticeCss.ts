/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const START = 'Cookie consent notice.'
const END = '/* end: cookie consent notice */'

/** The landing's notice rules: from the banner comment to the end marker. */
export function sliceCookieNoticeCss(css: string): string {
  const banner = css.indexOf(START)
  if (banner === -1) throw new Error(`landing.css has no "${START}" banner: slice start missing`)
  const end = css.indexOf(END)
  if (end === -1) throw new Error(`landing.css has no "${END}" marker: slice end missing`)
  return css.slice(css.lastIndexOf('/*', banner), end)
}

export function cookieNoticeDefine(): Record<string, string> {
  const css = readFileSync(fileURLToPath(new URL('../../landing/src/styles/landing.css', import.meta.url)), 'utf8')
  return { __COOKIE_NOTICE_CSS__: JSON.stringify(sliceCookieNoticeCss(css)) }
}
