// RED specs (task-304, INVCR-01-19, Test-first) — pin resolveActiveClient's contract
// before the executor implements its body. AC-2/AC-3 in the story ("App.tsx's `active`
// memo no longer special-cases mode === 'inhouse'... If an in-house tenant has exactly
// one entity it is selected deterministically") land here as unit facts, since App.tsx
// itself is unrenderable under this suite's node environment (no jsdom) — see
// resolveActiveClient's own doc comment in lib/clients.ts for the full rationale.
//
// Every spec below currently fails because resolveActiveClient's stub body throws
// `new Error('not implemented')` before ever returning anything — that IS the correct
// RED reason (assertion / thrown-error mismatch), not an import/compile error.
import { describe, expect, it } from 'vitest'

import type { AsyncStatus } from '@invoice-os/api-client'

import { emptyClient, firstRunSurface, resolveActiveClient } from './clients'
import { computeNoEntity } from './importFlow'
import type { Entity } from './portfolio'
import type { Client } from '../types'

// A minimal, distinguishable Client fixture built off the real emptyClient() output
// (never hand-rolled field-by-field) so a future Client field addition can't silently
// leave these fixtures type-invalid or drifted from the real shape. Only entityId/short/
// name vary per test — nothing about resolveActiveClient reads any other field.
function mkClient(entityId: string, short = entityId): Client {
  return { ...emptyClient(), entityId, short, name: short, onboarding: false }
}

describe('resolveActiveClient (RAC-1..7, task-304 AC-2/AC-3)', () => {
  // RAC-1 — the keystone case this story exists to fix: in-house's normal shape once it
  // owns its one seeded entity (db/seed.dev.sql, AC-1). Falsification: an impl that still
  // special-cases on some external "mode" signal (there is none in this signature) and
  // returns a null-entityId placeholder regardless of `clients`.
  it('RAC-1: with nothing explicitly selected, a workspace with exactly one entity resolves to THAT entity', () => {
    const only = mkClient('e-honeywell', 'Honeywell')
    const result = resolveActiveClient([only], null)
    expect(result.entityId).toBe('e-honeywell')
    expect(result).toBe(only)
  })

  // RAC-2 — an explicit id match wins regardless of array position. Falsification: an
  // impl that reads `clients[0]` whenever `activeEntityId` happens to equal the first
  // element's id (would pass a naive single-position check) but breaks the moment the
  // match sits elsewhere — the mutation guard for "entity selection becomes order-
  // dependent".
  it('RAC-2: an explicit activeEntityId match wins regardless of its position in the array', () => {
    const a = mkClient('e-a')
    const b = mkClient('e-b')
    const c = mkClient('e-c')
    expect(resolveActiveClient([a, b, c], 'e-b')).toBe(b)
    expect(resolveActiveClient([c, b, a], 'e-b')).toBe(b)
    expect(resolveActiveClient([b, a, c], 'e-b')).toBe(b)
  })

  // RAC-3 — the AC-3 bootstrap window: a workspace (either persona) with genuinely zero
  // entities and nothing to select. Falsification: a thrown error, `undefined`, or a
  // persona-flavoured placeholder distinct from the one firm already used for this same
  // honest "nothing to file against" case.
  it('RAC-3: zero entities and nothing selected resolves to the shared emptyClient() placeholder — never throws, never undefined', () => {
    const result = resolveActiveClient([], null)
    expect(result).toEqual(emptyClient())
    expect(result.entityId).toBeNull()
    expect(result.onboarding).toBe(true)
  })

  // RAC-4 — a stale/cleared id (names an entity not in the current list, e.g. between a
  // refetch landing and the next render) falls back to the server's own first row, same
  // as the pre-existing firm behaviour — never throws, never silently strands the caller
  // on `undefined`.
  it('RAC-4: an activeEntityId naming an entity NOT in the list falls back to the first client', () => {
    const a = mkClient('e-a')
    const b = mkClient('e-b')
    expect(resolveActiveClient([a, b], 'e-stale')).toBe(a)
  })

  // RAC-5 — no explicit selection AND more than one entity: falls back to the server's
  // own (stable, `name ASC, id ASC` — portfolio store.go's List) order, i.e. index 0.
  // This is the pre-existing firm default, unified onto the one shared path rather than
  // special-cased away.
  it('RAC-5: no explicit selection, multiple entities — falls back to the first (server-ordered) client', () => {
    const a = mkClient('e-a')
    const b = mkClient('e-b')
    expect(resolveActiveClient([a, b], null)).toBe(a)
  })

  // RAC-6 — positive companion to RAC-3: totality over every array length, not just the
  // zero/one cases the other specs already pin.
  it('RAC-6: never returns undefined for any clients-array length', () => {
    expect(resolveActiveClient([], null)).toBeDefined()
    expect(resolveActiveClient([mkClient('e-a')], null)).toBeDefined()
    expect(resolveActiveClient([mkClient('e-a'), mkClient('e-b')], 'e-b')).toBeDefined()
  })

  // AC-2 regression guard, mirroring importFlow.test.ts's own "1-arg SIGNATURE itself"
  // idiom (FLOW-12's own comment): pins the 2-arg signature so a REGRESSED 3-arg call
  // (e.g. a reintroduced `mode` parameter, exactly the special-case this story deletes)
  // does not compile. Verified against a local stub both directions before landing this:
  // a 3-arg call against a 2-arg fn is a real TS2554 the directive suppresses; the 2-arg
  // calls in every other spec above need no directive.
  it('AC-2: resolveActiveClient is 2-arg only — a stale 3-arg (mode-carrying) call does not compile', () => {
    // @ts-expect-error — regression guard: a reintroduced third argument (e.g. `mode`)
    // would restore exactly the persona special-case task-304 AC-2 deletes.
    const threeArg = resolveActiveClient([], null, 'inhouse')
    expect(threeArg).toBeDefined()
  })
})

// AUTH-10-03: which surface the dashboard shows while a workspace has no resolved entity.
describe('firstRunSurface (AUTH-10-03)', () => {
  const ENTITY: Entity = {
    id: 'e1',
    name: 'Lagos Freight',
    tin: '20184412-0001',
    registration: null,
    sector: null,
    address: null,
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
  }

  it.each([
    ['settled empty', null, 'empty', 0, 0, 'task'],
    ['no-gateway build', null, 'idle', 0, 0, 'task'],
    ['ready with zero entities', null, 'ready', 0, 0, 'task'],
    ['roster caught up, nothing resolved', null, 'ready', 2, 2, 'task'],
    ['still loading', null, 'loading', 0, 0, 'loading'],
    ['one-render roster catch-up', null, 'ready', 2, 0, 'loading'],
    ['an entity resolved', ENTITY, 'ready', 1, 1, 'loading'],
    ['fetch failed', null, 'error', 0, 0, 'error'],
  ] as const)('%s', (_label, entity, state, entitiesCount, clientsCount, want) => {
    expect(firstRunSurface(entity, state, entitiesCount, clientsCount)).toBe(want)
  })

  it('answers task whenever computeNoEntity does, so the dashboard and the amber panel agree', () => {
    const states: AsyncStatus[] = ['idle', 'loading', 'error', 'empty', 'ready']
    let noEntityCases = 0
    for (const state of states) {
      for (const entity of [null, ENTITY]) {
        for (const [entitiesCount, clientsCount] of [[0, 0], [2, 0], [2, 2], [1, 1]]) {
          if (computeNoEntity(entity, state, entitiesCount, clientsCount)) {
            noEntityCases++
            expect(firstRunSurface(entity, state, entitiesCount, clientsCount)).toBe('task')
          }
        }
      }
    }
    expect(noEntityCases).toBeGreaterThan(0)
  })
})

describe('emptyClient', () => {
  it('emptyClient reads as an empty company, not a missing client', () => {
    const c = emptyClient()
    expect(c.name).toBe('No company yet')
    expect(c.short).toBe('your company')
    expect(c.entityId).toBeNull()
    expect(c.onboarding).toBe(true)
    expect(c.tin).toBe('—')
  })
})
