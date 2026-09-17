/**
 * Removes `//` and `/* *\/` comments from JS/TS source so a source-scanning test reads code,
 * not prose about code. Quoted strings and template literals are kept whole, so `'//'` and
 * `https://` survive. A block comment keeps its newlines, so line-based scans stay aligned.
 */
// ceiling: regex literals are not recognised; an unescaped quote inside one opens a string to end of line
export function stripComments(src: string): string {
  let out = ''
  let quote = ''
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (c === '\\') {
      out += src.slice(i, i + 2)
      i++
    } else if (quote) {
      out += c
      if (c === quote || (c === '\n' && quote !== '`')) quote = ''
    } else if (c === '/' && src[i + 1] === '/') {
      const end = src.indexOf('\n', i)
      i = (end === -1 ? src.length : end) - 1
    } else if (c === '/' && src[i + 1] === '*') {
      const end = src.indexOf('*/', i + 2)
      const stop = end === -1 ? src.length : end + 2
      out += src.slice(i, stop).replace(/[^\n]/g, '')
      i = stop - 1
    } else {
      if (c === "'" || c === '"' || c === '`') quote = c
      out += c
    }
  }
  return out
}
