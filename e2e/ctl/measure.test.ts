import { existsSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CtlError } from './main'
import {
  measure,
  measureSnippet,
  parseMeasureArgs,
  playwrightCliCommand,
  type Exec,
  type ExecResult,
  type MeasureRequest,
} from './measure'

const E2E_DIR = path.resolve(fileURLToPath(new URL('..', import.meta.url)))
const req = (over: Partial<MeasureRequest> = {}): MeasureRequest => ({ selector: '.x', props: ['width'], session: 'default', ...over })

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

function usageError(fn: () => unknown): CtlError {
  try {
    fn()
  } catch (err) {
    expect(err, 'expected a CtlError').toBeInstanceOf(CtlError)
    return err as CtlError
  }
  throw new Error('expected the call to throw')
}

async function rejection(p: Promise<unknown>): Promise<CtlError> {
  const err = await p.then(() => null, (e: unknown) => e)
  expect(err, 'expected a CtlError rejection').toBeInstanceOf(CtlError)
  return err as CtlError
}

describe('parseMeasureArgs', () => {
  it('parseMeasureArgs reads selector, props and viewport', () => {
    vi.stubEnv('PLAYWRIGHT_CLI_SESSION', undefined)
    expect(parseMeasureArgs(['.pf-drawer'], { props: 'width, padding-left', viewport: '1440' })).toEqual({
      selector: '.pf-drawer',
      props: ['width', 'padding-left'],
      viewport: 1440,
      session: 'default',
    })
    expect(parseMeasureArgs(['.pf-drawer'], { props: 'width' }).viewport, 'no --viewport leaves it unset').toBeUndefined()

    vi.stubEnv('PLAYWRIGHT_CLI_SESSION', 'from-env')
    expect(parseMeasureArgs(['.x'], { props: 'width' }).session).toBe('from-env')
    expect(parseMeasureArgs(['.x'], { props: 'width', session: 'flag' }).session, '--session wins').toBe('flag')
  })

  it('missing selector, missing props and a bad width are usage errors', () => {
    const noSelector = usageError(() => parseMeasureArgs([], { props: 'width' }))
    expect(noSelector.code).toBe(2)

    const noProps = usageError(() => parseMeasureArgs(['.x'], {}))
    expect(noProps.code).toBe(2)
    expect(noProps.hint).toContain('Example:')

    for (const viewport of ['wide', '0', '-5', '14.5']) {
      expect(usageError(() => parseMeasureArgs(['.x'], { props: 'width', viewport })).code, `viewport ${viewport}`).toBe(2)
    }
    expect(parseMeasureArgs(['.x'], { props: 'width', viewport: '1280' }).viewport, 'a valid width passes').toBe(1280)
  })
})

// --- the snippet, run against a fake page ----------------------------------------
//
// The fake page records every call. Elements are plain objects: `fn(element, arg)` runs for real,
// and getComputedStyle / CSS / document are stubbed globals, as they are inside a browser.
// No browser is launched: the test:unit job installs none.

type Rect = { x: number; y: number; width: number; height: number }
type Anim = { effect: { target: unknown; getComputedTiming: () => { endTime: number } }; finished: Promise<unknown> }
type FakeEl = {
  ownerDocument: { getAnimations: () => Anim[] }
  contains: (n: unknown) => boolean
  getBoundingClientRect: () => Rect
  styles: Record<string, string>
  reads: { rect: number }
}

const deferred = () => {
  let resolve!: () => void
  const promise = new Promise<void>((r) => (resolve = r))
  return { promise, resolve }
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const KNOWN = new Set(['width', 'height', 'padding-left'])

function fakeDoc() {
  const animations: Anim[] = []
  return { animations, doc: { getAnimations: () => animations } }
}

function fakeElement(doc: FakeEl['ownerDocument'], rect: () => Rect, styles: Record<string, string> = {}): FakeEl {
  const reads = { rect: 0 }
  const el: FakeEl = {
    ownerDocument: doc,
    contains: (n) => n === el,
    getBoundingClientRect: () => (reads.rect++, rect()),
    styles,
    reads,
  }
  return el
}

const animation = (target: unknown, endTime: number, finished: Promise<unknown>): Anim => ({
  effect: { target, getComputedTiming: () => ({ endTime }) },
  finished,
})

function fakePage(elements: FakeEl[], opts: { viewport?: { width: number; height: number }; clientWidth?: number } = {}) {
  let viewport = opts.viewport ?? { width: 1280, height: 800 }
  const calls: string[] = []
  const selectors: string[] = []
  const styleTags: { content?: string }[] = []
  const viewports: { width: number; height: number }[] = []
  const locatorFor = (list: FakeEl[]) => ({
    count: async () => (calls.push('count'), list.length),
    nth: (i: number) => (calls.push('nth'), locatorFor([list[i]!])),
    evaluate: async (fn: (el: unknown, arg?: unknown) => unknown, arg?: unknown) => (calls.push('evaluate'), fn(list[0], arg)),
  })
  vi.stubGlobal('getComputedStyle', (el: FakeEl) => ({ getPropertyValue: (p: string) => el.styles[p] ?? '' }))
  vi.stubGlobal('CSS', { supports: (p: string) => KNOWN.has(p) })
  vi.stubGlobal('document', { documentElement: { clientWidth: opts.clientWidth ?? 1280 } })
  const page = {
    viewportSize: () => viewport,
    setViewportSize: async (v: { width: number; height: number }) => {
      calls.push('setViewportSize')
      viewports.push(v)
      viewport = v
    },
    addStyleTag: async (o: { content?: string }) => (calls.push('addStyleTag'), styleTags.push(o)),
    locator: (selector: string) => (selectors.push(selector), locatorFor(elements)),
    evaluate: async (fn: (arg?: unknown) => unknown, arg?: unknown) => fn(arg),
  }
  return { page, calls, selectors, styleTags, viewports }
}

// The snippet is `async page => { ... }`.
const compile = (r: MeasureRequest) => new Function('return (' + measureSnippet(r) + ')')() as (page: unknown) => Promise<any>

describe('measureSnippet', () => {
  it('zero matches returns at once without settling', async () => {
    const { page, calls } = fakePage([])
    const result = await compile(req({ selector: '.nope' }))(page)

    expect(calls, 'the snippet counted').toContain('count')
    expect(result).toEqual({ error: 'zero-match', selector: '.nope' })
    expect(calls).not.toContain('nth')
    expect(calls).not.toContain('evaluate')
  })

  it('smooth scrolling is off before counting', async () => {
    const { doc } = fakeDoc()
    const { page, calls, styleTags } = fakePage([fakeElement(doc, () => ({ x: 0, y: 0, width: 1, height: 1 }))])
    await compile(req())(page)

    expect(styleTags).toHaveLength(1)
    expect(styleTags[0]!.content).toContain('scroll-behavior:auto')
    expect(calls.indexOf('addStyleTag')).toBeGreaterThanOrEqual(0)
    expect(calls.indexOf('addStyleTag')).toBeLessThan(calls.indexOf('count'))
  })

  it('the viewport is set first, keeping the height', async () => {
    const { doc } = fakeDoc()
    const el = () => fakeElement(doc, () => ({ x: 0, y: 0, width: 1, height: 1 }))

    const withWidth = fakePage([el()], { viewport: { width: 1280, height: 800 } })
    const result = await compile(req({ viewport: 1440 }))(withWidth.page)
    expect(withWidth.viewports).toEqual([{ width: 1440, height: 800 }])
    expect(withWidth.calls[0]).toBe('setViewportSize')
    expect(result.viewport, 'the result reports the width the read ran at').toEqual({ width: 1440, height: 800 })

    const without = fakePage([el()])
    await compile(req())(without.page)
    expect(without.calls, 'the snippet counted').toContain('count')
    expect(without.calls).not.toContain('setViewportSize')
  })

  it('the read waits for a running finite animation', async () => {
    const { doc, animations } = fakeDoc()
    const slide = deferred()
    let settled = false
    const el = fakeElement(doc, () => (settled ? { x: 960, y: 0, width: 480, height: 900 } : { x: 1440, y: 0, width: 480, height: 900 }))
    animations.push(animation(el, 200, slide.promise))
    const { page } = fakePage([el])

    let done = false
    const run = compile(req())(page).finally(() => (done = true))
    await sleep(50)
    expect(el.reads.rect, 'the box was read mid-slide').toBe(0)
    expect(done, 'the snippet returned mid-slide').toBe(false)

    settled = true
    slide.resolve()
    const result = await run
    expect(result.matches).toHaveLength(1)
    expect(result.matches[0].box).toEqual({ x: 960, y: 0, width: 480, height: 900 })
  })

  it('the read waits for an animation on an ancestor', async () => {
    const { doc, animations } = fakeDoc()
    const slide = deferred()
    let settled = false
    const el = fakeElement(doc, () => ({ x: settled ? 960 : 1440, y: 0, width: 480, height: 900 }))
    const ancestor = { contains: (n: unknown) => n === ancestor || n === el }
    animations.push(animation(ancestor, 200, slide.promise))
    const { page } = fakePage([el])

    const run = compile(req())(page)
    await sleep(50)
    expect(el.reads.rect, 'the box was read mid-slide').toBe(0)

    settled = true
    slide.resolve()
    const result = await run
    expect(result.matches).toHaveLength(1)
    expect(result.matches[0].box.x).toBe(960)
  })

  it('an infinite animation does not block the read', async () => {
    const { doc, animations } = fakeDoc()
    const el = fakeElement(doc, () => ({ x: 0, y: 0, width: 10, height: 10 }))
    animations.push(animation(el, Infinity, new Promise(() => {})))
    const { page } = fakePage([el])

    const result = await Promise.race([compile(req())(page), sleep(100).then(() => 'blocked')])
    expect(result).not.toBe('blocked')
    expect(result.matches).toHaveLength(1)
  })

  it('styles and box come from the element', async () => {
    const { doc } = fakeDoc()
    const first = fakeElement(doc, () => ({ x: 960, y: 0, width: 480, height: 900 }), { width: '480px', 'padding-left': '24px', '--brand': '#123' })
    const second = fakeElement(doc, () => ({ x: 10, y: 20, width: 30, height: 40 }), { width: '30px', 'padding-left': '0px', '--brand': '#456' })
    const { page } = fakePage([first, second], { clientWidth: 1440, viewport: { width: 1440, height: 900 } })

    const result = await compile(req({ props: ['width', 'padding-left', '--brand'] }))(page)

    expect(result.selector).toBe('.x')
    expect(result.layoutWidth).toBe(1440)
    expect(result.count).toBe(2)
    expect(result.matches).toEqual([
      { index: 0, box: { x: 960, y: 0, width: 480, height: 900 }, styles: { width: '480px', 'padding-left': '24px', '--brand': '#123' } },
      { index: 1, box: { x: 10, y: 20, width: 30, height: 40 }, styles: { width: '30px', 'padding-left': '0px', '--brand': '#456' } },
    ])
  })

  it('an unknown property name is refused', async () => {
    const { doc } = fakeDoc()
    const { page } = fakePage([fakeElement(doc, () => ({ x: 0, y: 0, width: 1, height: 1 }))])

    const result = await compile(req({ props: ['widht', '--brand', 'width'] }))(page)
    expect(result).toEqual({ error: 'unknown-prop', props: ['widht'] })

    const ok = await compile(req({ props: ['--brand', 'width'] }))(page)
    expect(ok.error, 'custom and known properties pass').toBeUndefined()
    expect(ok.matches).toHaveLength(1)
  })

  it('a selector with quotes survives', async () => {
    const { doc } = fakeDoc()
    for (const selector of [`[data-x="a'b"]`, 'a\\b"c', 'x y`${1}`']) {
      const { page, selectors } = fakePage([fakeElement(doc, () => ({ x: 0, y: 0, width: 1, height: 1 }))])
      await compile(req({ selector }))(page)
      expect(selectors, selector).toEqual([selector])
    }
  })
})

// --- measure(): the playwright-cli call layer ------------------------------------

function fakeExec(result: ExecResult) {
  const calls: string[][] = []
  const exec: Exec = async (args) => (calls.push(args), result)
  return { exec, calls }
}

describe('measure', () => {
  it('measure calls the e2e-local playwright-cli in the named session', async () => {
    const r = req({ selector: '.pf-drawer', session: 'qa', viewport: 1440 })
    const { exec, calls } = fakeExec({ code: 0, stdout: '{"selector":".pf-drawer","count":0,"matches":[]}\n' })
    await measure(r, exec)

    expect(calls).toHaveLength(1)
    const args = calls[0]!
    expect(args.slice(0, 3)).toEqual(['-s=qa', '--raw', 'run-code'])
    expect(args).toHaveLength(4)
    expect(args[3]).toContain(JSON.stringify('.pf-drawer'))
  })

  it('playwrightCliCommand points at the e2e-local binary', () => {
    const cmd = playwrightCliCommand(['-s=qa', 'list'])
    expect(cmd.file).toBe(process.execPath)
    expect(cmd.args[0]).toMatch(/@playwright[\\/]cli[\\/]playwright-cli\.js$/)
    expect(existsSync(cmd.args[0]!), 'the resolved binary exists').toBe(true)
    expect(cmd.args.slice(1)).toEqual(['-s=qa', 'list'])
    expect(cmd.cwd).toBe(E2E_DIR)
    expect(path.basename(cmd.cwd)).toBe('e2e')
  })

  it('a zero-match result exits 1 naming the selector', async () => {
    const { exec } = fakeExec({ code: 0, stdout: '{"error":"zero-match","selector":".x"}' })
    const err = await rejection(measure(req(), exec))
    expect(err.code).toBe(1)
    expect(err.message).toContain('.x')
    expect(err.hint).toContain('snapshot')
  })

  it('an unknown property from the page exits 2 naming it', async () => {
    const { exec } = fakeExec({ code: 0, stdout: '{"error":"unknown-prop","props":["widht"]}' })
    const err = await rejection(measure(req({ props: ['widht', 'width'] }), exec))
    expect(err.code).toBe(2)
    expect(err.message).toContain('widht')
  })

  it('a measurement is returned as parsed JSON', async () => {
    const measured = {
      selector: '.x',
      viewport: { width: 1440, height: 900 },
      layoutWidth: 1440,
      count: 1,
      matches: [{ index: 0, box: { x: 960, y: 0, width: 480, height: 900 }, styles: { width: '480px' } }],
    }
    const { exec } = fakeExec({ code: 0, stdout: JSON.stringify(measured) + '\n' })
    await expect(measure(req(), exec)).resolves.toEqual(measured)
  })

  it('a closed session explains how to open it', async () => {
    const { exec } = fakeExec({ code: 1, stdout: "The browser 'qa' is not open, please run open first" })
    const err = await rejection(measure(req({ session: 'qa' }), exec))
    expect(err.code).toBe(1)
    expect(err.hint).toContain('open')
    expect(err.hint).toContain('state-load')
    expect(err.hint).toContain('ctl login')
  })

  it("a snippet error carries playwright-cli's message", async () => {
    const { exec } = fakeExec({ code: 1, stdout: '### Error\nTimeoutError: locator.evaluate: Timeout' })
    const err = await rejection(measure(req(), exec))
    expect(err.code).toBe(1)
    expect(err.message).toContain('TimeoutError')
  })
})
