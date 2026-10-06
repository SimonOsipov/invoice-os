import { execFile } from 'node:child_process'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { settleAnimations } from '../topology/layout'
import { CtlError } from './main'

export interface MeasureRequest {
  selector: string
  props: string[]
  viewport?: number
  session: string
}

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

export interface MeasureResult {
  selector: string
  viewport: { width: number; height: number }
  layoutWidth: number
  count: number
  matches: { index: number; box: Box; styles: Record<string, string> }[]
}

// `args` are playwright-cli arguments, without the binary.
export interface ExecResult {
  code: number
  stdout: string
  stderr?: string
}
export type Exec = (args: string[]) => Promise<ExecResult>

const EXAMPLE = `Example: pnpm -s --filter @invoice-os/e2e ctl measure '[data-testid="evidence-bundle-drawer"]' --props width,padding-left --viewport 1440`

export function parseMeasureArgs(positionals: string[], flags: Record<string, string | undefined>): MeasureRequest {
  const selector = positionals[0]
  if (!selector) throw new CtlError('measure needs a selector', EXAMPLE, 2)
  if (!flags.props) throw new CtlError('measure needs --props', EXAMPLE, 2)
  const props = flags.props.split(',').map((p) => p.trim()).filter(Boolean)
  if (props.length === 0) throw new CtlError('measure needs --props', EXAMPLE, 2)
  let viewport: number | undefined
  if (flags.viewport !== undefined) {
    viewport = Number(flags.viewport)
    if (!Number.isInteger(viewport) || viewport <= 0) {
      throw new CtlError(`--viewport must be a positive integer width, got "${flags.viewport}"`, EXAMPLE, 2)
    }
  }
  return { selector, props, viewport, session: flags.session ?? process.env.PLAYWRIGHT_CLI_SESSION ?? 'default' }
}

// Runs inside the browser session via `playwright-cli run-code`; no imports.
export function measureSnippet(req: MeasureRequest): string {
  return `async page => {
  ${settleAnimations.toString()}
  const selector = ${JSON.stringify(req.selector)}
  const props = ${JSON.stringify(req.props)}
  const width = ${JSON.stringify(req.viewport ?? null)}
  if (width !== null) await page.setViewportSize({ width, height: page.viewportSize()?.height ?? 900 })
  const unknown = await page.evaluate((ps) => ps.filter((p) => !p.startsWith('--') && !CSS.supports(p, 'initial')), props)
  if (unknown.length) return { error: 'unknown-prop', props: unknown }
  await page.addStyleTag({ content: '*,*::before,*::after{scroll-behavior:auto!important}' })
  const loc = page.locator(selector)
  const count = await loc.count()
  if (count === 0) return { error: 'zero-match', selector }
  const matches = []
  for (let index = 0; index < count; index++) {
    const el = loc.nth(index)
    await settleAnimations(el)
    const read = await el.evaluate((e, ps) => {
      const r = e.getBoundingClientRect()
      const cs = getComputedStyle(e)
      const styles = {}
      for (const p of ps) styles[p] = cs.getPropertyValue(p)
      return { box: { x: r.x, y: r.y, width: r.width, height: r.height }, styles }
    }, props)
    matches.push({ index, box: read.box, styles: read.styles })
  }
  const layoutWidth = await page.evaluate(() => document.documentElement.clientWidth)
  return { selector, viewport: page.viewportSize(), layoutWidth, count, matches }
}`
}

export function playwrightCliCommand(args: string[]): { file: string; args: string[]; cwd: string } {
  return {
    file: process.execPath,
    args: [createRequire(import.meta.url).resolve('@playwright/cli/playwright-cli.js'), ...args],
    cwd: path.resolve(fileURLToPath(new URL('..', import.meta.url))),
  }
}

const defaultExec: Exec = (args) => {
  const { file, args: argv, cwd } = playwrightCliCommand(args)
  return new Promise((resolve) => {
    execFile(file, argv, { cwd, env: { ...process.env, NO_UPDATE_NOTIFIER: '1' }, maxBuffer: 16 * 1024 * 1024 }, (err, stdout, stderr) => {
      const code = err ? (typeof err.code === 'number' ? err.code : 1) : 0
      resolve({ code, stdout, stderr })
    })
  })
}

export async function measure(req: MeasureRequest, exec: Exec = defaultExec): Promise<MeasureResult> {
  const r = await exec([`-s=${req.session}`, '--raw', 'run-code', measureSnippet(req)])
  const text = (r.stdout.trim() || (r.stderr ?? '').trim()) || `playwright-cli exited ${r.code}`
  if (/is not open/.test(text)) {
    throw new CtlError(
      text,
      `Open the session first: playwright-cli -s=${req.session} open <url>, then state-load <file> (ctl login prints it). Or run ctl login.`,
      1,
    )
  }
  if (r.code !== 0) {
    throw new CtlError(text, `The snippet failed in session ${req.session}. Check the page is loaded: playwright-cli -s=${req.session} snapshot`, 1)
  }
  let out: { error?: string; selector?: string; props?: string[] }
  try {
    out = JSON.parse(text)
  } catch {
    throw new CtlError(`unreadable playwright-cli output: ${text.slice(0, 300)}`, `Run playwright-cli -s=${req.session} snapshot`, 1)
  }
  if (out.error === 'zero-match') {
    throw new CtlError(`no element matches ${out.selector}`, `Run playwright-cli -s=${req.session} snapshot to see the page, and fix the selector. If the page is still drawing, wait for it and retry.`, 1)
  }
  if (out.error === 'unknown-prop') {
    throw new CtlError(`unknown CSS property: ${(out.props ?? []).join(', ')}`, 'Use a real property name (width, padding-left) or a custom property written as --props=--x.', 2)
  }
  return out as MeasureResult
}

export async function measureCommand(positionals: string[], flags: Record<string, string | undefined>): Promise<MeasureResult> {
  return measure(parseMeasureArgs(positionals, flags))
}
