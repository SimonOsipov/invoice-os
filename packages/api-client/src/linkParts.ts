export type LinkPart = { text: string; href?: string }

const ADDRESS = /https?:\/\/[^\s<>"]+/gi
const SCHEME = /^https?:\/\//i

function count(s: string, c: string): number {
  return s.split(c).length - 1
}

// Trailing sentence punctuation and an unmatched ")" stay outside the link.
function trimAddress(a: string): string {
  for (;;) {
    const last = a[a.length - 1]
    if (/[.,;:!?]/.test(last) || (last === ')' && count(a, ')') > count(a, '('))) a = a.slice(0, -1)
    else return a
  }
}

/** Splits post text into plain and http(s) address parts. Only bare addresses; mirrors internal/blogweb linkParts. */
export function linkParts(s: string): LinkPart[] {
  const out: LinkPart[] = []
  let at = 0
  for (const m of s.matchAll(ADDRESS)) {
    const a = trimAddress(m[0])
    if (a.length <= (SCHEME.exec(a)?.[0].length ?? a.length)) continue
    if (m.index > at) out.push({ text: s.slice(at, m.index) })
    out.push({ text: a, href: a })
    at = m.index + a.length
  }
  if (at < s.length) out.push({ text: s.slice(at) })
  return out
}
