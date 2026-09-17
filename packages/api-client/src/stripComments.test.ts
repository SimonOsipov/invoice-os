import { describe, expect, it } from 'vitest'

import { stripComments } from './stripComments'

describe('stripComments', () => {
  it('removes a whole-line and a trailing line comment', () => {
    expect(stripComments('// header\nconst a = 1 // trailing\nconst b = 2')).toBe('\nconst a = 1 \nconst b = 2')
  })

  it('removes a line comment on the last line with no newline', () => {
    expect(stripComments('x() // end')).toBe('x() ')
  })

  it('removes a one-line block comment and a JSX comment', () => {
    expect(stripComments('a /* note */ b')).toBe('a  b')
    expect(stripComments('<div>{/* Download original */}</div>')).toBe('<div>{}</div>')
  })

  it('removes a multi-line block comment and keeps its newlines', () => {
    expect(stripComments('/**\n * doc\n */\nexport const x = 1')).toBe('\n\n\nexport const x = 1')
  })

  it('keeps comment markers inside single, double and template strings', () => {
    const src = `const a = '//'\nconst b = "/* not */"\nconst c = \`// \${'x'} /*\`\n`
    expect(stripComments(src)).toBe(src)
  })

  it('keeps a URL inside a string and strips the comment after it', () => {
    expect(stripComments("fetch('https://example.com/a') // call")).toBe("fetch('https://example.com/a') ")
  })

  it('keeps an escaped quote inside a string', () => {
    expect(stripComments("const s = 'it\\'s // here' // gone")).toBe("const s = 'it\\'s // here' ")
  })

  it('ends an unterminated single-quoted string at the newline', () => {
    expect(stripComments("a = 'x\nb // gone")).toBe("a = 'x\nb ")
  })

  it('keeps an escaped slash pair in a regex literal', () => {
    const src = 'const re = /\\/\\*[\\s\\S]*?\\*\\//g'
    expect(stripComments(src)).toBe(src)
  })

  it('removes a comment that quotes a needle', () => {
    expect(stripComments("function f() {\n  // the old arm said 'Waiting'\n  return 1\n}")).toBe(
      'function f() {\n  \n  return 1\n}',
    )
  })
})
