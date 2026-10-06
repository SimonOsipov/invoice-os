import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
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
    for (const name of COMMANDS) {
      expect(r.stdout).toContain(name)
      expect(r.stdout, `${name} has its own line under Commands`).toMatch(new RegExp(`^  ${name} +\\S`, 'm'))
    }
  })

  it('each command help has an example', async () => {
    for (const name of COMMANDS) {
      const r = await run([name, '--help'])
      expect(r.code, `${name} --help exit code`).toBe(0)
      expect(r.stdout, `${name} --help names the command`).toContain(name)
      expect(r.stdout, `${name} --help has an example`).toContain('Example:')
    }
  })

  it('a command help prints that command usage, not the general help', async () => {
    for (const name of COMMANDS) {
      const r = await run([name, '--help'])
      expect(r.stdout, `${name} --help`).toContain(`Usage: ctl ${name} `)
      for (const other of COMMANDS.filter((c) => c !== name)) {
        expect(r.stdout, `${name} --help must not carry ${other} usage`).not.toContain(`Usage: ctl ${other} `)
      }
      expect(r.stdout).not.toContain('Usage: ctl <command>')
    }
  })

  it('every help example runs the ctl script of the e2e package', async () => {
    for (const name of [undefined, ...COMMANDS]) {
      const r = await run(name ? [name, '--help'] : ['--help'])
      const example = r.stdout.split('\n').find((l) => l.startsWith('Example:'))
      expect(example, `${name ?? 'ctl'} --help has an Example line`).toBeDefined()
      expect(example).toContain('pnpm -s --filter @invoice-os/e2e ctl ')
    }
  })

  it('--help never runs the command', async () => {
    let calls = 0
    const r = await run(['env', '--help'], {
      env: async () => {
        calls++
        return {}
      },
    })
    expect(r.code).toBe(0)
    expect(calls).toBe(0)
  })

  it('a command receives its positionals and flags, never --help', async () => {
    let seen: { positionals: string[]; flags: Record<string, string | undefined> } | undefined
    const r = await run(['measure', '.x', '--props', 'width,height', '--viewport', '1440', '--session', 's1'], {
      measure: async (positionals, flags) => {
        seen = { positionals, flags }
        return {}
      },
    })
    expect(r.code).toBe(0)
    expect(seen?.positionals).toEqual(['.x'])
    expect(seen?.flags).toEqual({ props: 'width,height', viewport: '1440', session: 's1' })
    expect(seen?.flags).not.toHaveProperty('help')
  })

  it('a CtlError keeps its own exit code', async () => {
    const r = await run(['measure', '.x'], {
      measure: async () => {
        throw new CtlError('bad prop', 'use CSS names', 2)
      },
    })
    expect(r.code).toBe(2)
    expect(r.stdout).toBe('')
    expect(JSON.parse(r.stderr)).toEqual({ error: 'bad prop', hint: 'use CSS names' })
  })

  it('a thrown non-Error value still prints JSON', async () => {
    const r = await run(['env', 'pr-1'], {
      env: async () => {
        throw 'plain string'
      },
    })
    expect(r.code).toBe(1)
    expect(JSON.parse(r.stderr)).toMatchObject({ error: 'plain string', hint: expect.stringContaining('--help') })
  })

  it('an unknown flag is a usage error', async () => {
    const r = await run(['env', 'pr-1', '--bogus'], { env: async () => ({ a: 1 }) })
    expect(r.code).toBe(2)
    expect(r.stdout).toBe('')
    expect(JSON.parse(r.stderr)).toMatchObject({ error: expect.stringContaining('bogus'), hint: expect.stringContaining('ctl --help') })
  })

  it('--help does not hide an unknown command', async () => {
    const r = await run(['frobnicate', '--help'])
    expect(r.code).toBe(2)
    expect(JSON.parse(r.stderr)).toMatchObject({ error: expect.stringContaining('frobnicate') })
  })

  it('a result prints as JSON on stdout', async () => {
    const r = await run(['env', 'pr-1'], { env: async () => ({ a: 1, nested: { b: [1, 2] } }) })
    expect(r.code).toBe(0)
    expect(r.stdout.endsWith('\n')).toBe(true)
    expect(r.stdout.trimEnd().split('\n')).toHaveLength(1)
    expect(JSON.parse(r.stdout)).toEqual({ a: 1, nested: { b: [1, 2] } })
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

  const entry = (...args: string[]) => {
    const r = spawnSync(process.execPath, ['--import', './ctl/tsResolve.mjs', './ctl/main.ts', ...args], {
      cwd: E2E_DIR,
      env,
      encoding: 'utf8',
    })
    const lines = r.stderr.trim().split('\n').filter(Boolean)
    return { status: r.status, stdout: r.stdout, stderr: r.stderr, stderrJson: lines.length ? JSON.parse(lines[lines.length - 1]) : undefined }
  }

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

  it('the real entry exits 2 on a usage error', () => {
    for (const args of [[], ['frobnicate']]) {
      const r = entry(...args)
      expect(r.status, `args ${JSON.stringify(args)}`).toBe(2)
      expect(r.stdout).toBe('')
      expect(r.stderrJson).toMatchObject({ hint: expect.stringContaining('ctl --help') })
    }
  })

  it('the real entry exits 1 on a CtlError', () => {
    const r = entry('login', 'firm', '--env', 'production')
    expect(r.status).toBe(1)
    expect(r.stdout).toBe('')
    expect(r.stderrJson).toMatchObject({ error: expect.any(String), hint: expect.any(String) })
  })

  it('main loads a command module only when that command runs', () => {
    const script = `
      import { registerHooks } from 'node:module'
      const loaded = []
      registerHooks({ load(url, ctx, next) { loaded.push(url); return next(url, ctx) } })
      const has = (name) => loaded.some((u) => u.endsWith('/ctl/' + name + '.ts'))
      const main = await import('./ctl/main')
      const afterImport = has('railway')
      await main.run(['--help'])
      await main.run(['env', '--help'])
      const afterHelp = has('railway')
      await main.run(['env', 'staging'])
      console.log(JSON.stringify({ main: has('main'), afterImport, afterHelp, afterRun: has('railway') }))`
    const r = spawnSync(process.execPath, ['--import', './ctl/tsResolve.mjs', '--input-type=module', '-e', script], { cwd: E2E_DIR, env, encoding: 'utf8' })
    expect(r.status, r.stderr).toBe(0)
    expect(JSON.parse(r.stdout.trim().split('\n').pop() as string)).toEqual({ main: true, afterImport: false, afterHelp: false, afterRun: true })
  })

  it('the resolve hook rewrites only relative specifiers', () => {
    const script = `
      const t = async (s) => { try { await import(s); return 'ok' } catch (e) { return e.code } }
      console.log(JSON.stringify({
        dot: await t('./ctl/main'),
        dotdot: await t('../e2e/ctl/main'),
        absolute: await t(process.argv[1]),
        bare: await t('zz-not-a-package'),
        missing: await t('./ctl/nope'),
      }))`
    const r = spawnSync(
      process.execPath,
      ['--import', './ctl/tsResolve.mjs', '--input-type=module', '-e', script, `${E2E_DIR}ctl/main`],
      { cwd: E2E_DIR, env, encoding: 'utf8' },
    )
    expect(r.status, r.stderr).toBe(0)
    expect(JSON.parse(r.stdout)).toEqual({
      dot: 'ok',
      dotdot: 'ok',
      absolute: 'ERR_MODULE_NOT_FOUND',
      bare: 'ERR_MODULE_NOT_FOUND',
      missing: 'ERR_MODULE_NOT_FOUND',
    })
  })
})

describe('ctl toolchain', () => {
  const pkg = JSON.parse(readFileSync(`${E2E_DIR}package.json`, 'utf8'))
  const node = (bin: string[]) => spawnSync(process.execPath, bin, { cwd: E2E_DIR, encoding: 'utf8' })

  it('@playwright/cli is pinned exactly and the installed binary is that version', () => {
    const pinned: string = pkg.devDependencies['@playwright/cli']
    expect(pinned).toMatch(/^\d+\.\d+\.\d+$/)
    const r = node(['node_modules/@playwright/cli/playwright-cli.js', '--version'])
    expect(r.status, r.stderr).toBe(0)
    expect(r.stdout.trim()).toBe(pinned)
  })

  it('the playwright bin is still the version of @playwright/test, not the cli nested one', () => {
    const declared: string = pkg.devDependencies['@playwright/test']
    const installed = JSON.parse(readFileSync(`${E2E_DIR}node_modules/@playwright/test/package.json`, 'utf8')).version
    expect(installed.split('.')[0]).toBe(declared.replace(/^\D*/, '').split('.')[0])
    const r = node(['node_modules/@playwright/test/cli.js', '--version'])
    expect(r.status, r.stderr).toBe(0)
    expect(r.stdout.trim()).toBe(`Version ${installed}`)
  })

  it('git ignores the .playwright-cli scratch directory', () => {
    const r = spawnSync('git', ['check-ignore', '-q', '.playwright-cli/session.png'], { cwd: E2E_DIR })
    expect(r.status).toBe(0)
  })
})
