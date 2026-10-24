// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { Blocks } from './components/Blocks'
import type { Block } from './data'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root

async function mount(blocks: Block[]) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<Blocks blocks={blocks} />))
}
afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

const URL_ = 'https://example.com/a'
const links = () => [...container.querySelectorAll('a')]

describe('post addresses render as links', () => {
  it('BUG-42 an address in p, ul item, note text and quote text is a new-tab link', async () => {
    await mount([
      { type: 'p', text: `see ${URL_}/p now` },
      { type: 'ul', items: [`label: ${URL_}/ul`] },
      { type: 'note', kind: 'regulation', title: 'T', text: `read ${URL_}/note` },
      { type: 'quote', text: `via ${URL_}/quote`, who: 'W' },
    ])
    const a = links()
    expect(a.map((x) => x.getAttribute('href'))).toEqual(['p', 'ul', 'note', 'quote'].map((s) => `${URL_}/${s}`))
    for (const x of a) {
      expect(x.textContent).toBe(x.getAttribute('href'))
      expect(x.getAttribute('target')).toBe('_blank')
      expect(x.getAttribute('rel')).toBe('noopener noreferrer')
    }
  })

  it('BUG-42 trailing dot and wrapping parens stay outside the link', async () => {
    await mount([
      { type: 'p', text: `see ${URL_}.` },
      { type: 'p', text: `(${URL_})` },
    ])
    expect(links().map((x) => x.getAttribute('href'))).toEqual([URL_, URL_])
    const ps = container.querySelectorAll('p')
    expect(ps[0].textContent).toBe(`see ${URL_}.`)
    expect(ps[1].textContent).toBe(`(${URL_})`)
  })

  it('BUG-42 a javascript: address stays plain text next to a real link', async () => {
    await mount([{ type: 'p', text: `javascript:alert(1) and ${URL_}` }])
    expect(links().map((x) => x.getAttribute('href'))).toEqual([URL_])
    expect(container.textContent).toContain('javascript:alert(1)')
  })

  it('BUG-42 markup beside an address shows as text', async () => {
    await mount([{ type: 'p', text: `<script>alert(1)</script> ${URL_}` }])
    expect(links().map((x) => x.getAttribute('href'))).toEqual([URL_])
    expect(container.querySelector('script')).toBeNull()
    expect(container.textContent).toContain('<script>alert(1)</script>')
  })

  it('BUG-42 an attribute-breaking quote, a balanced paren and an uppercase scheme', async () => {
    await mount([
      { type: 'p', text: `${URL_}"onmouseover="x HTTPS://E.com/W_(b).` },
      { type: 'ul', items: [`<${URL_}/l>`] },
    ])
    const a = links()
    expect(a.map((x) => x.getAttribute('href'))).toEqual([URL_, 'HTTPS://E.com/W_(b)', `${URL_}/l`])
    for (const x of a) expect(x.getAttributeNames().sort()).toEqual(['href', 'rel', 'style', 'target'])
    expect(container.querySelector('p')!.textContent).toBe(`${URL_}"onmouseover="x HTTPS://E.com/W_(b).`)
  })
})
