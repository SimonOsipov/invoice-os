import { dirname, isAbsolute } from 'node:path'
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const argosScreenshot = vi.fn()
const info = {
  timeout: 300_000,
  project: { name: 'unit-argos' },
  setTimeout(n: number) {
    this.timeout = n
  },
}
vi.mock('@argos-ci/playwright', () => ({ argosScreenshot: (...a: unknown[]) => argosScreenshot(...a) }))
vi.mock('@playwright/test', () => ({ test: { info: () => info } }))

const { ARGOS_ROOT, MOVING_TEXT, moving, reasonOf, shoot } = await import('./argos')

const MOVING = Symbol('moving')
function fakePage() {
  const sizes: Array<{ width: number; height: number }> = []
  const page = {
    viewportSize: () => ({ width: 1280, height: 720 }),
    setViewportSize: vi.fn(async (s: { width: number; height: number }) => void sizes.push(s)),
    evaluate: vi.fn(async () => {}),
    locator: () => ({ filter: () => MOVING }),
  }
  return { page: page as never, sizes }
}
const failedFile = (w: number) => `${ARGOS_ROOT}/unit-argos/app/firm-invoices/${w}.failed.txt`

beforeEach(() => {
  process.env.ARGOS_SHOTS = '1'
  argosScreenshot.mockReset()
  argosScreenshot.mockResolvedValue([])
  info.timeout = 300_000
})
afterEach(async () => {
  delete process.env.ARGOS_SHOTS
  vi.useRealTimers()
  vi.restoreAllMocks()
  await rm(`${ARGOS_ROOT}/unit-argos`, { recursive: true, force: true })
})

describe('shoot', () => {
  it('shoot is inert without the flag', async () => {
    process.env.ARGOS_SHOTS = ''
    const boom = new Proxy({}, { get: () => () => { throw new Error('touched') } })
    await expect(shoot(boom as never, 'app', 'firm-invoices')).resolves.toBeUndefined()
    expect(argosScreenshot).not.toHaveBeenCalled()
  })

  it('shoot names each width', async () => {
    await shoot(fakePage().page, 'app', 'firm-invoices')
    expect(argosScreenshot.mock.calls.map((c) => c[1])).toEqual([
      'app/firm-invoices/1440',
      'app/firm-invoices/375',
    ])
  })

  it('shoot sets each viewport then restores', async () => {
    const { page, sizes } = fakePage()
    await shoot(page, 'app', 'firm-invoices')
    expect(sizes).toEqual([
      { width: 1440, height: 900 },
      { width: 375, height: 812 },
      { width: 1280, height: 720 },
    ])
  })

  it('shoot extends the test timeout per width', async () => {
    await shoot(fakePage().page, 'app', 'firm-invoices')
    expect(info.timeout).toBe(390_000)
  })

  it("shoot passes the spec's masks after the moving mask", async () => {
    const a = { id: 'a' }
    await shoot(fakePage().page, 'app', 'firm-invoices', { mask: [a as never] })
    expect(argosScreenshot.mock.calls[0][2].mask).toEqual([MOVING, a])
  })

  it('shoot passes an absolute root and fullPage', async () => {
    vi.spyOn(process, 'cwd').mockReturnValue('/')
    await shoot(fakePage().page, 'app', 'firm-invoices')
    const o = argosScreenshot.mock.calls[0][2]
    expect(isAbsolute(o.root)).toBe(true)
    expect(o.root.endsWith('e2e/argos-shots')).toBe(true)
    expect(o.fullPage).toBe(true)
    argosScreenshot.mockClear()
    await shoot(fakePage().page, 'app', 'firm-invoices', { fullPage: false })
    expect(argosScreenshot.mock.calls.map((c) => c[2].fullPage)).toEqual([false, false])
  })

  it('a failed shot leaves the test running', async () => {
    argosScreenshot.mockRejectedValueOnce(new Error('Failed to stabilize\nsecond line'))
    await expect(shoot(fakePage().page, 'app', 'firm-invoices')).resolves.toBeUndefined()
    expect(await readFile(failedFile(1440), 'utf8')).toBe('Failed to stabilize second line\n')
    expect(argosScreenshot.mock.calls.map((c) => c[1])).toEqual([
      'app/firm-invoices/1440',
      'app/firm-invoices/375',
    ])
  })

  it('a stalled shot is capped', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    argosScreenshot.mockReturnValueOnce(new Promise(() => {}))
    const done = shoot(fakePage().page, 'app', 'firm-invoices')
    await vi.advanceTimersByTimeAsync(30_000)
    await expect(done).resolves.toBeUndefined()
    vi.useRealTimers()
    expect(await readFile(failedFile(1440), 'utf8')).toBe('shot exceeded 30 s\n')
    expect(argosScreenshot).toHaveBeenCalledTimes(2)
  })

  it('a late shot deletes its PNG and metadata and keeps the failure', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const png = `${ARGOS_ROOT}/unit-argos/app/firm-invoices/1440.png`
    let finish: () => void = () => {}
    argosScreenshot.mockImplementationOnce(
      () =>
        new Promise<void>((r) => {
          finish = r
        }),
    )
    const done = shoot(fakePage().page, 'app', 'firm-invoices')
    await vi.advanceTimersByTimeAsync(30_000)
    await done
    vi.useRealTimers()
    await mkdir(dirname(png), { recursive: true })
    await writeFile(png, 'x')
    await writeFile(`${png}.argos.json`, '{}')
    finish()
    await vi.waitFor(async () => {
      await expect(readFile(png)).rejects.toThrow()
      await expect(readFile(`${png}.argos.json`)).rejects.toThrow()
    })
    expect(await readFile(failedFile(1440), 'utf8')).toBe('shot exceeded 30 s\n')
  })

  it('a shot in time keeps its PNG and metadata', async () => {
    const png = `${ARGOS_ROOT}/unit-argos/app/firm-invoices/1440.png`
    argosScreenshot.mockImplementationOnce(async () => {
      await mkdir(dirname(png), { recursive: true })
      await writeFile(png, 'x')
      await writeFile(`${png}.argos.json`, '{}')
    })
    await shoot(fakePage().page, 'app', 'firm-invoices')
    await new Promise((r) => setTimeout(r, 50))
    expect(await readFile(png, 'utf8')).toBe('x')
    expect(await readFile(`${png}.argos.json`, 'utf8')).toBe('{}')
  })

  it('a hung viewport restore is capped', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { page } = fakePage()
    let n = 0
    ;(page as never as { setViewportSize: unknown }).setViewportSize = vi.fn(() => (++n === 3 ? new Promise(() => {}) : Promise.resolve()))
    const done = shoot(page, 'app', 'firm-invoices')
    await vi.advanceTimersByTimeAsync(30_000)
    await expect(done).resolves.toBeUndefined()
    vi.useRealTimers()
    const f = `${ARGOS_ROOT}/unit-argos/app/firm-invoices/restore.failed.txt`
    expect(await readFile(f, 'utf8')).toBe('shot exceeded 30 s\n')
  })

  it('a failed viewport restore leaves shoot resolved', async () => {
    const { page } = fakePage()
    let n = 0
    ;(page as never as { setViewportSize: unknown }).setViewportSize = vi.fn(async () => {
      if (++n === 3) throw new Error('restore boom')
    })
    await expect(shoot(page, 'app', 'firm-invoices')).resolves.toBeUndefined()
    const f = `${ARGOS_ROOT}/unit-argos/app/firm-invoices/restore.failed.txt`
    expect(await readFile(f, 'utf8')).toBe('restore boom\n')
  })

  it('reasonOf keeps the SDK cause on one capped line', () => {
    const sdk = 'Failed to stabilize screenshot, found the following issues:\n- 2 fonts are loading'
    expect(reasonOf(new Error(sdk))).toBe(
      'Failed to stabilize screenshot, found the following issues: - 2 fonts are loading',
    )
    expect(reasonOf(new Error('x'.repeat(500)))).toHaveLength(300)
    expect(reasonOf(new Error('  a  \n\n  b  \n'))).toBe('a b')
  })

  it('a failed shot restores the timeout and the viewport', async () => {
    let before = 0
    argosScreenshot.mockImplementationOnce(async () => {
      before = info.timeout
      info.setTimeout(before * 3)
      throw new Error('boom')
    })
    const { page, sizes } = fakePage()
    await shoot(page, 'app', 'firm-invoices')
    expect(info.timeout).toBe(before)
    expect(sizes.at(-1)).toEqual({ width: 1280, height: 720 })
  })
})

describe('MOVING_TEXT', () => {
  it('MOVING_TEXT matches moving text', () => {
    for (const t of [
      '30 Oct 2026',
      '30 October 2026',
      '30/10/2026',
      '30/10/2026, 14:02:11',
      '30 Oct',
      'since 5 Jan',
      'Oct 30, 2026',
      '2026-10-30',
      '14:02',
      '3h ago',
      'just now',
      '0b8c1d2e-3f4a-4b5c-8d6e-7f8091a2b3c4',
      '83d9d017',
      '1761823000000',
      'abcdefab-abcd-abcd-abcd-abcdefabcdef',
      'ref x1761823000000y',
      `id ${'7'.repeat(41)}`,
    ]) expect(MOVING_TEXT.test(t), t).toBe(true)
  })

  it('MOVING_TEXT leaves fixed text', () => {
    for (const t of ['Invoices', 'Overview', '₦1,250,000', 'Rules', '1440', '5 marketplaces', '₦1,250,000.00', 'Defaced'])
      expect(MOVING_TEXT.test(t), t).toBe(false)
  })

  it('moving selects leaf elements by text', () => {
    const calls: unknown[] = []
    const page = { locator: (s: string) => ({ filter: (o: unknown) => calls.push(s, o) }) }
    moving(page as never)
    expect(calls).toEqual([':not(:has(*))', { hasText: MOVING_TEXT }])
  })
})
