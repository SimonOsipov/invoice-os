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

describe('ActorCell', () => {
  it('actorCell_personIsRoundSystemIsSquare', () => {
    const person = actorAvatar('person')
    const system = actorAvatar('system')
    // Shape AND colour both differ, so the distinction survives greyscale.
    expect(person.borderRadius).not.toBe(system.borderRadius)
    expect(person.background).not.toBe(system.background)
    // The design pins both at 26px; a square that is not square would read as a circle.
    expect(person.width).toBe(26)
    expect(system.width).toBe(26)
    expect(system.borderRadius).not.toContain('50%')
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
})
