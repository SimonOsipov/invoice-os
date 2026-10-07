// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Mode A (AUDIT-09-02): written RED against a throwing StatusStrip stub.
//
// jsdom HAS NO LAYOUT ENGINE. Every flex / min-width / white-space assertion below reads
// an inline style PROP, never a measured box. The geometry claims -- above the fold, no
// caption ellipsised, the rail absorbing the slack -- are provable only in
// a browser and belong to the sweep in e2e/topology/invoice-surfaces.spec.ts.
//
// One spec renders nothing and is GREEN in the red commit by design -- the
// interactive-selector control needle. It exists to stop the specs above it passing vacuously.
//
// TIMEZONE: timestamps are offset-less, which ECMA-262 parses as LOCAL time, so fmtTime's
// local getHours()/getMinutes() round-trip them in every timezone (invoiceStrip.test.ts).

import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { cleanup, render } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { crossGlyph, tickGlyph11 } from '../glyphs'
import type { ApprovalRun } from '../lib/approvals'
import type { InvoiceStatus, StatusChange } from '../lib/invoices'
import { stripNodes, type StripNode, type StripState } from '../lib/invoiceStrip'

import { StatusStrip } from './StatusStrip'

afterEach(cleanup)

const KEYS = ['draft', 'validated', 'approved', 'queued', 'accepted'] as const
const ALL_STATES: StripState[] = ['done', 'current', 'failed', 'unreached', 'not-required']

const T_DRAFT = '2026-07-01T09:00:00' // 09:00
const T_VALIDATED = '2026-07-01T10:15:00' // 10:15
const T_QUEUED = '2026-07-01T11:30:00' // 11:30
const T_CLOSED = '2026-07-01T13:05:00' // 13:05
const T_TERMINAL = '2026-07-01T14:32:07' // 14:32

// A subject APP_PERSONAS cannot name, so actorLabel degrades it to `raw` and `mono`.
const UNRESOLVED_SUBJECT = '9f2c1a4b8e6d40f2a1b3c5d7e9f0a2b4'

// ---------------------------------------------------------------------------
// Fixtures. Every one below is the REAL mapper's output: a hand-written StripNode
// literal is a shape production may never emit, and the renderer would then be
// verified against a fiction. The one deliberate exception is SENTINEL_NODES.
// ---------------------------------------------------------------------------

function h(to: InvoiceStatus, changed_at: string, over: Partial<StatusChange> = {}): StatusChange {
  return {
    from_status: null,
    to_status: to,
    actor: APP_PERSONAS.firm.subject,
    actor_name: 'Ada Lovelace',
    actor_kind: 'person',
    changed_at,
    ...over,
  }
}

function sys(to: InvoiceStatus, changed_at: string, from: InvoiceStatus): StatusChange {
  return h(to, changed_at, { from_status: from, actor: 'system', actor_name: 'System', actor_kind: 'system' })
}

function mkRun(state: string, over: Partial<ApprovalRun> = {}): ApprovalRun {
  return {
    run_id: 'run-1',
    state,
    opened_at: T_VALIDATED,
    closed_at: null,
    closed_by: null,
    steps: [],
    decisions: [],
    ...over,
  }
}

const HISTORY_TO_QUEUED: StatusChange[] = [
  h('draft', T_DRAFT),
  h('validated', T_VALIDATED, { from_status: 'draft' }),
  h('queued', T_QUEUED, { from_status: 'validated' }),
]

const APPROVED_BY_HUMAN = mkRun('approved', { closed_at: T_CLOSED, closed_by: APP_PERSONAS.firm.subject })
const APPROVED_BY_SYSTEM = mkRun('approved', { closed_at: T_CLOSED, closed_by: 'system' })

const FRESH_DRAFT = stripNodes([h('draft', T_DRAFT)], null, 'draft')
const HUMAN_APPROVED_AT_QUEUED = stripNodes(HISTORY_TO_QUEUED, APPROVED_BY_HUMAN, 'queued')
const ACCEPTED = stripNodes([...HISTORY_TO_QUEUED, sys('accepted', T_TERMINAL, 'queued')], APPROVED_BY_SYSTEM, 'accepted')
const REJECTED = stripNodes([...HISTORY_TO_QUEUED, sys('rejected', T_TERMINAL, 'queued')], APPROVED_BY_SYSTEM, 'rejected')
const TRANSMISSION_FAILED = stripNodes([...HISTORY_TO_QUEUED, sys('failed', T_TERMINAL, 'queued')], APPROVED_BY_SYSTEM, 'failed')
const APPROVAL_REJECTED = stripNodes(HISTORY_TO_QUEUED, mkRun('rejected', { closed_at: T_CLOSED, closed_by: 'system' }), 'queued')
const NO_APPROVAL_RUN = stripNodes(HISTORY_TO_QUEUED, null, 'queued')
const UNNAMEABLE_ACTOR = stripNodes(
  [h('draft', T_DRAFT, { actor: UNRESOLVED_SUBJECT, actor_name: '' }), h('validated', T_VALIDATED, { from_status: 'draft' })],
  null,
  'validated',
)
// approvalRunStateView('') returns its argument, so node 3's caption is ''.
const EMPTY_RUN_STATE = stripNodes(HISTORY_TO_QUEUED, mkRun(''), 'validated')

const SCENARIOS: Array<[string, StripNode[]]> = [
  ['fresh draft', FRESH_DRAFT],
  ['human-approved, queued', HUMAN_APPROVED_AT_QUEUED],
  ['accepted by FIRS', ACCEPTED],
  ['rejected by FIRS', REJECTED],
  ['transmission failed', TRANSMISSION_FAILED],
  ['approval rejected', APPROVAL_REJECTED],
  ['no approval run', NO_APPROVAL_RUN],
  ['unnameable actor', UNNAMEABLE_ACTOR],
  ['empty run state', EMPTY_RUN_STATE],
]

// Hand-written on purpose: no mapper input produces these labels or captions, and
// pass-through is precisely the property an internal lookup table would break.
const SENTINEL_NODES: StripNode[] = KEYS.map((key, i) => ({
  key,
  label: `Sentinel label ${i}`,
  state: 'done' as StripState,
  at: null,
  actor: null,
  caption: `Sentinel caption ${i}`,
}))

// The prototype's status-strip look map, with the label colours applied.
const PROTOTYPE_MAP: Record<StripState, { bg: string; border: string; fg: string; label: string }> = {
  done: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', fg: 'var(--status-green-text)', label: 'var(--fg-1)' },
  failed: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', fg: 'var(--status-red-text)', label: 'var(--fg-1)' },
  current: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-text)', fg: 'var(--status-amber-text)', label: 'var(--status-amber-text)' },
  unreached: { bg: 'var(--bg-2)', border: 'var(--line-3)', fg: 'var(--fg-4)', label: 'var(--fg-3)' },
  'not-required': { bg: 'var(--bg-3)', border: 'var(--line-2)', fg: 'var(--fg-4)', label: 'var(--fg-3)' },
}

const TOKEN_DIR = join(dirname(fileURLToPath(import.meta.url)), '../../../../packages/design-tokens/v2')
const TOKENS_CSS = [join(TOKEN_DIR, 'app-layer.css'), ...readdirSync(join(TOKEN_DIR, 'tokens')).map((f) => join(TOKEN_DIR, 'tokens', f))]
  .map((f) => readFileSync(f, 'utf8'))
  .join('\n')

// ---------------------------------------------------------------------------
// Render helpers
// ---------------------------------------------------------------------------

function renderStrip(nodes: StripNode[]): HTMLElement {
  const { container } = render(<StatusStrip nodes={nodes} />)
  const strip = container.querySelector<HTMLElement>('[data-testid="status-strip"]')
  expect(strip, 'the container carries data-testid="status-strip"').not.toBeNull()
  return strip!
}

function nodesOf(strip: HTMLElement): HTMLElement[] {
  const els = Array.from(strip.querySelectorAll<HTMLElement>('[data-testid="strip-node"]'))
  expect(els, 'five step blocks').toHaveLength(5)
  return els
}

function actorsOf(strip: HTMLElement): HTMLElement[] {
  const els = Array.from(strip.querySelectorAll<HTMLElement>('[data-testid="strip-actor"]'))
  expect(els, 'strip-actor is present on every node, whatever its state').toHaveLength(5)
  return els
}

function iconOf(node: HTMLElement): HTMLElement {
  const icon = node.firstElementChild as HTMLElement | null
  expect(icon, 'the node opens with its glyph holder').not.toBeNull()
  expect(icon!.getAttribute('aria-hidden'), 'the glyph holder is decorative').toBe('true')
  return icon!
}

// The label is the caption's immediate previous sibling: a bare `span > span` selector
// would match the dot inside the glyph holder first.
function labelOf(node: HTMLElement): HTMLElement {
  const actor = node.querySelector<HTMLElement>('[data-testid="strip-actor"]')
  expect(actor, 'the node carries a strip-actor caption').not.toBeNull()
  const label = actor!.previousElementSibling as HTMLElement | null
  expect(label, 'the label sits immediately above the caption, in one column').not.toBeNull()
  return label!
}

// Serialises an imported glyph through the DOM so the comparison is against what the
// shipped node renders, not against a path string copied into this file.
function glyphHtml(glyph: ReactNode): string {
  const { container } = render(<span>{glyph}</span>)
  const svg = container.querySelector('svg')
  expect(svg, 'the imported glyph renders an svg').not.toBeNull()
  return svg!.outerHTML
}

const INTERACTIVE = 'button, a[href], [role="button"], [role="link"], input, select, textarea'

// ---------------------------------------------------------------------------

describe('StatusStrip: the five nodes', () => {
  it('renders five nodes in the fixed key order, with data-key and data-state on each', () => {
    for (const [name, nodes] of SCENARIOS) {
      cleanup()
      const strip = renderStrip(nodes)
      const els = nodesOf(strip)
      expect(els.map((e) => e.getAttribute('data-key')), name).toEqual([...KEYS])
      expect(els.map((e) => e.getAttribute('data-state')), name).toEqual(nodes.map((n) => n.state))
    }
    expect(SCENARIOS.length, 'the sweep is not empty').toBe(9)
  })

  it('every StripState reaches the DOM as data-state -- the sweep covers all five', () => {
    const seen = new Set<string>()
    for (const [, nodes] of SCENARIOS) {
      cleanup()
      for (const el of nodesOf(renderStrip(nodes))) seen.add(el.getAttribute('data-state') ?? '')
    }
    expect([...seen].sort(), 'no state is asserted vacuously below').toEqual([...ALL_STATES].sort())
  })

  it('strip-actor renders on all five nodes in every scenario -- the count never varies with state', () => {
    // A caption count that shrinks on unreached nodes makes the browser
    // ellipsis sweep skip them silently, so the element is unconditional.
    for (const [name, nodes] of SCENARIOS) {
      cleanup()
      const actors = actorsOf(renderStrip(nodes))
      expect(actors, name).toHaveLength(5)
    }
  })

  it('labels and captions pass through from the node, untouched', () => {
    for (const [name, nodes] of SCENARIOS) {
      cleanup()
      const strip = renderStrip(nodes)
      expect(actorsOf(strip).map((e) => e.textContent), name).toEqual(nodes.map((n) => n.caption))
      const text = nodesOf(strip).map((e) => e.textContent ?? '')
      nodes.forEach((n, i) => expect(text[i], `${name} node ${n.key}`).toContain(n.label))
    }
  })

  it('node 5 renders the mapper relabels verbatim, and no lookup inside the component', () => {
    cleanup()
    expect(REJECTED[4].label, 'fixture guard').toBe('Rejected by FIRS')
    expect(nodesOf(renderStrip(REJECTED))[4].textContent).toContain('Rejected by FIRS')
    cleanup()
    expect(TRANSMISSION_FAILED[4].label, 'fixture guard').toBe('Transmission failed')
    expect(nodesOf(renderStrip(TRANSMISSION_FAILED))[4].textContent).toContain('Transmission failed')
    cleanup()
    expect(ACCEPTED[4].label, 'fixture guard').toBe('Accepted by FIRS')
    expect(nodesOf(renderStrip(ACCEPTED))[4].textContent).toContain('Accepted by FIRS')

    // A label no mapper input can produce: an internal FIXED_LABEL lookup would print
    // 'Draft' here and this is the only assertion that would catch it.
    cleanup()
    const strip = renderStrip(SENTINEL_NODES)
    expect(nodesOf(strip).map((e) => e.textContent)).toEqual(
      SENTINEL_NODES.map((n) => `${n.label}${n.caption}`),
    )
  })
})

describe('StatusStrip: the rail and the step blocks (inline style props, not geometry)', () => {
  it('the container is the pf-scroll-x recipe: focusable, grouped, scrolling', () => {
    const strip = renderStrip(HUMAN_APPROVED_AT_QUEUED)
    expect(strip.className.split(' ')).toContain('pf-scroll-x')
    expect(strip.getAttribute('role')).toBe('group')
    expect(strip.tabIndex).toBe(0)
    expect(strip.getAttribute('aria-label')).toBeTruthy()
    expect(strip.style.display).toBe('flex')
    expect(strip.style.alignItems).toBe('flex-start')
    expect(strip.style.overflowX).toBe('auto')
  })

  it('step blocks are flex:none + min-width:max-content; connectors are flex:1', () => {
    // jsdom has NO layout engine: this reads the inline style prop, so it proves the
    // component asked for the right flex behaviour, not that the browser delivered it.
    // The real proof is the 2560-vs-1280 sweep in e2e/topology.
    const strip = renderStrip(ACCEPTED)
    const children = Array.from(strip.children) as HTMLElement[]
    expect(children, 'five blocks interleaved with four connectors').toHaveLength(9)

    const blocks = children.filter((_, i) => i % 2 === 0)
    const connectors = children.filter((_, i) => i % 2 === 1)
    expect(blocks).toHaveLength(5)
    expect(connectors).toHaveLength(4)

    for (const b of blocks) {
      expect(b.getAttribute('data-testid'), 'even children are the step blocks').toBe('strip-node')
      // jsdom normalises `flex: none` to the longhands.
      expect(b.style.flexGrow).toBe('0')
      expect(b.style.flexShrink).toBe('0')
      expect(b.style.minWidth).toBe('max-content')
    }
    for (const c of connectors) {
      expect(c.getAttribute('data-testid'), 'odd children are the connectors').toBeNull()
      expect(c.getAttribute('aria-hidden'), 'a connector is decorative').toBe('true')
      expect(c.style.flexGrow, 'the connector, not the block, absorbs the slack').toBe('1')
    }
  })

  it('labels and captions never wrap -- the container scrolls instead', () => {
    // The inverse of the retired card overflowWrap:'anywhere'. Style prop only; the
    // no-ellipsis proof is the e2e/topology sweep.
    const strip = renderStrip(UNNAMEABLE_ACTOR)
    for (const actor of actorsOf(strip)) expect(actor.style.whiteSpace).toBe('nowrap')
    for (const node of nodesOf(strip)) expect(labelOf(node).style.whiteSpace).toBe('nowrap')
  })
})

describe('StatusStrip: tone', () => {
  // The names come from the rendered strip, so a typo in the shipped map is what fails.
  it('every token the strip map names is defined in the v2 app layer', () => {
    const byState = new Map<StripState, string[]>()
    for (const [, nodes] of SCENARIOS) {
      cleanup()
      const els = nodesOf(renderStrip(nodes))
      nodes.forEach((n, i) => {
        if (byState.has(n.state)) return
        const icon = iconOf(els[i])
        byState.set(n.state, [icon.style.background, icon.style.border, icon.style.color, labelOf(els[i]).style.color])
      })
    }
    expect([...byState.keys()].sort(), 'all five states are read').toEqual([...ALL_STATES].sort())
    const names = [...byState.values()].flat().map((v) => v.match(/var\((--[a-z0-9-]+)\)/)?.[1] ?? v)
    expect(names, 'five states x bg, border, fg, label').toHaveLength(20)
    const css = TOKENS_CSS.replace(/\/\*[\s\S]*?\*\//g, '')
    for (const name of names) {
      expect(name, 'every painted value is a var() reference').toMatch(/^--[a-z0-9-]+$/)
      expect(css, `${name} is a real token`).toContain(`${name}:`)
    }
  })

  it('each state paints the prototype map, and all five states are exercised', () => {
    const seen = new Set<StripState>()
    for (const [name, nodes] of SCENARIOS) {
      cleanup()
      const els = nodesOf(renderStrip(nodes))
      nodes.forEach((n, i) => {
        const m = PROTOTYPE_MAP[n.state]
        const icon = iconOf(els[i])
        expect(icon.style.background, `${name}/${n.key} bg`).toBe(m.bg)
        expect(icon.style.border, `${name}/${n.key} border`).toBe(`1px solid ${m.border}`)
        expect(icon.style.color, `${name}/${n.key} node colour`).toBe(m.fg)
        expect(labelOf(els[i]).style.color, `${name}/${n.key} label colour`).toBe(m.label)
        seen.add(n.state)
      })
    }
    expect([...seen].sort(), 'no state went unasserted').toEqual([...ALL_STATES].sort())
  })

  it('no strip label is --fg-4', () => {
    const labels: string[] = []
    const glyphs: string[] = []
    for (const [, nodes] of SCENARIOS) {
      cleanup()
      const els = nodesOf(renderStrip(nodes))
      els.forEach((el) => {
        labels.push(labelOf(el).style.color)
        glyphs.push(iconOf(el).style.color)
      })
    }
    expect(labels.length, 'labels were read').toBeGreaterThan(0)
    expect(labels).not.toContain('var(--fg-4)')
    // Control: --fg-4 stays on the unreached node glyph (an icon use), so the absence above is about labels.
    expect(glyphs).toContain('var(--fg-4)')
  })

  it('nodes are 19px circles; the dot is 5px', () => {
    const strip = renderStrip(stripNodes(HISTORY_TO_QUEUED, null, 'draft'))
    expect(strip.style.padding).toBe('13px 20px')
    expect(strip.style.marginBottom).toBe('16px')
    const [first] = nodesOf(strip)
    expect(first.style.gap, 'node gap').toBe('9px')
    const icon = iconOf(first)
    expect(icon.style.width).toBe('19px')
    expect(icon.style.height).toBe('19px')
    expect(icon.style.borderRadius).toBe('50%')
    expect(icon.style.marginTop).toBe('1px')
    const dot = icon.firstElementChild as HTMLElement
    expect(dot.querySelector('svg'), 'an open step draws the dot, not a glyph').toBeNull()
    expect(dot.style.width).toBe('5px')
    expect(dot.style.height).toBe('5px')
    expect(dot.style.borderRadius).toBe('50%')
    expect(dot.style.background.toLowerCase()).toBe('currentcolor')
    const rail = strip.children[1] as HTMLElement
    expect(rail.getAttribute('aria-hidden'), 'children[1] is the rail').toBe('true')
    expect(rail.style.marginTop).toBe('10px')
    expect(rail.style.minWidth).toBe('10px')
  })
})

describe('StatusStrip: glyphs', () => {
  it('tick on done, cross on failed, a plain dot on every other state', () => {
    const tick = glyphHtml(tickGlyph11)
    const cross = glyphHtml(crossGlyph)
    expect(tick, 'the two glyphs are distinguishable').not.toBe(cross)

    const seen = new Set<StripState>()
    for (const [name, nodes] of SCENARIOS) {
      const strip = renderStrip(nodes)
      const els = nodesOf(strip)
      nodes.forEach((n, i) => {
        const icon = iconOf(els[i])
        const svg = icon.querySelector('svg')
        if (n.state === 'done') {
          expect(svg?.outerHTML, `${name}/${n.key} renders tickGlyph11`).toBe(tick)
        } else if (n.state === 'failed') {
          expect(svg?.outerHTML, `${name}/${n.key} renders crossGlyph`).toBe(cross)
        } else {
          expect(svg, `${name}/${n.key} carries no glyph`).toBeNull()
          const dot = icon.firstElementChild as HTMLElement | null
          expect(dot, `${name}/${n.key} renders a dot`).not.toBeNull()
          expect(dot!.style.width).toBe('5px')
          expect(dot!.style.height).toBe('5px')
          // jsdom lowercases the keyword.
          expect(dot!.style.background.toLowerCase(), 'the dot inherits the tone').toBe('currentcolor')
        }
        seen.add(n.state)
      })
      cleanup()
    }
    expect([...seen].sort(), 'every state chose a glyph branch').toEqual([...ALL_STATES].sort())
  })

  it('a passed validation renders exactly like the Draft node beside it', () => {
    const tick = glyphHtml(tickGlyph11)
    const els = nodesOf(renderStrip(stripNodes(HISTORY_TO_QUEUED, null, 'validated')))
    const draft = iconOf(els[0])
    const validated = iconOf(els[1])
    expect(validated.style.background).toBe(draft.style.background)
    expect(validated.style.border).toBe(draft.style.border)
    expect(validated.style.color).toBe(draft.style.color)
    expect(draft.style.background, 'both green, not both amber').toBe(PROTOTYPE_MAP.done.bg)
    for (const icon of [draft, validated]) {
      expect(icon.querySelector('svg path[d="M20 6 9 17l-5-5"]')).not.toBeNull()
      expect(icon.querySelector('svg')?.outerHTML).toBe(tick)
      expect(icon.firstElementChild?.tagName.toLowerCase(), 'no dot').toBe('svg')
    }

    // Control: an open step still draws the dot and no tick.
    cleanup()
    const open = iconOf(nodesOf(renderStrip(stripNodes(HISTORY_TO_QUEUED, null, 'draft')))[0])
    expect(open.querySelector('svg')).toBeNull()
    expect((open.firstElementChild as HTMLElement).style.width).toBe('5px')
  })
})

describe('StatusStrip: attribution', () => {
  it('the mono class follows actor.mono and covers the whole caption', () => {
    expect(UNNAMEABLE_ACTOR[0].actor?.mono, 'fixture guard: an unresolvable subject is mono').toBe(true)
    expect(UNNAMEABLE_ACTOR[0].caption).toContain(UNRESOLVED_SUBJECT)
    const monoCaption = actorsOf(renderStrip(UNNAMEABLE_ACTOR))[0]
    expect(monoCaption.className.split(' ')).toContain('mono')
    expect(monoCaption.textContent, 'the timestamp is inside the mono span too').toBe(UNNAMEABLE_ACTOR[0].caption)

    // Control needle: a resolved person is NOT mono, so the assertion above is about
    // actor.mono and not about the class being unconditional.
    cleanup()
    expect(HUMAN_APPROVED_AT_QUEUED[0].actor?.mono, 'fixture guard: a named person is not mono').toBe(false)
    const named = actorsOf(renderStrip(HUMAN_APPROVED_AT_QUEUED))[0]
    expect(named.className.split(' ')).not.toContain('mono')
  })

  it('a time with no actor renders as the time (arch §12 C-5, the dominant production case)', () => {
    // A run approved by a human gives node 3 `at` with a null `actor` -- resolving
    // closed_by would re-open the cross-tenant leak S-10 guards. A renderer that draws
    // the attribution only when `actor` is set blanks node 3 on the commonest path.
    const node3 = HUMAN_APPROVED_AT_QUEUED[2]
    expect(node3.key, 'fixture guard').toBe('approved')
    expect(node3.at, 'fixture guard: a time').not.toBeNull()
    expect(node3.actor, 'fixture guard: and no actor').toBeNull()

    const caption = actorsOf(renderStrip(HUMAN_APPROVED_AT_QUEUED))[2]
    expect(caption.textContent).toMatch(/^\d\d:\d\d$/)
    expect(caption.textContent).not.toBe('—')
    expect(caption.className.split(' '), 'no actor, so no mono').not.toContain('mono')

    // Control: the system-closed run in the same position DOES carry an attribution,
    // so the assertion above is not passing on a renderer that drops actors entirely.
    cleanup()
    expect(ACCEPTED[2].actor?.text, 'fixture guard').toBe('System')
    expect(actorsOf(renderStrip(ACCEPTED))[2].textContent).toMatch(/^\d\d:\d\d · System$/)
  })

  it('no node ever renders a visually empty caption', () => {
    // Was RED against a hole: an empty run.state captioned node 3 with ''
    // and the strip drew a blank cell. Closed in invoiceStrip.ts's default branch, pinned
    // there by S-33; the guard below keeps this fixture on that branch.
    expect(EMPTY_RUN_STATE[2].caption, 'fixture guard: the C-6 fallback, not a blank cell').toBe('Waiting')
    for (const [name, nodes] of SCENARIOS) {
      cleanup()
      for (const actor of actorsOf(renderStrip(nodes))) {
        expect((actor.textContent ?? '').trim(), `${name}: every node captions something`).not.toBe('')
      }
    }
  })
})

describe('StatusStrip: read-only (AC-9 / arch §7E)', () => {
  it('contains no interactive element, while staying keyboard-reachable', () => {
    for (const [name, nodes] of SCENARIOS) {
      cleanup()
      const strip = renderStrip(nodes)
      expect(Array.from(strip.querySelectorAll(INTERACTIVE)), name).toHaveLength(0)
      // Focusable is not interactive: pf-scroll-x needs the tab stop so a keyboard user
      // can reach the far node. F-43 criterion 1 is 'read-only', and scrolling acts on
      // nothing.
      expect(strip.getAttribute('role')).toBe('group')
      expect(strip.tabIndex).toBe(0)
    }
  })

  it('control needle: the same selector does find an interactive element', () => {
    const { container } = render(
      <div>
        <button type="button">act</button>
      </div>,
    )
    expect(Array.from(container.querySelectorAll(INTERACTIVE)), 'the absence above is not vacuous').toHaveLength(1)
  })
})
