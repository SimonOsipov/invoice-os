import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { CtlError, run } from './main'

const E2E_DIR = fileURLToPath(new URL('..', import.meta.url))
const COMMANDS = ['env', 'login', 'measure'] as const
const URL_VARS = ['LANDING_URL', 'APP_URL', 'GATEWAY_URL', 'OPS_CONSOLE_URL', 'SUPPORT_CONSOLE_URL']

describe('ctl run', () => {
  it('run --help lists the three commands', async () => {
    const r = await run(['--help'])
    expect(r.code).toBe(0)
    for (const name of COMMANDS) expect(r.stdout).toContain(name)
  })

  it('each command help has an example', async () => {
    for (const name of COMMANDS) {
      const r = await run([name, '--help'])
      expect(r.code, `${name} --help exit code`).toBe(0)
      expect(r.stdout, `${name} --help names the command`).toContain(name)
      expect(r.stdout, `${name} --help has an example`).toContain('Example:')
    }
  })

  it('a result prints as JSON on stdout', async () => {
    const r = await run(['env', 'pr-1'], { env: async () => ({ a: 1 }) })
    expect(r.code).toBe(0)
    expect(JSON.parse(r.stdout)).toEqual({ a: 1 })
    expect(r.stderr).toBe('')
  })

  it('a CtlError prints error and hint on stderr', async () => {
    const r = await run(['login', 'firm'], {
      login: async () => {
        throw new CtlError('x', 'do y', 1, { dark: ['app'] })
      },
    })
    expect(r.code).toBe(1)
    expect(r.stdout).toBe('')
    expect(JSON.parse(r.stderr)).toEqual({ error: 'x', hint: 'do y', dark: ['app'] })
  })

  it('an unexpected error still prints JSON', async () => {
    const r = await run(['measure', '.x', '--props', 'width'], {
      measure: async () => {
        throw new Error('boom')
      },
    })
    expect(r.code).toBe(1)
    expect(r.stdout).toBe('')
    expect(JSON.parse(r.stderr)).toMatchObject({
      error: 'boom',
      hint: expect.stringContaining('--help'),
    })
  })

  it('no arguments is a usage error', async () => {
    const r = await run([])
    expect(r.code).toBe(2)
    expect(JSON.parse(r.stderr)).toMatchObject({ hint: expect.stringContaining('ctl --help') })
  })

  it('an unknown command is a usage error naming it', async () => {
    const r = await run(['frobnicate'])
    expect(r.code).toBe(2)
    expect(JSON.parse(r.stderr)).toMatchObject({ error: expect.stringContaining('frobnicate') })
  })
})

describe('ctl entry under node', () => {
  const env = { ...process.env }
  for (const name of URL_VARS) delete env[name]

  it('the real entry runs under node with the resolve hook', () => {
    const r = spawnSync(
      process.execPath,
      ['--import', './ctl/tsResolve.mjs', './ctl/main.ts', '--help'],
      { cwd: E2E_DIR, env, encoding: 'utf8' },
    )
    expect(r.status, r.stderr).toBe(0)
    expect(r.stdout).toContain('login')
  })

  it('the resolve hook retries a relative extensionless specifier with .ts', () => {
    const r = spawnSync(
      process.execPath,
      [
        '--import',
        './ctl/tsResolve.mjs',
        '--input-type=module',
        '-e',
        "const m = await import('./ctl/main'); console.log(typeof m.run)",
      ],
      { cwd: E2E_DIR, env, encoding: 'utf8' },
    )
    expect(r.status, r.stderr).toBe(0)
    expect(r.stdout.trim()).toBe('function')
  })
})
