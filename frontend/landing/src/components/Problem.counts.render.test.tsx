// @vitest-environment jsdom
// vi.mock is file-wide, so the derivation check lives apart from Problem.render.test.tsx.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

import { Problem } from './Problem'

vi.mock('../data', async (importActual) => {
  const actual = await importActual<typeof import('../data')>()
  return { ...actual, PROBLEMS: actual.PROBLEMS.slice(2, 5) }
})

describe('PR-06 the footer counts derive from PROBLEMS', () => {
  it('counts the FAIL rows as errors and the rest as warnings over the rows it was given', () => {
    const tpl = document.createElement('template')
    tpl.innerHTML = renderToStaticMarkup(createElement(Problem))
    const rows = [...tpl.content.querySelectorAll('[data-check="row"]')]
    expect(rows.map((r) => r.children[1].textContent?.trim())).toEqual(['FAIL', 'WARN', 'WARN'])
    expect(tpl.content.querySelector('[data-check="footer"]')?.textContent?.trim()).toBe(
      '1 errors · 2 warnings · Not ready to submit',
    )
  })
})
