import { execFile } from 'node:child_process'
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

vi.mock('node:child_process', async (orig) => ({ ...(await orig<typeof import('node:child_process')>()), execFile: vi.fn() }))

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

    expect(usageError(() => parseMeasureArgs([''], { props: 'width' })).code, 'an empty selector').toBe(2)
    for (const props of [',', ' , ,', '']) {
      expect(usageError(() => parseMeasureArgs(['.x'], { props })).code, `props "${props}"`).toBe(2)
    }
    expect(usageError(() => parseMeasureArgs(['.x'], { props: 'width', viewport: '' })).code, 'an empty --viewport').toBe(2)
    expect(parseMeasureArgs(['.x'], { props: 'width,, padding-left ,' }).props, 'empty entries are dropped').toEqual(['width', 'padding-left'])
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
  const removedTags: { content?: string }[] = []
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
    addStyleTag: async (o: { content?: string }) => {
      calls.push('addStyleTag')
      styleTags.push(o)
      return { evaluate: async (fn: (el: { remove: () => void }) => unknown) => fn({ remove: () => removedTags.push(o) }) }
    },
    locator: (selector: string) => (selectors.push(selector), locatorFor(elements)),
    evaluate: async (fn: (arg?: unknown) => unknown, arg?: unknown) => fn(arg),
  }
  return { page, calls, selectors, styleTags, removedTags, viewports }
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

  it('the scroll-behavior tag is removed after a read, a zero-match and a throw', async () => {
    const { doc } = fakeDoc()
    const read = fakePage([fakeElement(doc, () => ({ x: 0, y: 0, width: 1, height: 1 }))])
    await compile(req())(read.page)
    expect(read.removedTags, 'the tag stayed on the live page after a read').toEqual(read.styleTags)

    const none = fakePage([])
    await compile(req())(none.page)
    expect(none.removedTags, 'the tag stayed on the live page after a zero-match').toEqual(none.styleTags)

    const boom = fakePage([
      fakeElement(doc, () => {
        throw new Error('detached')
      }),
    ])
    await expect(compile(req())(boom.page)).rejects.toThrow('detached')
    expect(boom.removedTags, 'the tag stayed on the live page after a throw').toEqual(boom.styleTags)
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
    for (const selector of [`[data-x="a'b"]`, 'a\\b"c', 'x y`${1}`', 'a\u2028b\u2029c', 'a\nb', '</script>', "'; throw 1; '"]) {
      const { page, selectors } = fakePage([fakeElement(doc, () => ({ x: 0, y: 0, width: 1, height: 1 }))])
      await compile(req({ selector }))(page)
      expect(selectors, selector).toEqual([selector])
    }
  })
})

describe('measureSnippet edge cases', () => {
  const box = () => ({ x: 0, y: 0, width: 1, height: 1 })

  it('the viewport height falls back to 900 when the page reports none', async () => {
    const { doc } = fakeDoc()
    const f = fakePage([fakeElement(doc, box)])
    f.page.viewportSize = (() => null) as never
    await compile(req({ viewport: 1440 }))(f.page)
    expect(f.viewports).toEqual([{ width: 1440, height: 900 }])
  })

  it('an unknown property is refused before counting or settling', async () => {
    const { doc } = fakeDoc()
    const f = fakePage([fakeElement(doc, box)])
    const result = await compile(req({ props: ['widht'] }))(f.page)
    expect(result).toEqual({ error: 'unknown-prop', props: ['widht'] })
    expect(f.calls, 'nothing was counted or read').toEqual([])

    const none = fakePage([])
    expect(await compile(req({ props: ['widht'] }))(none.page), 'refused even with zero matches').toEqual({ error: 'unknown-prop', props: ['widht'] })
  })

  it('every match is settled, not only the first', async () => {
    const { doc, animations } = fakeDoc()
    const slide = deferred()
    const first = fakeElement(doc, box)
    let settled = false
    const second = fakeElement(doc, () => ({ x: settled ? 5 : 99, y: 0, width: 1, height: 1 }))
    animations.push(animation(second, 200, slide.promise))
    const { page } = fakePage([first, second])

    const run = compile(req())(page)
    await sleep(50)
    expect(first.reads.rect, 'the first match was read').toBe(1)
    expect(second.reads.rect, 'the second match waits for its own animation').toBe(0)

    settled = true
    slide.resolve()
    const result = await run
    expect(result.matches.map((m: { box: { x: number } }) => m.box.x)).toEqual([0, 5])
  })

  it('an animation on an unrelated element does not block the read', async () => {
    const { doc, animations } = fakeDoc()
    const el = fakeElement(doc, box)
    animations.push(animation({ contains: () => false }, 5_000, new Promise(() => {})))
    const { page } = fakePage([el])

    const result = await Promise.race([compile(req())(page), sleep(100).then(() => 'blocked')])
    expect(result).not.toBe('blocked')
    expect(result.matches).toHaveLength(1)
  })

  it('a cancelled animation does not hang or fail the read', async () => {
    const { doc, animations } = fakeDoc()
    const el = fakeElement(doc, box)
    animations.push({
      effect: { target: el, getComputedTiming: () => ({ endTime: 200 }) },
      get finished() {
        return Promise.reject(new Error('AbortError'))
      },
    })
    const { page } = fakePage([el])

    const result = await Promise.race([compile(req())(page), sleep(100).then(() => 'blocked')])
    expect(result).not.toBe('blocked')
    expect(result.matches).toHaveLength(1)
  })

  it('property names with quotes survive into the read', async () => {
    const { doc } = fakeDoc()
    const prop = `--a'b"c\`d `
    const { page } = fakePage([fakeElement(doc, box, { [prop]: 'v' })])
    const result = await compile(req({ props: [prop] }))(page)
    expect(result.matches).toHaveLength(1)
    expect(result.matches[0].styles).toEqual({ [prop]: 'v' })
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

    const dead = await rejection(measure(req({ session: 'qa' }), fakeExec({ code: 1, stdout: '', stderr: "Error: Browser 'qa' is not open. Run\n\n  playwright-cli -s=qa open\n\nto start the browser session." }).exec))
    expect(dead.hint, 'a session whose daemon is gone reads "Browser \'qa\' is not open" (playwright-core cli-client/session.js)').toContain('state-load')
  })

  it('a selector that reads "is not open" is not a closed session', async () => {
    const sel = '[title="is not open"]'
    const zero = fakeExec({ code: 0, stdout: JSON.stringify({ error: 'zero-match', selector: sel }) })
    const err = await rejection(measure(req({ selector: sel }), zero.exec))
    expect(err.message).toContain('no element matches')

    const ok = fakeExec({ code: 0, stdout: JSON.stringify({ selector: sel, viewport: { width: 1, height: 1 }, layoutWidth: 1, count: 0, matches: [] }) })
    await expect(measure(req({ selector: sel }), ok.exec)).resolves.toMatchObject({ selector: sel })

    const broken = fakeExec({ code: 1, stdout: `### Error\nTimeoutError: locator.count: ${sel} is not open` })
    const failed = await rejection(measure(req({ selector: sel }), broken.exec))
    expect(failed.hint, 'a failed snippet is not a closed session').not.toContain('state-load')
  })

  it("a snippet error carries playwright-cli's message", async () => {
    const { exec } = fakeExec({ code: 1, stdout: '### Error\nTimeoutError: locator.evaluate: Timeout' })
    const err = await rejection(measure(req(), exec))
    expect(err.code).toBe(1)
    expect(err.message).toContain('TimeoutError')
  })
})

describe('measure output edge cases', () => {
  it('output that is not JSON exits 1 and points at a snapshot', async () => {
    for (const stdout of ['', 'garbage', '<html>']) {
      const err = await rejection(measure(req({ session: 'qa' }), fakeExec({ code: 0, stdout }).exec))
      expect(err.code, `stdout "${stdout}"`).toBe(1)
      expect(err.hint, `stdout "${stdout}"`).toContain('snapshot')
    }
  })

  it('a failed spawn reports stderr when stdout is empty', async () => {
    const err = await rejection(measure(req(), fakeExec({ code: 1, stdout: '', stderr: 'spawn ENOENT' }).exec))
    expect(err.code).toBe(1)
    expect(err.message).toContain('ENOENT')

    const closed = await rejection(measure(req({ session: 'qa' }), fakeExec({ code: 1, stdout: '', stderr: "The browser 'qa' is not open" }).exec))
    expect(closed.hint, 'a closed session on stderr gets the open hint').toContain('state-load')
  })

  it('an unknown property error names every property and the fix', async () => {
    const err = await rejection(measure(req({ props: ['widht'] }), fakeExec({ code: 0, stdout: '{"error":"unknown-prop","props":["widht","heigth"]}' }).exec))
    expect(err.code).toBe(2)
    expect(err.message).toContain('widht')
    expect(err.message).toContain('heigth')
    expect(err.hint).toContain('padding-left')
  })

  it('the snippet the call sends runs with the request viewport, selector and props', async () => {
    const selector = `[a="b'c"]`
    const { exec, calls } = fakeExec({ code: 0, stdout: '{"selector":".x","count":0,"matches":[]}' })
    await measure(req({ selector, props: ['width', '--k'], viewport: 1440 }), exec)

    const { doc } = fakeDoc()
    const f = fakePage([fakeElement(doc, () => ({ x: 0, y: 0, width: 1, height: 1 }), { width: '1px', '--k': 'v' })])
    const sent = new Function('return (' + calls[0]![3] + ')')() as (page: unknown) => Promise<any>
    const result = await sent(f.page)
    expect(f.viewports).toEqual([{ width: 1440, height: 800 }])
    expect(f.selectors).toEqual([selector])
    expect(result.matches[0].styles).toEqual({ width: '1px', '--k': 'v' })
  })
})

describe('measure spawn and hints', () => {
  it('the default spawn runs in e2e/ with the update notifier off and the parent env kept', async () => {
    vi.stubEnv('PW_QA_MARK', 'kept')
    let seen: { cwd?: string; env?: NodeJS.ProcessEnv } | undefined
    vi.mocked(execFile).mockImplementation(((_f: string, _a: string[], opts: typeof seen, cb: (e: null, o: string, s: string) => void) => {
      seen = opts
      cb(null, '{"selector":".x","count":0,"matches":[]}', '')
    }) as never)

    await measure(req())
    expect(seen, 'execFile was called').toBeDefined()
    expect(seen!.env!.NO_UPDATE_NOTIFIER).toBe('1')
    expect(seen!.env!.PW_QA_MARK, 'the parent env is kept').toBe('kept')
    expect(seen!.cwd).toBe(E2E_DIR)
  })

  it('a successful result that contains "### Error" is still a success', async () => {
    const measured = {
      selector: 'text=### Error',
      viewport: { width: 1440, height: 900 },
      layoutWidth: 1440,
      count: 1,
      matches: [{ index: 0, box: { x: 0, y: 0, width: 1, height: 1 }, styles: { content: '"### Error"' } }],
    }
    await expect(measure(req({ selector: 'text=### Error' }), fakeExec({ code: 0, stdout: JSON.stringify(measured) }).exec)).resolves.toEqual(measured)
  })

  it('the zero-match hint tells an agent to retry while the page draws', async () => {
    const err = await rejection(measure(req(), fakeExec({ code: 0, stdout: '{"error":"zero-match","selector":".x"}' }).exec))
    expect(err.hint).toContain('still drawing')
    expect(err.hint).toContain('retry')
  })

  it('the unknown-property hint shows the working custom-property form', async () => {
    const err = await rejection(measure(req(), fakeExec({ code: 0, stdout: '{"error":"unknown-prop","props":["widht"]}' }).exec))
    expect(err.hint).toContain('--props=--x')
  })
})
