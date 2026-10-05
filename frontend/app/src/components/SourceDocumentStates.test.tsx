// @vitest-environment jsdom
// QA adversarial (task-392, BUG-03-03): NoSourceCanvas is actorLabel's third caller, an
// undocumented one the story's plan didn't grep for -- SourceDocumentModal.test.tsx only
// asserts this canvas's static sentence, never the resolved-persona clause it computes.
// An unproven migration is how the original raw-uuid defect got in in the first place.
import { ApiError } from '@invoice-os/api-client'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS } from '../auth'
import type { SourceDocumentRecord } from '../lib/sourceDocument'
import { FailedCanvas, LoadingCanvas, NoSourceCanvas, UnrenderableCanvas } from './SourceDocumentStates'

afterEach(cleanup)

describe('NoSourceCanvas actor resolution ([actor-label-shared])', () => {
  it('a known persona subject resolves to a name, not the raw uuid', () => {
    render(<NoSourceCanvas invoiceNumber="INV-2026-0037" createdAt="2026-06-12T09:15:00Z" createdBy={APP_PERSONAS.firm.subject} />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).toContain(`by ${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`)
    expect(canvas.textContent).not.toContain(APP_PERSONAS.firm.subject)
  })

  // NoSourceCanvas's own contract (SourceDocumentStates.tsx comment: "a raw uuid never
  // appears mid-prose"): unlike the strip's attribution and the kept-banner, an
  // unrecognised subject here is NOT shown raw -- the whole "by ..." clause is dropped.
  it('an unrecognised subject omits the "by" clause instead of leaking a raw uuid mid-prose', () => {
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    render(<NoSourceCanvas invoiceNumber="INV-2026-0037" createdAt="2026-06-12T09:15:00Z" createdBy={unknown} />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).not.toContain(unknown)
    // "ASComply on ..." not "ASComply by ... on ..." -- the clause is dropped whole, not
    // filled with the raw uuid. (Narrower than ' by ': the fallback prose below contains
    // "entered by hand", an unrelated match.)
    expect(canvas.textContent).toContain('into ASComply on')
  })

  it('a null creator also omits the "by" clause', () => {
    render(<NoSourceCanvas invoiceNumber="INV-2026-0037" createdAt="2026-06-12T09:15:00Z" createdBy={null} />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).toContain('into ASComply on')
    expect(canvas.textContent).not.toContain('Not recorded')
  })
})

// AUDIT-02-04 Stage-4. `system` actors the genesis row of every seeded invoice
// (db/seed.dev.sql:628) and now resolves to a NAME, so the old `!creator.mono` gate wrote
// "typed into ASComply by System" -- nobody typed it in. Only a person is ever named.
describe('NoSourceCanvas names a person and nobody else ([actor-label-shared])', () => {
  it('a system actor omits the "by" clause', () => {
    render(<NoSourceCanvas invoiceNumber="DEMO-2026-1009" createdAt="2026-06-12T09:15:00Z" createdBy="system" />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).not.toContain('by System')
    expect(canvas.textContent).toContain('into ASComply on')
  })

  it("the server's resolved pair decides the clause, not APP_PERSONAS", () => {
    render(
      <NoSourceCanvas
        invoiceNumber="DEMO-2026-1009"
        createdAt="2026-06-12T09:15:00Z"
        createdBy={APP_PERSONAS.inhouse.subject}
        createdByResolved={{ name: 'System', kind: 'system' }}
      />,
    )

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).not.toContain('by System')
    expect(canvas.textContent).not.toContain(APP_PERSONAS.inhouse.name)
    expect(canvas.textContent).toContain('into ASComply on')
  })
})

// The v2 look of the four non-sheet canvases. jsdom reads inline
// styles, so every value below is the authored one; `0` in a shorthand reads back as `0px`.
const RECORD: SourceDocumentRecord = {
  id: 'doc-1',
  filename: 'ledger.dat',
  declared_content_type: null,
  size_bytes: 624640,
  content_hash: '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560',
  uploaded_at: '2026-06-12T11:42:00Z',
  uploaded_by: APP_PERSONAS.firm.subject,
  invoices_created: 1,
  other_invoice_rows: [],
}

function none() {
  return render(<NoSourceCanvas invoiceNumber="INV-2026-0037" createdAt="2026-06-12T09:15:00Z" createdBy={APP_PERSONAS.firm.subject} />)
}
function bad() {
  return render(<UnrenderableCanvas record={RECORD} />)
}
function failed() {
  return render(<FailedCanvas error={new ApiError('http', 'boom', 503)} onRetry={vi.fn()} />)
}

function descendants(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>('*'))
}

/** The nearest ancestor of `el` (inclusive), below `root`, whose inline style sets `prop`. */
function styled(el: HTMLElement, root: HTMLElement, prop: 'padding' | 'width' | 'maxWidth'): HTMLElement | null {
  for (let n: HTMLElement | null = el; n && n !== root; n = n.parentElement) if (n.style[prop] !== '') return n
  return null
}

describe('LoadingCanvas follows the prototype skeleton', () => {
  it('loading draws the prototype\'s skeleton', () => {
    render(<LoadingCanvas sizeBytes={624640} />)
    const canvas = screen.getByTestId('source-document-loading')
    const label = screen.getByText(/READING .* FROM DOCUMENT STORAGE/)
    expect(label.textContent, 'control: the label is read').toContain('610 KB')

    expect.soft(canvas.innerHTML, 'no stripes').not.toContain('repeating-linear-gradient')
    expect.soft(canvas.innerHTML, 'no shimmer').not.toContain('shimmer')

    const toolbar = label.parentElement as HTMLElement
    expect.soft(toolbar.style.padding).toBe('11px 16px')
    expect.soft(toolbar.style.gap).toBe('12px')
    expect(toolbar.style.background, 'pin').toBe('var(--bg-2)')
    expect(label.className, 'pin: the label is mono').toContain('mono')
    expect.soft(label.style.fontSize).toBe('10px')
    expect.soft(label.style.color).toBe('var(--fg-3)')
    expect(label.style.letterSpacing, 'pin').toBe('0.06em')

    const all = descendants(canvas)
    const tracks = all.filter((el) => el.style.height === '4px')
    expect(tracks, 'exactly one 4px track').toHaveLength(1)
    const track = tracks[0]
    expect.soft(track.style.background).toBe('var(--bg-3)')
    expect.soft(track.style.borderRadius).toBe('4px')
    expect.soft(track.style.animation, 'a static track').toBe('')
    expect.soft(track.children.length, 'no inner fill').toBe(0)

    const rows = all.filter((el) => el.style.height === '26px')
    expect(rows, '8 pulsing + 3 static rows').toHaveLength(11)
    rows.forEach((row, i) => {
      expect.soft(row.style.background, `row ${i} ground`).toBe('var(--bg-3)')
      expect.soft(row.style.borderRadius, `row ${i} radius`).toBe('var(--radius-md)')
    })
    for (const row of rows.slice(0, 8)) expect.soft(row.style.animation).toMatch(/^pulse /)
    expect.soft(rows[0].style.animation).toBe('pulse 1.5s linear infinite')
    // the first static row is at full opacity: authored as 1 or left to default
    const statics = rows.slice(8)
    for (const row of statics) expect.soft(row.style.animation, 'static row').toBe('')
    expect.soft(['', '1']).toContain(statics[0]?.style.opacity)
    expect.soft(statics[1]?.style.opacity).toBe('0.6')
    expect.soft(statics[2]?.style.opacity).toBe('0.35')

    const block = rows[0].parentElement as HTMLElement
    expect.soft(block.style.gap).toBe('9px')
    expect.soft(styled(block, canvas, 'padding')?.style.padding, 'rows block padding').toBe('14px 16px')

    const para = screen.getByText(/only the bytes are still on their way/)
    expect.soft(para.style.fontSize).toBe('12.5px')
    expect.soft(para.style.color).toBe('var(--fg-3)')
    expect.soft(para.style.lineHeight).toBe('1.55')
    expect.soft(para.style.maxWidth).toBe('460px')
    expect.soft(para.style.marginTop).toBe('20px')
  })

  it('loading with a zero size still names the read', () => {
    render(<LoadingCanvas sizeBytes={0} />)
    expect(screen.getByTestId('source-document-loading').textContent, 'a size of 0 is known, not missing').toContain('READING 0 B FROM DOCUMENT STORAGE')
  })

  it('loading without a size drops the label and keeps the skeleton', () => {
    render(<LoadingCanvas sizeBytes={null} />)
    const canvas = screen.getByTestId('source-document-loading')
    expect(canvas.textContent, 'floor').toContain('only the bytes are still on their way')
    expect(canvas.textContent, 'pin').not.toContain('READING')
    expect(descendants(canvas).filter((el) => el.style.height === '26px')).toHaveLength(11)
    expect(descendants(canvas).filter((el) => el.style.height === '4px')).toHaveLength(1)
  })
})

describe('bad, error and none states follow v2', () => {
  const states = [
    { name: 'none', testid: 'source-document-no-source', heading: 'There is no source document', maxWidth: '540px', mount: none },
    { name: 'bad', testid: 'source-document-unrenderable', heading: 'This file is stored, but we cannot render it here', maxWidth: '560px', mount: bad },
    { name: 'error', testid: 'source-document-failed', heading: 'The document did not load', maxWidth: '520px', mount: failed },
  ]

  it('the bad and error tiles draw the prototype warn triangle at 15', () => {
    for (const st of states.filter((x) => x.name !== 'none')) {
      st.mount()
      const svg = screen.getByTestId(st.testid).querySelector('svg') as SVGElement
      expect(svg, `${st.name}: control: the glyph rendered`).not.toBeNull()
      expect.soft(svg.querySelector('path')?.getAttribute('d'), `${st.name}: path`).toMatch(/^M10\.29 3\.86 1\.82 18a2 2 0 0 0 1\.71 3h16\.94/)
      expect.soft(svg.getAttribute('width'), `${st.name}: size`).toBe('15')
      cleanup()
    }
  })

  it('state headings and tiles follow v2', () => {
    for (const st of states) {
      st.mount()
      const canvas = screen.getByTestId(st.testid)
      const heading = screen.getByText(st.heading)
      const at = (what: string) => `${st.name}: ${what}`

      expect.soft(canvas.style.display, at('layout')).toBe('grid')
      expect.soft(canvas.style.placeItems, at('centred')).toBe('center')
      expect.soft(canvas.style.padding, at('padding')).toBe('32px')
      expect.soft(styled(heading, canvas, 'maxWidth')?.style.maxWidth, at('content width')).toBe(st.maxWidth)

      expect.soft(heading.style.fontSize, at('heading size')).toBe('19px')
      expect.soft(heading.style.fontWeight, at('heading weight')).toBe('700')
      expect.soft(heading.style.letterSpacing, at('heading tracking')).toBe('-0.02em')
      expect.soft(heading.style.margin, at('heading margin')).toBe('0px 0px 9px')
      expect(heading.tagName, at('pin: HEADING stays a div')).toBe('DIV')

      const svg = canvas.querySelector('svg')
      expect.soft(svg, at('a glyph tile exists')).not.toBeNull()
      if (!svg) {
        cleanup()
        continue
      }
      const tile = styled(svg.parentElement as HTMLElement, canvas, 'width') ?? (svg.parentElement as HTMLElement)
      expect.soft(tile.style.width, at('tile width')).toBe('44px')
      expect.soft(tile.style.height, at('tile height')).toBe('44px')
      expect.soft(tile.style.borderRadius, at('tile radius')).toBe('var(--radius-md)')
      expect.soft(tile.style.marginBottom, at('tile margin')).toBe('16px')
      expect.soft(svg.getAttribute('width'), at('glyph size')).toBe(st.name === 'none' ? '20' : '15')
      expect.soft(svg.getAttribute('height'), at('glyph height')).toBe(st.name === 'none' ? '20' : '15')
      cleanup()
    }
  })

  it('tile colours follow the prototype', () => {
    const tones: Array<[() => unknown, string, string, string]> = [
      [bad, 'source-document-unrenderable', 'var(--status-amber-bg)', 'var(--status-amber-text)'],
      [failed, 'source-document-failed', 'var(--status-red-bg)', 'var(--status-red-text)'],
      [none, 'source-document-no-source', 'var(--bg-3)', 'var(--fg-3)'],
    ]
    for (const [mount, testid, bg, fg] of tones) {
      mount()
      const svg = screen.getByTestId(testid).querySelector('svg')
      expect.soft(svg, `${testid}: a glyph tile exists`).not.toBeNull()
      if (!svg) {
        cleanup()
        continue
      }
      const tile = svg.parentElement as HTMLElement
      expect.soft(tile.style.background, `${testid}: tile ground`).toBe(bg)
      expect.soft(tile.style.color, `${testid}: tile glyph`).toBe(fg)
      cleanup()
    }
  })

  it('body paragraphs follow v2', () => {
    for (const [mount, testid, margins] of [
      [bad, 'source-document-unrenderable', ['0px 0px 8px', '0px 0px 20px']],
      [failed, 'source-document-failed', ['0px 0px 8px', '0px 0px 20px']],
      [none, 'source-document-no-source', ['0px 0px 20px']],
    ] as const) {
      mount()
      const paras = Array.from(screen.getByTestId(testid).querySelectorAll<HTMLElement>(':scope p')).slice(0, margins.length)
      expect(paras, `${testid}: paragraphs`).toHaveLength(margins.length)
      paras.forEach((p, i) => {
        expect.soft(p.style.fontSize, `${testid} p${i} size`).toBe('13.5px')
        expect.soft(p.style.color, `${testid} p${i} colour`).toBe('var(--fg-2)')
        expect.soft(p.style.lineHeight, `${testid} p${i} leading`).toBe('1.6')
        expect.soft(p.style.margin, `${testid} p${i} margin`).toBe(margins[i])
      })
      cleanup()
    }
  })

  it('the bad state\'s facts box follows v2', () => {
    bad()
    const canvas = screen.getByTestId('source-document-unrenderable')
    const title = screen.getByText('What we know about this file')
    const strip = title.parentElement as HTMLElement
    const box = strip.parentElement as HTMLElement
    expect(canvas.contains(box) && box !== canvas, 'control: the facts box').toBe(true)
    expect.soft(box.style.background).toBe('var(--bg-2)')
    expect.soft(box.style.marginBottom).toBe('20px')

    expect.soft(strip.style.background).toBe('var(--bg-1)')
    expect.soft(title.className.split(/\s+/)).toContain('label')

    for (const key of ['FORMAT', 'READER', 'IMPORTED', 'INTEGRITY']) {
      const k = screen.getByText(key)
      const row = k.parentElement as HTMLElement
      const value = k.nextElementSibling as HTMLElement
      expect.soft(row.style.display, `${key}: row`).toBe('grid')
      expect.soft(row.style.gridTemplateColumns, `${key}: columns`).toBe('96px 1fr')
      expect.soft(row.style.gap, `${key}: gap`).toBe('12px')
      expect.soft(row.style.padding, `${key}: padding`).toBe('8px 14px')
      expect.soft(row.style.alignItems, `${key}: row aligns on the baseline`).toBe('baseline')
      expect.soft(k.style.paddingTop, `${key}: key has no padding-top`).toBe('')
      expect(k.className, `${key}: key pin`).toContain('mono')
      expect.soft(k.style.fontSize, `${key}: key size`).toBe('9.5px')
      expect.soft(k.style.fontWeight, `${key}: key weight`).toBe('700')
      expect.soft(k.style.letterSpacing, `${key}: key tracking`).toBe('0.07em')
      expect.soft(k.style.color, `${key}: key colour`).toBe('var(--fg-3)')
      expect.soft(value.className, `${key}: value is sans`).not.toContain('mono')
      expect.soft(value.style.fontSize, `${key}: value size`).toBe('12.5px')
      expect.soft(value.style.color, `${key}: value colour`).toBe('var(--fg-2)')
      expect.soft(value.style.lineHeight, `${key}: value leading`).toBe('1.45')
    }

    const foot = screen.getByText(/A file that failed to import never produces an invoice/)
    expect.soft(foot.style.fontSize).toBe('12px')
    expect.soft(foot.style.color).toBe('var(--fg-3)')
    expect.soft(foot.style.lineHeight).toBe('1.55')
  })

  it('the error chip names the real failure', () => {
    const cases: Array<[ApiError | null, string]> = [
      [new ApiError('http', 'boom', 503), 'HTTP 503'],
      [new ApiError('http', 'boom', 404), 'HTTP 404'],
      [new ApiError('malformed', 'boom'), 'MALFORMED RESPONSE'],
      [new ApiError('network', 'boom'), 'NETWORK ERROR'],
      [null, 'NETWORK ERROR'],
    ]
    for (const [error, line] of cases) {
      render(<FailedCanvas error={error} onRetry={vi.fn()} />)
      expect(screen.getByTestId('source-document-failure-line').textContent).toBe(line)
      cleanup()
    }
  })

  it('the error chip and the none block follow v2', () => {
    failed()
    const chip = screen.getByTestId('source-document-failure-line')
    expect(chip.textContent, 'control: the chip is read').toBe('HTTP 503')
    expect(chip.className, 'pin').toContain('mono')
    expect.soft(chip.style.fontSize).toBe('11px')
    expect.soft(chip.style.color).toBe('var(--fg-2)')
    expect.soft(chip.style.background).toBe('var(--bg-2)')
    expect.soft(chip.style.border).toContain('--line-2')
    expect.soft(chip.style.borderRadius).toBe('var(--radius-md)')
    expect.soft(chip.style.padding).toBe('8px 11px')
    expect.soft(chip.style.marginBottom).toBe('20px')

    const retry = screen.getByTestId('source-document-retry')
    expect(retry.className, 'pin').toContain('v2-btn')
    expect.soft(retry.className.split(/\s+/), 'ghost: v2-btn alone has no border').toContain('v2-btn-ghost')
    expect.soft(retry.style.height).toBe('36px')
    cleanup()

    none()
    const label = screen.getByText('Why this is not an error')
    const block = label.parentElement as HTMLElement
    expect(block.style.border, 'pin: dashed on --line-3').toBe('1px dashed var(--line-3)')
    expect.soft(block.style.borderRadius, 'pin').toBe('var(--radius-md)')
    expect.soft(block.style.padding).toBe('16px 18px')
    expect.soft(block.style.background).toBe('var(--bg-2)')
    expect.soft(label.className.split(/\s+/), 'label class').toContain('label')
    expect.soft(label.style.marginBottom).toBe('8px')
    const text = screen.getByText(/A source document can only arrive through an import run/)
    expect.soft(text.style.fontSize).toBe('12.5px')
    expect.soft(text.style.color).toBe('var(--fg-2)')
    expect.soft(text.style.lineHeight).toBe('1.6')
  })
})
