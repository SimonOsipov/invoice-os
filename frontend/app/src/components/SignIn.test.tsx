// SSR markup of the gates: the sign-in picker has no deployed oracle (no build renders it).
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import type { PersonaId } from '../auth'
import { SignIn, SignInLoading } from './SignIn'

const picker = (signingIn: PersonaId | null = null) => renderToStaticMarkup(<SignIn signingIn={signingIn} onPick={() => {}} />)

const decode = (s: string) => s.replace(/&gt;/g, '>').replace(/&lt;/g, '<').replace(/&quot;/g, '"').replace(/&#x27;/g, "'").replace(/&amp;/g, '&')

function declsOf(style: string): Map<string, string> {
  return new Map(style.split(';').filter(Boolean).map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()]))
}

// The one 452px card of a render; its count is the control that the lookup found something.
function cardDecls(html: string): Map<string, string> {
  const styles = [...html.matchAll(/style="([^"]*max-width:452px[^"]*)"/g)].map((m) => m[1])
  expect(styles, 'exactly one 452px card').toHaveLength(1)
  return declsOf(styles[0])
}

// Comments are stripped so a rule that is commented out reads as gone.
const styleText = (html: string) => decode(/<style>([\s\S]*?)<\/style>/.exec(html)?.[1] ?? '').replace(/\/\*[\s\S]*?\*\//g, '')

const GATES: Array<[string, () => string]> = [
  ['SignIn', () => picker()],
  ['SignIn mid sign-in', () => picker('firm')],
  ['SignInLoading', () => renderToStaticMarkup(<SignInLoading />)],
]

describe('GT-01 the sign-in and loading cards are 10px with no shadow', () => {
  it.each(GATES)('%s', (_name, render) => {
    const d = cardDecls(render())

    expect(d.get('background'), 'control: the card declarations are read').toBe('var(--bg-2)')
    expect(d.get('border-radius')).toBe('var(--radius-lg)')
    expect(d.has('box-shadow'), 'the card carries no shadow').toBe(false)
  })
})

describe('GT-02 the sign-in h1 takes the heading rule weight', () => {
  it('the one h1 declares its size and no inline weight', () => {
    const h1s = [...picker().matchAll(/<h1\b([^>]*)>/g)]
    expect(h1s, 'exactly one h1').toHaveLength(1)
    const d = declsOf(/style="([^"]*)"/.exec(h1s[0][1])?.[1] ?? '')

    expect(d.get('font-size'), 'control: the h1 style is read').toBe('20px')
    expect(d.has('font-weight'), 'an inline weight beats the v2 heading rule').toBe(false)
  })
})

describe('GT-03 nothing moves on press', () => {
  it('the picker styles hover only: no :active, no translate, no transform in the persona transition', () => {
    const css = styleText(picker())

    expect(css, 'control: the hover rule is read').toContain('.si-persona:not(:disabled):hover')
    expect(css).not.toContain(':active')
    expect(css).not.toContain('translate')

    const rule = /\.si-persona\s*\{([^}]*)\}/.exec(css)?.[1]
    expect(rule, 'the .si-persona rule exists').toBeDefined()
    const transition = declsOf(rule!).get('transition')
    expect(transition, 'control: the transition is read').toContain('border-color')
    expect(transition).not.toContain('transform')
  })

  it('the loading card styles no press state either (pin, green at write)', () => {
    const css = styleText(renderToStaticMarkup(<SignInLoading />))

    expect(css, 'control: the spinner rule is read').toContain('.si-spin')
    expect(css).not.toContain(':active')
    expect(css).not.toContain('translate')
  })
})

describe('GT-04 spinner and markup carry no v1 vocabulary', () => {
  it.each([
    ['SignIn mid sign-in', () => picker('firm')],
    ['SignInLoading', () => renderToStaticMarkup(<SignInLoading />)],
  ])('%s: a round spinner, no oklch, no 99px', (_name, render) => {
    const html = render()
    const spinners = [...html.matchAll(/<span class="si-spin" style="([^"]*)"/g)].map((m) => m[1])

    expect(spinners, 'the spinner renders').toHaveLength(1)
    expect(declsOf(spinners[0]).get('border-radius')).toBe('50%')
    expect(html).not.toContain('oklch')
    expect(html).not.toContain('99px')
  })
})
