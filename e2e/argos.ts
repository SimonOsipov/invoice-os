import { mkdir, rm, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import type { Locator, Page } from '@playwright/test'

export const ARGOS_WIDTHS = [1440, 375] as const
const HEIGHTS = { 1440: 900, 375: 812 } as const
export const SHOT_CAP_MS = 30_000
export const ARGOS_ROOT = fileURLToPath(new URL('./argos-shots', import.meta.url))

const MONTH = '(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*'
const MONTH_ABBR = '(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)'
export const MOVING_TEXT = new RegExp(
  [
    `\\b\\d{1,2} ${MONTH},? \\d{4}\\b`,
    `\\b${MONTH} \\d{1,2},? \\d{4}\\b`,
    '\\b\\d{4}-\\d{2}-\\d{2}\\b',
    '\\b\\d{1,2}/\\d{1,2}/\\d{4}\\b',
    `\\b\\d{1,2} ${MONTH_ABBR}\\b`,
    '\\b\\d{1,2}:\\d{2}\\b',
    '\\b\\d+ ?(?:s|sec|m|min|h|hr|d|w|mo|y) ago\\b',
    '\\bjust now\\b',
    '\\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\\b',
    '\\b(?=[0-9a-f]*\\d)[0-9a-f]{7,40}\\b',
    '\\d{10,}',
  ].join('|'),
  'i',
)

export const argosOn = (): boolean => !!process.env.ARGOS_SHOTS

export function moving(page: Page): Locator {
  return page.locator(':not(:has(*))').filter({ hasText: MOVING_TEXT })
}

// The SDK has no option to bound its wait, so a late settle deletes what it wrote.
function capped<T>(p: Promise<T>, onLate: () => Promise<void>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    let late = false
    const t = setTimeout(() => {
      late = true
      reject(new Error('shot exceeded 30 s'))
    }, SHOT_CAP_MS)
    p.then(resolve, reject).finally(() => {
      clearTimeout(t)
      if (late) onLate().catch(() => {})
    })
  })
}

/** reasonOf: the error's lines joined into one line of at most 300 characters. */
export function reasonOf(err: unknown): string {
  const text = String((err as Error)?.message ?? err)
  return text.split('\n').map((l) => l.trim()).filter(Boolean).join(' ').slice(0, 300)
}

async function fail(project: string, name: string, err: unknown): Promise<void> {
  const file = `${ARGOS_ROOT}/${project}/${name}.failed.txt`
  await mkdir(file.slice(0, file.lastIndexOf('/')), { recursive: true })
  await writeFile(file, `${reasonOf(err)}\n`)
}

export async function shoot(
  page: Page,
  app: string,
  screen: string,
  opts: { mask?: Locator[] } = {},
): Promise<void> {
  if (!argosOn()) return
  const [{ argosScreenshot }, { test }] = await Promise.all([
    import('@argos-ci/playwright'),
    import('@playwright/test'),
  ])
  const info = test.info()
  info.setTimeout(info.timeout + SHOT_CAP_MS * (ARGOS_WIDTHS.length + 1))
  const timeout = info.timeout
  const original = page.viewportSize()
  try {
    for (const width of ARGOS_WIDTHS) {
      const name = `${app}/${screen}/${width}`
      try {
        await page.setViewportSize({ width, height: HEIGHTS[width] })
        const png = `${ARGOS_ROOT}/${info.project.name}/${name}.png`
        await capped(
          argosScreenshot(page, name, {
            root: ARGOS_ROOT,
            fullPage: true,
            mask: [moving(page), ...(opts.mask ?? [])],
          }),
          async () => {
            await Promise.all([png, `${png}.argos.json`].map((f) => rm(f, { force: true })))
          },
        )
      } catch (err) {
        info.setTimeout(timeout)
        await fail(info.project.name, name, err)
      }
    }
  } finally {
    if (original) {
      try {
        await capped(
          (async () => {
            await page.setViewportSize(original)
            await page.evaluate(
              () => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))),
            )
          })(),
          async () => {},
        )
      } catch (err) {
        await fail(info.project.name, `${app}/${screen}/restore`, err)
      }
    }
  }
}
