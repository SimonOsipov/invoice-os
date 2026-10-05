// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { ActorCell, actorAvatar } from './ActorCell'

// The three actor shapes audit_log actually stores: a GoTrue subject uuid resolved to a
// person, the literal "system", and free text such as backfill-source-rows.
const PERSON = { actor: 'c0000000-0000-0000-0000-000000000001', actor_name: 'Chinedu Okafor', actor_kind: 'person' }
const SYSTEM = { actor: 'system', actor_name: 'System', actor_kind: 'system' }
const RAW = { actor: 'backfill-source-rows', actor_name: 'backfill-source-rows', actor_kind: 'raw' }

afterEach(cleanup)

// Raw style attribute, not .style.*: jsdom's CSSStyleDeclaration can drop var() shorthands.
function styleValue(el: Element, prop: string): string | null {
  const style = el.getAttribute('style') ?? ''
  const match = style.match(new RegExp(`(?:^|;\\s*)${prop}:\\s*([^;]+)`))
  return match ? match[1].trim() : null
}

describe('ActorCell', () => {
  it('actorCell_personAndSystemShareTheRoundAvatar', () => {
    const person = actorAvatar('person')
    const system = actorAvatar('system')
    // Round and same fill for both -- the glyph colour tells person and System apart.
    expect(person.borderRadius).toBe('50%')
    expect(system.borderRadius).toBe('50%')
    expect(person.background).toBe('var(--bg-3)')
    expect(system.background).toBe('var(--bg-3)')
    expect(person.color).toBe('var(--fg-2)')
    expect(system.color).toBe('var(--fg-3)')
    // The design pins both at 26px.
    expect(person.width).toBe(26)
    expect(person.height).toBe(26)
    expect(system.width).toBe(26)
    expect(system.height).toBe(26)
  })

  it('actorCell_systemKeepsBoltAndFillPersonKeepsInitials', () => {
    const { unmount } = render(<ActorCell {...SYSTEM} />)
    const systemAvatar = screen.getByTestId('actor-bolt').parentElement as HTMLElement
    expect(screen.queryByTestId('actor-initials')).toBeNull()
    expect(styleValue(systemAvatar, 'background')).toBe('var(--bg-3)')
    unmount()

    render(<ActorCell {...PERSON} />)
    expect(screen.getByTestId('actor-initials').textContent).toBe('CO')
    const personAvatar = screen.getByTestId('actor-initials').parentElement as HTMLElement
    expect(styleValue(personAvatar, 'background')).toBe('var(--bg-3)')
  })

  it('actorCell_freeTextAvatarIsUnchanged', () => {
    expect(actorAvatar('raw')).toEqual({
      width: 26,
      height: 26,
      borderRadius: 'var(--radius-xs)',
      background: 'transparent',
      color: 'var(--fg-3)',
    })
  })

  it('actorCell_renderedSystemAndPersonAvatarsShareCornerAndSize', () => {
    const props = ['border-radius', 'width', 'height', 'color']
    const { unmount } = render(<ActorCell {...SYSTEM} />)
    const systemAvatar = screen.getByTestId('actor-bolt').parentElement as HTMLElement
    const system = props.map((p) => styleValue(systemAvatar, p))
    unmount()

    render(<ActorCell {...PERSON} />)
    const personAvatar = screen.getByTestId('actor-initials').parentElement as HTMLElement
    const person = props.map((p) => styleValue(personAvatar, p))

    // Reads the rendered span: an inline key after the actorAvatar spread would override it.
    expect(system).toEqual(['50%', '26px', '26px', 'var(--fg-3)'])
    expect(person).toEqual(['50%', '26px', '26px', 'var(--fg-2)'])
  })

  it('actorCell_variantSetsGapNameSizeAndWeight', () => {
    const want = { audit: { gap: '9px', size: '13px', weight: '500' }, activity: { gap: '8px', size: '12.5px', weight: '600' } } as const
    // Default (no prop) is the audit density.
    const { container, unmount } = render(<ActorCell {...PERSON} />)
    const def = container.firstElementChild as HTMLElement
    expect(styleValue(def, 'gap')).toBe(want.audit.gap)
    unmount()
    for (const v of ['audit', 'activity'] as const) {
      const { container: c, unmount: u } = render(<ActorCell {...PERSON} variant={v} />)
      const root = c.firstElementChild as HTMLElement
      const name = root.lastElementChild as HTMLElement
      expect(name.textContent).toBe('Chinedu Okafor')
      expect(styleValue(root, 'gap'), v).toBe(want[v].gap)
      expect(styleValue(name, 'font-size'), v).toBe(want[v].size)
      expect(styleValue(name, 'font-weight'), v).toBe(want[v].weight)
      u()
    }
  })

  it('actorCell_initialsAreMono95Bold', () => {
    render(<ActorCell {...PERSON} />)
    const avatar = screen.getByTestId('actor-initials').parentElement as HTMLElement
    expect(styleValue(avatar, 'font-family')).toContain('--font-mono')
    expect(styleValue(avatar, 'font-size')).toBe('9.5px')
    expect(styleValue(avatar, 'font-weight')).toBe('700')
  })

  it('actorCell_freeTextActorIsNotAPerson', () => {
    render(<ActorCell {...RAW} />)
    expect(screen.getByText('backfill-source-rows')).toBeTruthy()
    // No initials bubble: a free-text process is not a person and must not borrow the
    // person treatment.
    expect(screen.queryByTestId('actor-initials')).toBeNull()
    expect(actorAvatar('raw').borderRadius).not.toBe(actorAvatar('person').borderRadius)
  })

  it('actorCell_personRendersNameNotUuid', () => {
    render(<ActorCell {...PERSON} />)
    expect(screen.getByText('Chinedu Okafor')).toBeTruthy()
    expect(screen.queryByText(PERSON.actor)).toBeNull()
    expect(screen.getByTestId('actor-initials').textContent).toBe('CO')
  })

  it('actorCell_systemRendersProcessNameInMono', () => {
    render(<ActorCell {...SYSTEM} />)
    expect(screen.getByText('System')).toBeTruthy()
    expect(screen.getByTestId('actor-bolt')).toBeTruthy()
  })

  it('actorCell_alwaysPassesResolvedPair', () => {
    // The APP_PERSONAS fall-through in lib/actor.ts holds BOTH tenants' subjects unscoped,
    // so a cell that omits the resolved pair can name another tenant's admin. Rendering a
    // subject that IS in that table with a server answer of "Someone Else" proves the
    // fall-through never runs.
    render(<ActorCell actor={PERSON.actor} actor_name="Someone Else" actor_kind="person" />)
    expect(screen.getByText('Someone Else')).toBeTruthy()
    expect(screen.queryByText(/Okafor/)).toBeNull()
  })

describe('ActorCell initials tracking', () => {
  it('actorCell_initialsCarryTheMonoClass', () => {
    render(<ActorCell {...PERSON} />)
    expect(screen.getByTestId('actor-initials').parentElement!.className).toBe('mono')
  })
})
})
