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

import { ApiError, asyncReducer, initialState } from '@invoice-os/api-client'

import { membersViewState } from './lib/members'
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

  // QA (task-507). The `await` specs below CANNOT see a verb degraded to `=> void`: TS
  // accepts any return type where `void` is expected, and `expect(x).resolves` typechecks
  // against a non-promise. Measured — retyping `savePolicy`/`publishPolicy` as `=> void`
  // left 2026 tests green and tsc clean. These four lines are the only thing that fails.
  type Exact<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false
  const _createResolvesNothing: Exact<ReturnType<PlatformCtx['createPolicy']>, Promise<void>> = true
  const _deleteResolvesNothing: Exact<ReturnType<PlatformCtx['deletePolicy']>, Promise<void>> = true
  const _saveResolvesTheServerRow: Exact<ReturnType<PlatformCtx['savePolicy']>, Promise<Policy>> = true
  const _publishResolvesTheServerRow: Exact<ReturnType<PlatformCtx['publishPolicy']>, Promise<Policy>> = true

  it('every write verb resolves the type its caller has to read', () => {
    // `true` is unreachable unless each Exact above resolved to `true` — a `=> void` verb
    // makes the declaration itself a tsc error, and this asserts the constants are live
    // rather than tree-shaken away unread.
    expect([_createResolvesNothing, _deleteResolvesNothing, _saveResolvesTheServerRow, _publishResolvesTheServerRow]).toEqual([true, true, true, true])
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

/** The source text of `async function NAME(…) {…}` in App.tsx, brace-matched. */
function asyncFnBody(name: string): string {
  const at = APP.indexOf(`async function ${name}(`)
  expect(at, `App.tsx declares no async function ${name}`).toBeGreaterThan(-1)
  const open = APP.indexOf('{', APP.indexOf(')', at))
  let depth = 0
  for (let i = open; i < APP.length; i++) {
    if (APP[i] === '{') depth++
    else if (APP[i] === '}' && --depth === 0) return APP.slice(open + 1, i)
  }
  throw new Error(`unbalanced braces reading ${name}`)
}

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

  // QA (task-507). AC-1 says the mirror is patched off the SERVER's returned row. Nothing
  // pinned the ORDER: the harnesses in lib/policies.test.ts replicate App.tsx's composition
  // rather than reading it, so moving `setPolicies` above the `await` — an optimistic patch
  // that shows the caller's own stale tree, step ids and all — left 2026 tests green.
  it('every write patches the mirror only AFTER the server has answered', () => {
    const verbs = [
      { name: 'createPolicy', wire: 'createApprovalPolicy(', binds: 'created' },
      { name: 'deletePolicy', wire: 'deleteApprovalPolicy(', binds: null },
      { name: 'savePolicy', wire: 'putApprovalPolicyDraft(', binds: 'saved' },
    ] as const

    for (const verb of verbs) {
      const body = asyncFnBody(verb.name)
      const awaitAt = body.indexOf(`await ${verb.wire}`)
      expect(awaitAt, `${verb.name} never awaits ${verb.wire}`).toBeGreaterThan(-1)

      const writes: number[] = []
      for (let at = body.indexOf('setPolicies('); at !== -1; at = body.indexOf('setPolicies(', at + 1)) writes.push(at)
      expect(writes.length, `${verb.name} never patches the mirror`).toBe(1)
      expect(writes[0], `${verb.name} patches the mirror before the server answers`).toBeGreaterThan(awaitAt)

      // The DELETE response is inert, so `deletePolicy` binds nothing and drops the id.
      if (verb.binds) expect(body.slice(writes[0]), `${verb.name} patches from something other than the server's row`).toContain(verb.binds)
    }
  })

  // QA (task-507). Dropping the guard for `listApprovalPolicies(authedFetch, base!)` left
  // 2026 tests green and tsc clean, and would send a request to `null/api/…` the first time
  // subtask 04's Retry fires on a workspace with no gateway.
  it('the policies producer refuses a null gateway, exactly as the members one does', () => {
    const producerOf = (fn: string) => {
      const at = APP.indexOf(`${fn}(authedFetch, base)`)
      expect(at, `App.tsx never calls ${fn}(authedFetch, base)`).toBeGreaterThan(-1)
      // Whitespace-collapsed: prettier wraps the members producer's ternary across lines
      // and leaves the shorter policies one inline.
      return APP.slice(Math.max(0, at - 300), at).replace(/\s+/g, ' ')
    }

    // Control: the members producer is the shipped idiom this one copies. If the assertion
    // below stopped describing a guard, this line would stop passing too.
    expect(producerOf('listMembers')).toContain('base ?')
    expect(producerOf('listApprovalPolicies'), 'the policies producer dereferences a null gateway').toContain('base ?')
    expect(APP, 'App.tsx forces base past the null check').not.toContain('listApprovalPolicies(authedFetch, base!)')

    // `immediate: base != null` alone is not the guard: it only suppresses the FIRST run,
    // and `refetchPolicies` calls the producer directly.
    expect(APP).toContain('{ immediate: base != null }')
  })
})

// ============================================================================
// APPR-09-03 QA (task-507) — what the mirror and the state report at the edges
// ============================================================================
// The mirror effect is three tokens of inline App.tsx that no jsdom test can mount, so it
// is modelled here against the REAL reducer rather than restated. `applyEffect` is the
// exact expression at App.tsx:298-300; `blankingEffect` is the `?? []` the two other
// mirrors use, kept so each assertion says what the guard buys.

describe('the policies mirror at the edges', () => {
  const A: Policy = { ...newPolicy(), id: 'polA', name: 'Standard' }
  const B: Policy = { ...newPolicy(), id: 'polB', name: 'Capex' }

  /** App.tsx:298-300, verbatim. */
  const applyEffect = (mirror: Policy[], data: Policy[] | null) => (data ? data : mirror)
  /** The members/roles idiom, for contrast. */
  const blankingEffect = (_mirror: Policy[], data: Policy[] | null) => data ?? []

  it('a publish refetch never empties a loaded list, where the ungated idiom would', () => {
    let state = initialState<Policy[]>(true)
    state = asyncReducer(state, { type: 'success', data: [A, B] })
    let mirror = applyEffect([], state.data)
    expect(mirror.map((p) => p.id), 'the list must load before the refetch is meaningful').toEqual(['polA', 'polB'])

    // publishPolicy calls policiesAsync.run(), which dispatches 'start' first.
    state = asyncReducer(state, { type: 'start' })
    expect(state.data, 'asyncReducer.start no longer nulls data — this spec is testing nothing').toBeNull()

    expect(applyEffect(mirror, state.data).map((p) => p.id)).toEqual(['polA', 'polB'])
    // The counterfactual, stated so the guard is not mistaken for decoration.
    expect(blankingEffect(mirror, state.data)).toEqual([])

    state = asyncReducer(state, { type: 'success', data: [B, A] })
    mirror = applyEffect(mirror, state.data)
    expect(mirror.map((p) => p.id), 'the landed refetch must still overwrite the mirror wholesale').toEqual(['polB', 'polA'])
  })

  it('a failed refetch keeps the loaded list AND reports the failure — the two are separate channels', () => {
    let state = asyncReducer(initialState<Policy[]>(true), { type: 'success', data: [A, B] })
    const mirror = applyEffect([], state.data)

    const boom = new ApiError('http', 'only an admin can read approval policies', 403)
    state = asyncReducer(state, { type: 'error', error: boom })

    expect(state.status).toBe('error')
    expect(state.error, 'the gateway message must reach the surface unreshaped').toBe(boom)
    expect(applyEffect(mirror, state.data).map((p) => p.id)).toEqual(['polA', 'polB'])
  })

  it('an EMPTY tenant and a FAILED fetch are indistinguishable by length — only policiesState separates them', () => {
    const empty = asyncReducer(initialState<Policy[]>(true), { type: 'success', data: [] })
    const failed = asyncReducer(initialState<Policy[]>(true), { type: 'error', error: new ApiError('http', 'gateway is down', 503) })

    // Both null `data`, so both leave the mirror at []. This is why WorkflowsView's ladder
    // must branch on `ctx.policiesState` (subtask 04 AC-1) and never on `policies.length`.
    expect(empty.data).toBeNull()
    expect(failed.data).toBeNull()
    expect(applyEffect([], empty.data)).toEqual([])
    expect(applyEffect([], failed.data)).toEqual([])

    expect(empty.status).toBe('empty')
    expect(failed.status).toBe('error')
  })

  it('a refetch landing EMPTY leaves the mirror stale — the state, not the list, is what says so', () => {
    // The cost the guard buys the flash with: `success` nulls `data` on the empty branch too,
    // so a tenant whose last policy was deleted elsewhere keeps rendering here until the
    // policiesState ladder lands. Recorded, not repaired — subtask 04 gates on the state.
    let state = asyncReducer(initialState<Policy[]>(true), { type: 'success', data: [A] })
    const mirror = applyEffect([], state.data)
    expect(mirror.map((p) => p.id)).toEqual(['polA'])

    state = asyncReducer(state, { type: 'success', data: [] })
    expect(state.status).toBe('empty')
    expect(applyEffect(mirror, state.data).map((p) => p.id)).toEqual(['polA'])
  })

  it('no gateway reports idle, never empty — a workspace without one has not been asked', () => {
    // `membersViewState` is what App.tsx runs `policiesAsync.status` through. An 'idle'
    // reading is the difference between "we never asked" and "the tenant has none".
    expect(membersViewState(null, 'ready')).toBe('idle')
    expect(membersViewState(null, 'empty')).toBe('idle')
    expect(membersViewState(null, 'error')).toBe('idle')
    // Control: with a gateway the status passes through untouched.
    expect(membersViewState('https://gw', 'empty')).toBe('empty')
    expect(membersViewState('https://gw', 'error')).toBe('error')

    expect(APP, 'policiesState bypasses the no-gateway reading').toContain('membersViewState(base, policiesAsync.status)')
  })
})

describe('the deleted per-mode store leaves no comment behind (AC-7)', () => {
  // Matched case-INSENSITIVELY. QA (task-507) added the last two after finding the store's
  // premise restated in prose that AC-7's four needles could not reach: App.tsx:470 said
  // "Policies are per workspace" and WorkflowsView.tsx:38 "held per WORKSPACE, not per
  // client". One spelling per needle would have missed one of the two.
  const NEEDLES = ['PolicyStore', 'per-mode', 'firm/inhouse', 'keyed firm', 'per workspace', 'per-workspace']

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
      const src = readFileSync(file, 'utf8').toLowerCase()
      for (const needle of NEEDLES) expect(src.includes(needle.toLowerCase()), `${file} names "${needle}"`).toBe(false)
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
