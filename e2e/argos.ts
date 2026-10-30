import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import type { Locator, Page } from '@playwright/test'

export const ARGOS_WIDTHS = [1440, 375] as const
const HEIGHTS = { 1440: 900, 375: 812 } as const
export const SHOT_CAP_MS = 30_000
export const ARGOS_ROOT = fileURLToPath(new URL('./argos-shots', import.meta.url))

const MONTH = '(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*'
export const MOVING_TEXT = new RegExp(
  [
    `\\b\\d{1,2} ${MONTH},? \\d{4}\\b`,
    `\\b${MONTH} \\d{1,2},? \\d{4}\\b`,
    '\\b\\d{4}-\\d{2}-\\d{2}\\b',
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

function capped<T>(p: Promise<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('shot exceeded 30 s')), SHOT_CAP_MS)
    p.then(resolve, reject).finally(() => clearTimeout(t))
  })
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
  info.setTimeout(info.timeout + SHOT_CAP_MS * ARGOS_WIDTHS.length)
  const timeout = info.timeout
  const original = page.viewportSize()
  try {
    for (const width of ARGOS_WIDTHS) {
      const name = `${app}/${screen}/${width}`
      try {
        await page.setViewportSize({ width, height: HEIGHTS[width] })
        await capped(
          argosScreenshot(page, name, {
            root: ARGOS_ROOT,
            fullPage: true,
            mask: [moving(page), ...(opts.mask ?? [])],
          }),
        )
      } catch (err) {
        info.setTimeout(timeout)
        const reason = String((err as Error)?.message ?? err).split('\n')[0]
        const file = `${ARGOS_ROOT}/${info.project.name}/${name}.failed.txt`
        await mkdir(file.slice(0, file.lastIndexOf('/')), { recursive: true })
        await writeFile(file, `${reason}\n`)
      }
    }
  } finally {
    if (original) {
      await page.setViewportSize(original)
      await page.evaluate(
        () => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))),
      )
    }
  }
}
