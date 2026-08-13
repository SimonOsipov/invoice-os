// Node-environment companion to the policy specs in lib/. Two things this subtask changes
// have no runtime oracle anywhere else:
//
//  - The ctx contract. All 22 fake-PlatformCtx sites end in `as unknown as PlatformCtx`,
//    which disables property checking, so adding fields breaks NOTHING at compile time. The
//    `Pick` below is typed straight, no cast, so a missing field is a tsc error here.
//  - App.tsx's wiring. Workspace cannot mount in jsdom (it needs a session and a live
//    entities fetch), so the source is the only place these facts are observable.
//
// Test files are excluded from the source walk: this one carries the forbidden strings as
// fixtures and would otherwise match itself.

import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { newPolicy, type Policy } from './lib/workflows'
import type { PlatformCtx } from './types'

const SRC = fileURLToPath(new URL('.', import.meta.url))
const APP = readFileSync(join(SRC, 'App.tsx'), 'utf8')
const TYPES = readFileSync(join(SRC, 'types.ts'), 'utf8')

// The `Pick<PlatformCtx, …>` intermediate the repo already uses for fakes (Header.test.tsx:21):
// typing against the real ctx keeps a rename — or an absent field — from silently passing.
type PolicyCtx = Pick<
  PlatformCtx,
  'policies' | 'policiesState' | 'policiesError' | 'refetchPolicies' | 'createPolicy' | 'deletePolicy' | 'savePolicy' | 'publishPolicy' | 'openPolicy' | 'closePolicy'
>

const policyCtx = (over: Partial<PolicyCtx> = {}): PolicyCtx => ({
  policies: [],
  policiesState: 'ready',
  policiesError: null,
  refetchPolicies: vi.fn(),
  createPolicy: vi.fn(async () => {}),
  deletePolicy: vi.fn(async (_id: string) => {}),
  savePolicy: vi.fn(async (next: Policy) => next),
  publishPolicy: vi.fn(async (_id: string) => newPolicy()),
  openPolicy: vi.fn(),
  closePolicy: vi.fn(),
  ...over,
})

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...sourceFiles(path))
    else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(path)
  }
  return out
}

describe('the policies ctx contract (§4.4)', () => {
  it('carries the async triple beside the list, so a reader never branches on length', () => {
    const boom = new ApiError('http', 'only an admin can change approval policies', 403)
    const ctx = policyCtx({ policies: [], policiesState: 'error', policiesError: boom })

    expect(ctx.policiesState).toBe('error')
    expect(ctx.policiesError).toBe(boom)
    // An error resolves `data` to null, which an emptiness check would render as a tenant
    // holding no policies — the reading a user acts on.
    expect(ctx.policies).toEqual([])

    ctx.refetchPolicies()
    expect(ctx.refetchPolicies).toHaveBeenCalledTimes(1)
  })

  it('the four write verbs answer promises, so a caller can await the server', async () => {
    const saved = newPolicy()
    const published = { ...newPolicy(), status: 'published' as const, activeVersion: 1 }
    const ctx = policyCtx({ savePolicy: vi.fn(async () => saved), publishPolicy: vi.fn(async () => published) })

    // The annotations are the assertion: a `void` verb fails to typecheck on these lines.
    const created: Promise<void> = ctx.createPolicy()
    const removed: Promise<void> = ctx.deletePolicy('p1')

    await expect(created).resolves.toBeUndefined()
    await expect(removed).resolves.toBeUndefined()
    await expect(ctx.savePolicy(saved)).resolves.toBe(saved)
    await expect(ctx.publishPolicy('p1')).resolves.toBe(published)
  })
})

describe('App.tsx wiring', () => {
  // Asserted on booleans, not on the source string: a `.toContain` failure prints all 67 KB
  // of App.tsx and buries the reason.
  it('holds no policy seed and no per-mode store (AC-1)', () => {
    // Controls: the file really was read, and the idiom the seed is replaced by is present.
    expect(APP.length).toBeGreaterThan(40_000)
    expect(APP.includes('useAsync'), 'App.tsx no longer uses useAsync at all').toBe(true)
    expect(APP.includes('listApprovalPolicies'), 'App.tsx never fetches the policy list').toBe(true)

    for (const gone of ['seedPolicies', 'PolicyStore', 'setPolicyStore', 'updatePolicies']) {
      expect(APP.includes(gone), `App.tsx still names ${gone}`).toBe(false)
    }
  })

  it('never blanks the policies mirror during a refetch', () => {
    const lines = APP.split('\n')
    const writes = lines.map((line, i) => ({ line, at: i + 1 })).filter((l) => l.line.includes('setPolicies('))
    expect(writes.length, 'App.tsx holds no policies mirror at all').toBeGreaterThan(0)

    // `asyncReducer`'s `start` nulls `data`, so `?? []` empties the list for the round trip —
    // the exact flash the mirror exists to prevent. Members and roles never hit it because
    // they only ever patch; publish is the one verb that refetches.
    for (const w of writes) expect(w.line, `App.tsx:${w.at} blanks the mirror`).not.toContain('?? []')

    const fromAsync = writes.filter((w) => w.line.includes('.data'))
    expect(fromAsync.length, 'no mirror effect reads the async data').toBeGreaterThan(0)
    for (const w of fromAsync) {
      const window = lines.slice(Math.max(0, w.at - 3), w.at).join('\n')
      expect(window, `App.tsx:${w.at} writes the mirror ungated`).toMatch(/if\s*\(/)
    }
  })

  it('refetches the whole list on publish rather than patching one row (AC-2)', () => {
    // The active slot is tenant-wide, so publishing B deactivates A and publish's own
    // response describes B alone (lib/policies.test.ts pins that half). The observable
    // effect on a live tenant is subtask 07's topology spec; this pins the call.
    const sites: number[] = []
    for (let at = APP.indexOf('publishApprovalPolicy('); at !== -1; at = APP.indexOf('publishApprovalPolicy(', at + 1)) sites.push(at)
    expect(sites.length, 'App.tsx never calls publishApprovalPolicy').toBeGreaterThan(0)

    const refetching = sites.filter((at) => /refetch|\.run\(/.test(APP.slice(at, at + 600)))
    expect(refetching.length, 'no publish call site is followed by a refetch').toBeGreaterThan(0)
  })
})

describe('the deleted per-mode store leaves no comment behind (AC-7)', () => {
  const NEEDLES = ['PolicyStore', 'per-mode', 'firm/inhouse', 'keyed firm']

  it('names the store only in lib/policies.fixture.ts', () => {
    const files = sourceFiles(SRC)
    expect(files.length, 'the source walk found nothing to scan').toBeGreaterThanOrEqual(20)

    // Control needle: the fixture keeps the type, so a walk that stopped matching would
    // fail here rather than reporting a clean tree.
    const fixture = files.filter((f) => f.endsWith('policies.fixture.ts'))
    expect(fixture).toHaveLength(1)
    expect(readFileSync(fixture[0], 'utf8')).toContain('PolicyStore')

    for (const file of files) {
      if (file.endsWith('policies.fixture.ts')) continue
      const src = readFileSync(file, 'utf8')
      for (const needle of NEEDLES) expect(src.includes(needle), `${file} names "${needle}"`).toBe(false)
    }
  })

  // The needles above cannot reach this one: the block spells it "Per mode" and
  // "firm/in-house", and the second string is also legitimate prose in Sidebar.tsx:3.
  it('the ctx block no longer calls the policy set per mode', () => {
    expect(TYPES.includes('policies: Policy[]'), 'types.ts no longer declares the policy list').toBe(true)
    expect(TYPES.includes('Per mode, not per client'), 'types.ts:307 still calls the policy set per mode').toBe(false)
  })

  it('Sidebar keeps NAV_WORKFLOWS in the FIRM-WIDE group — the conclusion the comment repair must not move', () => {
    const src = readFileSync(join(SRC, 'components', 'Sidebar.tsx'), 'utf8')
    expect(src).toContain("scope: 'FIRM-WIDE', items: [NAV_WORKFLOWS, NAV_CLIENTS, NAV_SETTINGS]")
  })
})
