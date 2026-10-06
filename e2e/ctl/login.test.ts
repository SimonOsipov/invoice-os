// ctl login: argument rules, the reuse / re-sign-in / re-create chain, the API role read and the re-grant.
// The helpers that resolve a URL at load are imported by login.ts lazily, so every test imports a fresh ./login
// after vi.resetModules() (the realAccounts.test.ts pattern). Credentials never reach an assertion message.
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Account, AccountKey, LoginDeps, LoginRequest, Persona, Role, StoredAccounts } from './login'
import type { EnvResult } from './railway'

type Login = typeof import('./login')

const URL_VARS = ['GATEWAY_URL', 'APP_URL', 'LANDING_URL', 'OPS_CONSOLE_URL', 'SUPPORT_CONSOLE_URL'] as const
const ENV_KEYS = [...URL_VARS, 'PLAYWRIGHT_CLI_SESSION']
const urls = {
  GATEWAY_URL: 'https://gateway.test',
  APP_URL: 'https://app.test',
  LANDING_URL: 'https://landing.test',
  OPS_CONSOLE_URL: 'https://ops.test',
  SUPPORT_CONSOLE_URL: 'https://support.test',
}
const ENV_ID = 'env-id-1'
const TENANT = { firm: '11111111-1111-1111-1111-111111111111', inhouse: '22222222-2222-2222-2222-222222222222' }
// The account-set table of the story (Command contracts).
const KEYS: AccountKey[] = ['firm-admin', 'firm-preparer', 'firm-reviewer', 'inhouse-admin', 'inhouse-preparer', 'inhouse-reviewer', 'developer', 'support']
const SORTED_KEYS = [...KEYS].sort()
const SUBJECT = 'a1b2c3d4-0000-4000-8000-000000000001'
const ACCESS_TOKEN = `h.${Buffer.from(JSON.stringify({ sub: SUBJECT })).toString('base64url')}.s`
const BROWSER = ['signInFresh', 'checkSavedState']

let roots: string[] = []
let savedEnv: Record<string, string | undefined> = {}

beforeEach(() => {
  savedEnv = Object.fromEntries(ENV_KEYS.map((k) => [k, process.env[k]]))
  for (const k of ENV_KEYS) delete process.env[k]
})

afterEach(() => {
  vi.unstubAllGlobals()
  for (const k of ENV_KEYS) {
    if (savedEnv[k] === undefined) delete process.env[k]
    else process.env[k] = savedEnv[k]
  }
  for (const r of roots) rmSync(r, { recursive: true, force: true })
  roots = []
})

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

type Refusal = Error & { code: number; hint: string }

function asRefusal(e: unknown): Refusal {
  expect(e).toBeInstanceOf(Error)
  return e as Refusal
}

async function refused(p: Promise<unknown>): Promise<Refusal> {
  let out: { err: unknown } | { value: unknown }
  try {
    out = { value: await p }
  } catch (err) {
    out = { err }
  }
  expect('err' in out, 'expected a rejection').toBe(true)
  return asRefusal((out as { err: unknown }).err)
}

function refusedSync(fn: () => unknown): Refusal {
  let out: { err: unknown } | { value: unknown }
  try {
    out = { value: fn() }
  } catch (err) {
    out = { err }
  }
  expect('err' in out, 'expected a throw').toBe(true)
  return asRefusal((out as { err: unknown }).err)
}

function accountFor(key: AccountKey): Account {
  const [persona, role] = key.split('-')
  if (persona === 'firm' || persona === 'inhouse') {
    const tenantId = TENANT[persona]
    return { email: `e2e-member-${tenantId}-${role}@example.com`, password: `pw-${key}`, displayName: `E2E ${persona} ${role}`, tenantId, role: role as Role }
  }
  return { email: `ctl-${key}-0001@example.com`, password: `pw-${key}`, userId: `user-${key}` }
}
const allAccounts = () => Object.fromEntries(KEYS.map((k) => [k, accountFor(k)])) as Record<AccountKey, Account>

const req = (persona: Persona, role: Role = 'admin', env = 'pr-1', session = 'default'): LoginRequest => ({ persona, env, role, session })

const storeDir = (root: string, env = 'pr-1') => path.join(root, '.ralph', 'ctl', env)
const accountsFile = (root: string, env = 'pr-1') => path.join(storeDir(root, env), 'accounts.json')

function seed(root: string, opts: { env?: string; environmentId?: string; states?: AccountKey[] } = {}): void {
  const env = opts.env ?? 'pr-1'
  mkdirSync(storeDir(root, env), { recursive: true })
  const stored: StoredAccounts = { environmentId: opts.environmentId ?? ENV_ID, createdAt: '2026-10-06T00:00:00.000Z', accounts: allAccounts() }
  writeFileSync(accountsFile(root, env), JSON.stringify(stored))
  for (const k of opts.states ?? []) writeFileSync(path.join(storeDir(root, env), `${k}.json`), '{"cookies":[],"origins":[]}')
}

function readStored(root: string, env = 'pr-1'): StoredAccounts {
  const f = accountsFile(root, env)
  expect(existsSync(f), `${f} was not written`).toBe(true)
  return JSON.parse(readFileSync(f, 'utf8')) as StoredAccounts
}

// A spy that also records its name on the shared timeline.
function spy<A extends unknown[], R>(calls: string[], name: string, impl: (...a: A) => R) {
  return vi.fn((...a: A): R => {
    calls.push(name)
    return impl(...a)
  })
}
const count = (fn: unknown) => vi.mocked(fn as (...a: unknown[]) => unknown).mock.calls.length
const browserIndex = (calls: string[]) => calls.findIndex((c) => BROWSER.includes(c))

function makeDeps(L: Login, root: string, calls: string[], over: Partial<LoginDeps> = {}): LoginDeps {
  const base: LoginDeps = {
    resolveEnv: vi.fn(async (env: string): Promise<EnvResult> => ({ env, environmentId: ENV_ID, urls: { ...urls }, dark: [] })),
    store: L.createStore(root),
    provisionAll: spy(calls, 'provisionAll', async () => allAccounts()),
    apiRole: spy(calls, 'apiRole', async (a: Account) => ({ role: a.role ?? null, tenantId: a.tenantId })),
    credentialsValid: spy(calls, 'credentialsValid', async () => true),
    regrant: spy(calls, 'regrant', async () => {}),
    signInFresh: spy(calls, 'signInFresh', async (k: AccountKey) => ({ role: accountFor(k).role })),
    checkSavedState: spy(calls, 'checkSavedState', async (k: AccountKey) => ({ ok: true, role: accountFor(k).role })),
  }
  return { ...base, ...over }
}

async function load(): Promise<Login> {
  vi.resetModules()
  return import('./login')
}

async function setup(over: Partial<LoginDeps> | ((calls: string[]) => Partial<LoginDeps>) = {}) {
  const L = await load()
  const root = mkdtempSync(path.join(tmpdir(), 'ctl-login-'))
  roots.push(root)
  const calls: string[] = []
  return { L, root, calls, deps: makeDeps(L, root, calls, typeof over === 'function' ? over(calls) : over) }
}

type Answer = { status: number; body?: unknown }
interface Gateway {
  requests: { method: string; path: string; body: any }[]
}

const meOk = (role: string): Answer => ({ status: 200, body: { tenant: { id: TENANT.firm, name: 'Okafor', kind: 'firm' }, user: { id: SUBJECT, role, display_name: null, email: null } } })
const FORBIDDEN: Answer = { status: 403, body: { error: 'no active membership' } }

// Stubs the gateway at global fetch; every request lands on `log` as "METHOD /path".
function stubGateway(opts: { log?: string[]; me?: Answer[]; signIn?: Answer; staff?: Answer[] } = {}): Gateway {
  const gw: Gateway = { requests: [] }
  const me = [...(opts.me ?? [meOk('admin')])]
  const staff = [...(opts.staff ?? [{ status: 204 }])]
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: { method?: string; body?: string }) => {
      const method = init?.method ?? 'GET'
      const p = new URL(url).pathname
      gw.requests.push({ method, path: p, body: init?.body ? JSON.parse(init.body) : undefined })
      opts.log?.push(`${method} ${p}`)
      const answer = (a: Answer) => new Response(a.body === undefined ? null : JSON.stringify(a.body), { status: a.status })
      switch (p) {
        case '/auth/register':
          return answer({ status: 202, body: {} })
        case '/auth/sign-in':
          return answer(opts.signIn ?? { status: 200, body: { code: 'code-1' } })
        case '/auth/exchange':
          return answer({ status: 200, body: { access_token: ACCESS_TOKEN, refresh_token: 'refresh-1' } })
        case '/auth/mock/member':
          return answer({ status: 204 })
        case '/auth/mock/staff':
          return answer(staff.length > 1 ? staff.shift()! : staff[0])
        case '/api/tenancy/v1/me':
          return answer(me.length > 1 ? me.shift()! : me[0])
        default:
          return new Response('{}', { status: 404 })
      }
    }),
  )
  return gw
}

const grants = (gw: Gateway) => gw.requests.filter((r) => r.path === '/auth/mock/member')

describe('parseLoginArgs', () => {
  it('production is refused before any network call', async () => {
    const L = await load()
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)

    const err = refusedSync(() => L.parseLoginArgs(['firm'], { env: 'production' }))

    expect(err.code).toBe(1)
    expect(err.message).toContain('production')
    expect(err.message).toContain('PR environment')
    expect(err.hint).toContain('--env pr-')
    expect(fetchSpy).toHaveBeenCalledTimes(0)
    expect(L.parseLoginArgs(['firm'], { env: 'pr-1' })).toMatchObject({ persona: 'firm', env: 'pr-1' })
  })

  it('the production refusal wins over a role error', async () => {
    const L = await load()

    const err = refusedSync(() => L.parseLoginArgs(['developer'], { env: 'production', role: 'admin' }))

    expect(err.code).toBe(1)
    expect(err.message).toContain('production')
    expect(err.message).toContain('PR environment')
  })

  it('--role on a staff persona names the reason', async () => {
    const L = await load()
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)

    for (const persona of ['developer', 'support']) {
      const err = refusedSync(() => L.parseLoginArgs([persona], { env: 'pr-1', role: 'admin' }))
      expect(err.code, persona).toBe(2)
      expect(err.message, persona).toContain('--role applies to firm and inhouse')
    }
    expect(fetchSpy).toHaveBeenCalledTimes(0)
    expect(L.parseLoginArgs(['firm'], { env: 'pr-1', role: 'admin' }).persona).toBe('firm')
    expect(L.parseLoginArgs(['developer'], { env: 'pr-1' }).persona).toBe('developer')
  })

  it('--role defaults to admin', async () => {
    const L = await load()

    expect(L.parseLoginArgs(['firm'], { env: 'pr-1' }).role).toBe('admin')
    expect(L.parseLoginArgs(['inhouse'], { env: 'pr-1' }).role).toBe('admin')
    for (const role of ['admin', 'preparer', 'reviewer']) expect(L.parseLoginArgs(['firm'], { env: 'pr-1', role }).role, role).toBe(role)
  })

  it('an unknown role or persona is a usage error', async () => {
    const L = await load()

    const badRole = refusedSync(() => L.parseLoginArgs(['firm'], { env: 'pr-1', role: 'owner' }))
    expect(badRole.code).toBe(2)
    expect(badRole.message).toContain('admin|preparer|reviewer')

    expect(refusedSync(() => L.parseLoginArgs(['staff'], { env: 'pr-1' })).code).toBe(2)
    expect(refusedSync(() => L.parseLoginArgs([], { env: 'pr-1' })).code).toBe(2)
    expect(refusedSync(() => L.parseLoginArgs(['firm'], {})).code).toBe(2)
    expect(refusedSync(() => L.parseLoginArgs(['firm'], { env: 'staging' })).code).toBe(2)
    // Rule 1 runs before the production rule.
    expect(refusedSync(() => L.parseLoginArgs(['staff'], { env: 'production' })).code).toBe(2)
  })

  it('the session comes from --session, then $PLAYWRIGHT_CLI_SESSION, then "default"', async () => {
    const L = await load()

    expect(L.parseLoginArgs(['firm'], { env: 'pr-1' }).session).toBe('default')
    process.env.PLAYWRIGHT_CLI_SESSION = 'from-env'
    expect(L.parseLoginArgs(['firm'], { env: 'pr-1' }).session).toBe('from-env')
    expect(L.parseLoginArgs(['firm'], { env: 'pr-1', session: 'qa' }).session).toBe('qa')
  })
})

describe('provisionAll', () => {
  it('provisionAll creates the 8 accounts', async () => {
    const L = await load()
    const gw = stubGateway()
    process.env.GATEWAY_URL = urls.GATEWAY_URL
    process.env.APP_URL = urls.APP_URL
    const { TENANTS } = await import('../topology/targets')

    const accounts = await L.provisionAll()

    expect(Object.keys(accounts).sort()).toEqual(SORTED_KEYS)
    expect([...L.ACCOUNT_KEYS].sort()).toEqual(SORTED_KEYS)
    const members = grants(gw)
    expect(members).toHaveLength(6)
    const want = [TENANTS.a.id, TENANTS.b.id].flatMap((t) => ['admin', 'preparer', 'reviewer'].map((r) => `${t}:${r}`))
    expect(new Set(members.map((m) => `${m.body.tenant_id}:${m.body.role}`))).toEqual(new Set(want))
    expect(gw.requests.filter((r) => r.path === '/auth/mock/staff')).toHaveLength(2)
    for (const k of KEYS) {
      expect(accounts[k].email, k).toEqual(expect.any(String))
      expect(accounts[k].password.length, k).toBeGreaterThan(0)
    }
    expect(accounts['firm-reviewer']).toMatchObject({ tenantId: TENANTS.a.id, role: 'reviewer' })
    expect(accounts['inhouse-preparer']).toMatchObject({ tenantId: TENANTS.b.id, role: 'preparer' })
    expect(accounts.developer.email.startsWith('ctl-developer-')).toBe(true)
    expect(accounts.support.email.startsWith('ctl-support-')).toBe(true)
  })

  it('a staff password is independent of the email login prints', async () => {
    const L = await load()
    const gw = stubGateway()
    process.env.GATEWAY_URL = urls.GATEWAY_URL
    process.env.APP_URL = urls.APP_URL

    const accounts = await L.provisionAll()

    for (const k of ['developer', 'support'] as const) {
      const { email, password } = accounts[k]
      expect(password.length, k).toBeGreaterThanOrEqual(16)
      expect(email.includes(password), `${k} email contains its password`).toBe(false)
      expect(email.includes(password.slice(0, 8)), `${k} email carries a password fragment`).toBe(false)
      const sent = gw.requests.find((r) => r.path === '/auth/register' && r.body.email === email)
      expect(sent?.body.password, `${k} register carries the stored password`).toBe(password)
    }
    expect(accounts.developer.password).not.toBe(accounts.support.password)
  })

  it('a failure partway through provisionAll writes nothing', async () => {
    const L = await load()
    const root = mkdtempSync(path.join(tmpdir(), 'ctl-login-'))
    roots.push(root)
    const calls: string[] = []
    const gw = stubGateway({ staff: [{ status: 204 }, { status: 500, body: { error: 'boom' } }] })
    const deps = makeDeps(L, root, calls, { provisionAll: L.provisionAll })

    const err = await refused(L.login(req('firm', 'reviewer'), deps))

    expect(err.message).toMatch(/staff grant/i)
    expect(grants(gw).length, 'the member grants ran before the failure').toBeGreaterThan(0)
    expect(existsSync(accountsFile(root))).toBe(false)
  })

  it('regrant posts the grant even after provisionAll ran in-process', async () => {
    const L = await load()
    const gw = stubGateway()
    process.env.GATEWAY_URL = urls.GATEWAY_URL
    process.env.APP_URL = urls.APP_URL
    const accounts = await L.provisionAll()
    const before = grants(gw).length
    expect(before).toBe(6)

    await L.regrant('firm-reviewer', accounts['firm-reviewer'])

    const after = grants(gw)
    expect(after).toHaveLength(before + 1)
    expect(after[after.length - 1].body).toMatchObject({ role: 'reviewer', tenant_id: accounts['firm-reviewer'].tenantId, email: accounts['firm-reviewer'].email })
  })
})

describe('login chain', () => {
  it('login on production touches no dependency', async () => {
    const { L, root, calls, deps } = await setup()
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)

    const err = await refused(L.login(req('firm', 'admin', 'production'), deps))

    expect(err.code).toBe(1)
    expect(err.message).toContain('production')
    expect(count(deps.resolveEnv)).toBe(0)
    expect(calls).toEqual([])
    expect(fetchSpy).toHaveBeenCalledTimes(0)
    expect(existsSync(path.join(root, '.ralph'))).toBe(false)

    await L.login(req('firm', 'admin', 'pr-1'), deps)
    expect(count(deps.resolveEnv), 'the same deps run for a PR environment').toBe(1)
  })

  it('the first login provisions, saves and signs in', async () => {
    let seenEnv: Record<string, string | undefined> = {}
    const { L, root, deps } = await setup((calls) => ({
      provisionAll: spy(calls, 'provisionAll', async () => {
        seenEnv = Object.fromEntries(URL_VARS.map((k) => [k, process.env[k]]))
        return allAccounts()
      }),
    }))

    const r = await L.login(req('firm', 'reviewer'), deps)

    expect(count(deps.provisionAll)).toBe(1)
    const stored = readStored(root)
    expect(stored.environmentId).toBe(ENV_ID)
    expect(Object.keys(stored.accounts).sort()).toEqual(SORTED_KEYS)
    expect(count(deps.signInFresh)).toBe(1)
    const [key, , gotUrls, statePath] = vi.mocked(deps.signInFresh).mock.calls[0]
    expect(key).toBe('firm-reviewer')
    expect(gotUrls).toEqual(urls)
    expect(statePath).toBe(path.join(storeDir(root), 'firm-reviewer.json'))
    expect(count(deps.checkSavedState)).toBe(0)
    expect(r).toMatchObject({ env: 'pr-1', persona: 'firm', role: 'reviewer', created: true, reused: false, email: accountFor('firm-reviewer').email, tenantId: TENANT.firm, url: urls.APP_URL })
    expect(path.isAbsolute(r.storageState)).toBe(true)
    expect(r.storageState.endsWith(path.join('.ralph', 'ctl', 'pr-1', 'firm-reviewer.json'))).toBe(true)
    const printed = JSON.stringify(r)
    for (const k of KEYS) expect(printed.includes(accountFor(k).password), `the result prints the password of ${k}`).toBe(false)
    expect(seenEnv, 'the URL variables are set before provisionAll runs').toEqual(urls)
  })

  it('a working saved state is reused with no account and no grant', async () => {
    const { L, root, deps } = await setup({
      apiRole: vi.fn(async () => ({ role: 'admin', tenantId: TENANT.firm })),
      checkSavedState: vi.fn(async () => ({ ok: true, role: 'admin' })),
    })
    seed(root, { states: ['firm-admin'] })

    const r = await L.login(req('firm', 'admin'), deps)

    expect(count(deps.checkSavedState)).toBe(1)
    expect(vi.mocked(deps.checkSavedState).mock.calls[0][0]).toBe('firm-admin')
    expect(vi.mocked(deps.checkSavedState).mock.calls[0][3]).toBe(path.join(storeDir(root), 'firm-admin.json'))
    for (const name of ['provisionAll', 'credentialsValid', 'signInFresh', 'regrant'] as const) expect(count(deps[name]), name).toBe(0)
    expect(r).toMatchObject({ reused: true, created: false, gatewayWrites: 0 })
  })

  it('a stale state signs in again with the saved credentials', async () => {
    const firm = await setup({
      apiRole: vi.fn(async () => ({ role: 'admin' })),
      checkSavedState: vi.fn(async () => ({ ok: false })),
    })
    seed(firm.root, { states: ['firm-admin'] })

    const r = await firm.L.login(req('firm', 'admin'), firm.deps)

    expect(count(firm.deps.checkSavedState)).toBe(1)
    expect(count(firm.deps.signInFresh)).toBe(1)
    expect(count(firm.deps.credentialsValid)).toBe(0)
    expect(count(firm.deps.provisionAll)).toBe(0)
    expect(r).toMatchObject({ created: false, reused: false })

    const dev = await setup({ checkSavedState: vi.fn(async () => ({ ok: false })), credentialsValid: vi.fn(async () => true) })
    seed(dev.root, { states: ['developer'] })

    await dev.L.login(req('developer'), dev.deps)

    expect(count(dev.deps.checkSavedState)).toBe(1)
    expect(count(dev.deps.credentialsValid)).toBe(1)
    expect(count(dev.deps.signInFresh)).toBe(1)
    expect(vi.mocked(dev.deps.signInFresh).mock.calls[0][0]).toBe('developer')
    expect(count(dev.deps.apiRole)).toBe(0)
    expect(count(dev.deps.provisionAll)).toBe(0)
  })

  it('a refused staff credential re-creates all 8', async () => {
    const { L, root, calls, deps } = await setup((c) => ({
      checkSavedState: spy(c, 'checkSavedState', async () => ({ ok: false })),
      credentialsValid: spy(c, 'credentialsValid', async () => false),
    }))
    seed(root, { states: ['developer'] })

    const r = await L.login(req('developer'), deps)

    expect(count(deps.provisionAll)).toBe(1)
    expect(count(deps.signInFresh)).toBe(1)
    expect(calls.indexOf('provisionAll')).toBeGreaterThan(calls.indexOf('credentialsValid'))
    expect(calls.indexOf('signInFresh')).toBeGreaterThan(calls.indexOf('provisionAll'))
    expect(r.created).toBe(true)
  })

  it('a changed environment id re-creates all 8', async () => {
    const { L, root, deps } = await setup({
      resolveEnv: vi.fn(async (env: string): Promise<EnvResult> => ({ env, environmentId: 'new', urls: { ...urls }, dark: [] })),
    })
    seed(root, { environmentId: 'old', states: ['firm-admin'] })

    const r = await L.login(req('firm', 'admin'), deps)

    expect(count(deps.provisionAll)).toBe(1)
    expect(vi.mocked(deps.provisionAll).mock.invocationCallOrder[0]).toBeLessThan(
      Math.min(...[deps.apiRole, deps.checkSavedState, deps.signInFresh].flatMap((f) => vi.mocked(f).mock.invocationCallOrder)),
    )
    expect(readStored(root).environmentId).toBe('new')
    expect(r.created).toBe(true)
  })

  it('a corrupt accounts.json stops with the delete hint', async () => {
    const file = (root: string) => accountsFile(root)
    const cases: [string, (root: string) => void][] = [
      ['truncated JSON', (root) => (mkdirSync(storeDir(root), { recursive: true }), writeFileSync(file(root), '{"environmentId":"e"'))],
      [
        'missing the support key',
        (root) => {
          seed(root)
          const stored = JSON.parse(readFileSync(file(root), 'utf8'))
          delete stored.accounts.support
          writeFileSync(file(root), JSON.stringify(stored))
        },
      ],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [name, corrupt] of cases) {
      const { L, root, deps } = await setup()
      corrupt(root)

      const err = await refused(L.login(req('firm', 'admin'), deps))

      expect(err.code, name).toBe(1)
      expect(err.hint, name).toContain('delete')
      expect(err.hint, name).toContain(file(root))
      expect(count(deps.provisionAll), name).toBe(0)
      expect(count(deps.signInFresh) + count(deps.checkSavedState), name).toBe(0)
    }
  })

  it('a dark domain the persona needs stops login', async () => {
    const dark = async (env: string): Promise<EnvResult> => ({ env, environmentId: ENV_ID, urls: { ...urls }, dark: ['app'] })
    const { L, deps } = await setup({ resolveEnv: vi.fn(dark) })

    const err = await refused(L.login(req('firm', 'admin'), deps))

    expect(err.code).toBe(1)
    expect(err.message).toMatch(/\bapp\b/)
    expect(count(deps.provisionAll)).toBe(0)

    const other = await setup({
      resolveEnv: vi.fn(async (env: string): Promise<EnvResult> => ({ env, environmentId: ENV_ID, urls: { ...urls }, dark: ['ops-console'] })),
    })
    const r = await other.L.login(req('firm', 'admin'), other.deps)
    expect(r.persona, 'a dark service the persona does not use does not stop login').toBe('firm')
  })

  it('accounts.json is written owner-only', async () => {
    const { L, root, deps } = await setup()

    await L.login(req('firm', 'admin'), deps)

    const f = accountsFile(root)
    expect(existsSync(f), `${f} was not written`).toBe(true)
    expect(statSync(f).mode & 0o777).toBe(0o600)
  })

  it('next lists open, state-load and goto with absolute paths', async () => {
    const { L, deps } = await setup()

    const r = await L.login(req('firm', 'reviewer', 'pr-1', 'qa'), deps)

    expect(path.isAbsolute(r.storageState)).toBe(true)
    expect(r.url).toBe(urls.APP_URL)
    expect(r.next.length).toBeGreaterThan(0)
    const at = (needle: string) => r.next.findIndex((l) => l.includes(needle))
    const open = at('-s=qa open')
    const load = at(`-s=qa state-load ${r.storageState}`)
    const go = at(`-s=qa goto ${urls.APP_URL}`)
    expect(open).toBeGreaterThanOrEqual(0)
    expect(load).toBeGreaterThan(open)
    expect(go).toBeGreaterThan(load)
    expect(r.next.some((l) => l.includes('list --json'))).toBe(true)
  })
})

describe('next commands are POSIX-shell safe', () => {
  it('a storage path and a session with spaces and quotes are single-quoted', async () => {
    const L = await load()
    const root = mkdtempSync(path.join(tmpdir(), "ctl login it's "))
    roots.push(root)
    const deps = makeDeps(L, root, [])

    const r = await L.login(req('firm', 'admin', 'pr-1', "my ses'sion"), deps)

    const q = (v: string) => `'${v.replace(/'/g, `'\\''`)}'`
    expect(r.next.some((l) => l.includes(`state-load ${q(r.storageState)}`)), r.next.join('\n')).toBe(true)
    expect(r.next.filter((l) => l.includes('-s=')).every((l) => l.includes(`-s=${q("my ses'sion")} `)), r.next.join('\n')).toBe(true)
  })
})

describe('the API role read (D47)', () => {
  it('the API role read runs before any browser wait', async () => {
    const { L, root, calls, deps } = await setup((c) => ({
      apiRole: spy(c, 'apiRole', async () => ({ role: 'reviewer', tenantId: TENANT.firm })),
    }))
    seed(root, { states: ['firm-reviewer'] })

    await L.login(req('firm', 'reviewer'), deps)

    expect(calls[0]).toBe('apiRole')
    const browser = browserIndex(calls)
    expect(browser, 'a browser dependency ran').toBeGreaterThan(0)
    expect(calls.indexOf('checkSavedState')).toBeGreaterThan(calls.indexOf('apiRole'))
  })

  it('a 403 is re-granted before any browser wait', async () => {
    const { L, root, calls, deps } = await setup()
    const gw = stubGateway({ log: calls, me: [FORBIDDEN, meOk('admin')] })
    seed(root, { states: ['firm-admin'] })
    const real = { ...deps, apiRole: L.apiRole, regrant: L.regrant }

    const r = await L.login(req('firm', 'admin'), real)

    const grant = calls.indexOf('POST /auth/mock/member')
    const browser = browserIndex(calls)
    expect(grant, 'a grant was sent').toBeGreaterThanOrEqual(0)
    expect(browser, 'a browser dependency ran').toBeGreaterThanOrEqual(0)
    expect(grant).toBeLessThan(browser)
    expect(grants(gw)).toHaveLength(1)
    expect(grants(gw)[0].body).toEqual({ user_id: SUBJECT, tenant_id: TENANT.firm, role: 'admin', display_name: accountFor('firm-admin').displayName, email: accountFor('firm-admin').email })
    expect(gw.requests.filter((q) => q.path === '/api/tenancy/v1/me')).toHaveLength(2)
    expect(gw.requests.some((q) => q.path === '/auth/register'), 'a re-grant registers nobody').toBe(false)
    expect(count(deps.signInFresh) + count(deps.checkSavedState)).toBe(1)
    expect(count(deps.provisionAll)).toBe(0)
    expect(r.gatewayWrites).toBeGreaterThanOrEqual(1)
  })

  it('a wrong role is re-granted before any browser wait', async () => {
    const { L, root, calls, deps } = await setup()
    const gw = stubGateway({ log: calls, me: [meOk('preparer'), meOk('reviewer')] })
    seed(root, { states: ['firm-reviewer'] })

    const r = await L.login(req('firm', 'reviewer'), { ...deps, apiRole: L.apiRole, regrant: L.regrant })

    const grant = calls.indexOf('POST /auth/mock/member')
    expect(grant).toBeGreaterThanOrEqual(0)
    expect(browserIndex(calls)).toBeGreaterThan(grant)
    expect(grants(gw)).toHaveLength(1)
    expect(grants(gw)[0].body.role).toBe('reviewer')
    expect(r.role).toBe('reviewer')
  })

  it('a second 403 or wrong role fails before the browser starts', async () => {
    const scenarios: { name: string; me: Answer[]; outcome: string }[] = [
      { name: '403 twice', me: [FORBIDDEN, FORBIDDEN], outcome: '403' },
      { name: 'preparer twice', me: [meOk('preparer'), meOk('preparer')], outcome: 'preparer' },
    ]
    expect(scenarios.length).toBeGreaterThan(0)
    for (const s of scenarios) {
      const { L, root, calls, deps } = await setup()
      const gw = stubGateway({ log: calls, me: s.me })
      seed(root, { states: ['firm-reviewer'] })

      const err = await refused(L.login(req('firm', 'reviewer'), { ...deps, apiRole: L.apiRole, regrant: L.regrant }))

      expect(err.code, s.name).toBe(1)
      expect(err.message, s.name).toContain('reviewer')
      expect(err.message, s.name).toContain(s.outcome)
      expect(grants(gw), s.name).toHaveLength(1)
      expect(count(deps.signInFresh) + count(deps.checkSavedState), s.name).toBe(0)
    }
  })

  it('a 401 at the API sign-in re-creates all 8, then reads again', async () => {
    const answers = ['gone' as const, { role: 'admin', tenantId: TENANT.firm }]
    const { L, root, calls, deps } = await setup((c) => ({ apiRole: spy(c, 'apiRole', async () => answers.shift() ?? { role: 'admin' }) }))
    seed(root)

    const r = await L.login(req('firm', 'admin'), deps)

    expect(count(deps.provisionAll)).toBe(1)
    expect(calls.slice(0, 3)).toEqual(['apiRole', 'provisionAll', 'apiRole'])
    expect(r.created).toBe(true)
  })

  it('a second gone read after a re-creation does not re-create again', async () => {
    const { L, root, deps } = await setup({
      resolveEnv: vi.fn(async (env: string): Promise<EnvResult> => ({ env, environmentId: 'new', urls: { ...urls }, dark: [] })),
      apiRole: vi.fn(async () => 'gone' as const),
    })
    seed(root, { environmentId: 'old' })

    const err = await refused(L.login(req('firm', 'admin'), deps))

    expect(count(deps.provisionAll), 'recreate ran twice in one login').toBe(1)
    expect(err.message).toMatch(/right after it was created/)
  })

  it('staff personas skip the API role read', async () => {
    const { L, root, deps } = await setup()
    seed(root, { states: ['developer'] })

    await L.login(req('developer'), deps)

    expect(count(deps.checkSavedState), 'the saved state was checked').toBe(1)
    expect(count(deps.apiRole)).toBe(0)
  })

  it('a browser role that differs after the API read fails without a second grant', async () => {
    const { L, root, deps } = await setup({
      apiRole: vi.fn(async () => ({ role: 'reviewer', tenantId: TENANT.firm })),
      signInFresh: vi.fn(async () => ({ role: 'preparer' })),
    })
    seed(root)

    const err = await refused(L.login(req('firm', 'reviewer'), deps))

    expect(err.code).toBe(1)
    expect(err.message).toContain('reviewer')
    expect(err.message).toContain('preparer')
    expect(count(deps.regrant)).toBe(0)
    expect(count(deps.signInFresh), 'the browser sign-in ran').toBe(1)
  })
})

describe('credentialsValid', () => {
  it('credentialsValid sends a state and reads the status', async () => {
    const L = await load()
    process.env.GATEWAY_URL = urls.GATEWAY_URL
    process.env.APP_URL = urls.APP_URL
    const account = accountFor('developer')

    const ok = stubGateway({ signIn: { status: 200, body: { code: 'code-1' } } })
    expect(await L.credentialsValid(account)).toBe(true)
    const signIns = ok.requests.filter((r) => r.path === '/auth/sign-in')
    expect(signIns).toHaveLength(1)
    expect(signIns[0].method).toBe('POST')
    expect(signIns[0].body).toMatchObject({ email: account.email, password: account.password })
    expect(signIns[0].body.state).toMatch(/^[A-Za-z0-9_-]{43}$/)

    stubGateway({ signIn: { status: 401, body: { error: 'invalid email or password' } } })
    expect(await L.credentialsValid(account)).toBe(false)
  })

  it('throttle, full store and server errors are not "account gone"', async () => {
    for (const status of [429, 503, 500]) {
      const { L, root, deps } = await setup({ checkSavedState: vi.fn(async () => ({ ok: false })) })
      stubGateway({ signIn: { status, body: { error: 'nope' } } })
      seed(root, { states: ['developer'] })

      const err = await refused(L.login(req('developer'), { ...deps, credentialsValid: L.credentialsValid }))

      expect(err.code, String(status)).toBe(1)
      expect(err.message, String(status)).toContain(String(status))
      expect(count(deps.provisionAll), String(status)).toBe(0)
      expect(count(deps.signInFresh), String(status)).toBe(0)
    }
  })
})

describe('raceReady', () => {
  // Fails if a rejection of either promise is left without a handler.
  async function noUnhandledRejection(run: () => Promise<void>): Promise<void> {
    const seen = vi.fn()
    process.on('unhandledRejection', seen)
    try {
      await run()
      await sleep(100)
    } finally {
      process.off('unhandledRejection', seen)
    }
    expect(seen).not.toHaveBeenCalled()
  }

  it('raceReady: ready first is ready', async () => {
    const L = await load()
    await noUnhandledRejection(async () => {
      const ready = sleep(10)
      const bounced = new Promise<never>(() => {})
      expect(await L.raceReady(ready, bounced)).toBe('ready')
    })
  })

  it('raceReady: a bounce first is stale', async () => {
    const L = await load()
    await noUnhandledRejection(async () => {
      const bounced = sleep(10)
      const ready = new Promise<never>((_, reject) => setTimeout(() => reject(new Error('timeout')), 50))
      expect(await L.raceReady(ready, bounced)).toBe('stale')
    })
  })

  it('raceReady: both reject is stale', async () => {
    const L = await load()
    await noUnhandledRejection(async () => {
      const ready = Promise.reject(new Error('timeout'))
      const bounced = Promise.reject(new Error('timeout'))
      expect(await L.raceReady(ready, bounced)).toBe('stale')
    })
  })
})

const WRITE_PATHS = ['/auth/register', '/auth/mock/member', '/auth/mock/staff']
const writes = (gw: Gateway) => gw.requests.filter((r) => r.method === 'POST' && WRITE_PATHS.includes(r.path))

describe('gatewayWrites against the requests the gateway saw (D31)', () => {
  it('a first login reports every register and grant request it sent', async () => {
    const { L, root, deps } = await setup()
    const gw = stubGateway()

    const r = await L.login(req('firm', 'admin'), { ...deps, provisionAll: L.provisionAll, apiRole: L.apiRole })

    expect(writes(gw).length, 'the stub saw the register and grant requests').toBeGreaterThan(0)
    expect(r.created).toBe(true)
    expect(r.gatewayWrites).toBe(writes(gw).length)
    expect(readStored(root).environmentId).toBe(ENV_ID)
  })

  it('a re-grant reports exactly its one grant request', async () => {
    const { L, root, deps } = await setup()
    const gw = stubGateway({ me: [FORBIDDEN, meOk('admin')] })
    seed(root, { states: ['firm-admin'] })

    const r = await L.login(req('firm', 'admin'), { ...deps, apiRole: L.apiRole, regrant: L.regrant })

    expect(writes(gw)).toHaveLength(1)
    expect(r.gatewayWrites).toBe(1)
    expect(r.created).toBe(false)
  })

  it('a reuse sends no register and no grant, on the real API read', async () => {
    const { L, root, deps } = await setup()
    const gw = stubGateway({ me: [meOk('admin')] })
    seed(root, { states: ['firm-admin'] })

    const r = await L.login(req('firm', 'admin'), { ...deps, apiRole: L.apiRole })

    expect(gw.requests.some((q) => q.path === '/api/tenancy/v1/me'), 'the API role read ran').toBe(true)
    expect(writes(gw)).toHaveLength(0)
    expect(r).toMatchObject({ reused: true, gatewayWrites: 0 })
  })
})

describe('login chain, adversarial', () => {
  it('a rebuilt environment never checks the old state file', async () => {
    const { L, root, deps } = await setup({
      resolveEnv: vi.fn(async (env: string): Promise<EnvResult> => ({ env, environmentId: 'new', urls: { ...urls }, dark: [] })),
    })
    seed(root, { environmentId: 'old', states: ['firm-admin'] })

    const r = await L.login(req('firm', 'admin'), deps)

    expect(count(deps.signInFresh), 'the browser signed in again').toBe(1)
    expect(count(deps.checkSavedState)).toBe(0)
    expect(r).toMatchObject({ created: true, reused: false })
  })

  it('a firm account with no state file signs in fresh and never preflights', async () => {
    const { L, root, deps } = await setup()
    seed(root)

    const r = await L.login(req('firm', 'preparer'), deps)

    expect(count(deps.signInFresh)).toBe(1)
    expect(count(deps.checkSavedState)).toBe(0)
    expect(count(deps.credentialsValid)).toBe(0)
    expect(count(deps.provisionAll)).toBe(0)
    expect(r).toMatchObject({ created: false, reused: false })
  })

  it('a staff account with no state file preflights its credentials, then signs in', async () => {
    const { L, root, calls, deps } = await setup()
    seed(root)

    await L.login(req('support'), deps)

    expect(calls.filter((c) => c !== 'apiRole')).toEqual(['credentialsValid', 'signInFresh'])
    expect(vi.mocked(deps.signInFresh).mock.calls[0][0]).toBe('support')
  })

  it('an in-house login uses the in-house tenant account and state file', async () => {
    const { L, root, deps } = await setup({ apiRole: vi.fn(async () => ({ role: 'preparer', tenantId: TENANT.inhouse })) })
    seed(root)

    const r = await L.login(req('inhouse', 'preparer'), deps)

    const [key, account, , statePath] = vi.mocked(deps.signInFresh).mock.calls[0]
    expect(key).toBe('inhouse-preparer')
    expect(account.tenantId).toBe(TENANT.inhouse)
    expect(statePath).toBe(path.join(storeDir(root), 'inhouse-preparer.json'))
    expect(r).toMatchObject({ persona: 'inhouse', role: 'preparer', tenantId: TENANT.inhouse, url: urls.APP_URL })
  })

  it('staff results point at their console and carry no role', async () => {
    for (const [persona, url] of [['developer', urls.OPS_CONSOLE_URL], ['support', urls.SUPPORT_CONSOLE_URL]] as const) {
      const { L, root, deps } = await setup()
      seed(root)

      const r = await L.login(req(persona), deps)

      expect(r.url, persona).toBe(url)
      expect(r.persona, persona).toBe(persona)
      expect('role' in r, `${persona} result has a role key`).toBe(false)
      expect(r.tenantId, persona).toBeUndefined()
      expect(r.storageState.endsWith(`${persona}.json`), persona).toBe(true)
    }
  })

  it('a dark domain stops exactly the personas that use it', async () => {
    const cases: { persona: Persona; dark: EnvResult['dark']; stops: boolean }[] = [
      { persona: 'firm', dark: ['landing'], stops: true },
      { persona: 'firm', dark: ['gateway'], stops: true },
      { persona: 'inhouse', dark: ['app'], stops: true },
      { persona: 'developer', dark: ['ops-console'], stops: true },
      { persona: 'developer', dark: ['gateway'], stops: true },
      { persona: 'support', dark: ['support-console'], stops: true },
      { persona: 'developer', dark: ['support-console', 'landing', 'app'], stops: false },
      { persona: 'support', dark: ['ops-console', 'landing', 'app'], stops: false },
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const c of cases) {
      const { L, root, deps } = await setup({
        resolveEnv: vi.fn(async (env: string): Promise<EnvResult> => ({ env, environmentId: ENV_ID, urls: { ...urls }, dark: c.dark })),
      })
      seed(root)
      const label = `${c.persona} with ${c.dark.join(',')} dark`

      if (c.stops) {
        const err = await refused(L.login(req(c.persona), deps))
        expect(err.code, label).toBe(1)
        expect(err.message, label).toContain(c.dark[0])
        expect(count(deps.signInFresh) + count(deps.checkSavedState), label).toBe(0)
      } else {
        expect((await L.login(req(c.persona), deps)).persona, label).toBe(c.persona)
      }
    }
  })

  it('a role found for another tenant stops before any grant or browser', async () => {
    const { L, root, deps } = await setup({ apiRole: vi.fn(async () => ({ role: 'admin', tenantId: TENANT.inhouse })) })
    seed(root, { states: ['firm-admin'] })

    const err = await refused(L.login(req('firm', 'admin'), deps))

    expect(err.code).toBe(1)
    expect(err.message).toContain(TENANT.inhouse)
    expect(err.message).toContain(TENANT.firm)
    expect(count(deps.regrant)).toBe(0)
    expect(count(deps.signInFresh) + count(deps.checkSavedState)).toBe(0)
  })

  it('a saved state whose browser role differs fails without a second grant', async () => {
    const { L, root, deps } = await setup({
      apiRole: vi.fn(async () => ({ role: 'reviewer', tenantId: TENANT.firm })),
      checkSavedState: vi.fn(async () => ({ ok: true, role: 'preparer' })),
    })
    seed(root, { states: ['firm-reviewer'] })

    const err = await refused(L.login(req('firm', 'reviewer'), deps))

    expect(err.code).toBe(1)
    expect(err.message).toContain('reviewer')
    expect(err.message).toContain('preparer')
    expect(count(deps.checkSavedState)).toBe(1)
    expect(count(deps.regrant)).toBe(0)
  })

  it('a role that matches in the browser is not an error', async () => {
    const { L, root, deps } = await setup({
      apiRole: vi.fn(async () => ({ role: 'reviewer', tenantId: TENANT.firm })),
      signInFresh: vi.fn(async () => ({ role: 'reviewer' })),
    })
    seed(root)

    const r = await L.login(req('firm', 'reviewer'), deps)

    expect(count(deps.signInFresh)).toBe(1)
    expect(r.role).toBe('reviewer')
  })

  it('accounts.json that is valid JSON but not an account store is corrupt', async () => {
    const bodies: Record<string, unknown> = {
      null: null,
      array: [],
      'no environment id': { createdAt: 'x', accounts: allAccounts() },
      'numeric environment id': { environmentId: 7, createdAt: 'x', accounts: allAccounts() },
      'no accounts': { environmentId: ENV_ID, createdAt: 'x' },
      'an account that is null': { environmentId: ENV_ID, createdAt: 'x', accounts: { ...allAccounts(), developer: null } },
    }
    expect(Object.keys(bodies).length).toBeGreaterThan(0)
    for (const [name, body] of Object.entries(bodies)) {
      const { L, root, deps } = await setup()
      mkdirSync(storeDir(root), { recursive: true })
      writeFileSync(accountsFile(root), JSON.stringify(body))

      const err = await refused(L.login(req('firm', 'admin'), deps))

      expect(err.code, name).toBe(1)
      expect(err.hint, name).toContain(accountsFile(root))
      expect(count(deps.provisionAll), name).toBe(0)
    }
  })

  it('a browser failure that echoes a credential does not carry it out of login', async () => {
    const secret = accountFor('firm-admin').password
    const { L, root, deps } = await setup({
      signInFresh: vi.fn(async () => {
        throw new Error(`locator.fill: Timeout 30000ms exceeded.\nCall log:\n  - waiting for getByLabel('Password')\n  - fill("${secret}")`)
      }),
    })
    seed(root)

    const err = await refused(L.login(req('firm', 'admin'), deps))

    expect(err.message, 'the failure itself still shows').toContain('Timeout')
    expect(`${err.message}\n${err.hint}`, 'a Playwright call log echoes the filled value').not.toContain(secret)
  })

  it('no failure message of the login chain carries a saved password', async () => {
    const scenarios: { name: string; run: () => Promise<Refusal> }[] = [
      {
        name: 'second wrong role',
        run: async () => {
          const { L, root, deps } = await setup()
          stubGateway({ me: [meOk('preparer'), meOk('preparer')] })
          seed(root, { states: ['firm-reviewer'] })
          return refused(L.login(req('firm', 'reviewer'), { ...deps, apiRole: L.apiRole, regrant: L.regrant }))
        },
      },
      {
        name: 'browser role differs',
        run: async () => {
          const { L, root, deps } = await setup({ apiRole: vi.fn(async () => ({ role: 'reviewer', tenantId: TENANT.firm })), signInFresh: vi.fn(async () => ({ role: 'preparer' })) })
          seed(root)
          return refused(L.login(req('firm', 'reviewer'), deps))
        },
      },
      {
        name: 'preflight throttled',
        run: async () => {
          const { L, root, deps } = await setup({ checkSavedState: vi.fn(async () => ({ ok: false })) })
          stubGateway({ signIn: { status: 429, body: { error: 'slow down' } } })
          seed(root, { states: ['developer'] })
          return refused(L.login(req('developer'), { ...deps, credentialsValid: L.credentialsValid }))
        },
      },
    ]
    for (const s of scenarios) {
      const err = await s.run()
      expect(err.message.length, s.name).toBeGreaterThan(0)
      for (const k of KEYS) expect(`${err.message}\n${err.hint}`.includes(accountFor(k).password), `${s.name} prints the password of ${k}`).toBe(false)
    }
  })
})

describe('apiRole against a stubbed gateway (D47)', () => {
  const first = (L: Login, key: AccountKey = 'firm-admin') => L.apiRole(accountFor(key))
  const env = () => {
    process.env.GATEWAY_URL = urls.GATEWAY_URL
    process.env.APP_URL = urls.APP_URL
  }

  it('reads the role and tenant of the signed-in account', async () => {
    const L = await load()
    env()
    const gw = stubGateway({ me: [meOk('reviewer')] })

    expect(await first(L)).toEqual({ role: 'reviewer', tenantId: TENANT.firm })
    expect(gw.requests.filter((r) => r.path === '/api/tenancy/v1/me')).toHaveLength(1)
  })

  it('a 403 is no membership, not an error', async () => {
    const L = await load()
    env()
    stubGateway({ me: [FORBIDDEN] })

    expect(await first(L)).toEqual({ role: null })
  })

  it('a 500 on the role read is an error, never a re-grant trigger', async () => {
    const L = await load()
    env()
    stubGateway({ me: [{ status: 500, body: { error: 'boom' } }] })

    await expect(first(L)).rejects.toThrow()
  })

  it('a refused sign-in is gone; a throttled or full one is an error naming the status', async () => {
    const L = await load()
    env()
    stubGateway({ signIn: { status: 401, body: { error: 'invalid email or password' } } })
    expect(await first(L)).toBe('gone')

    for (const status of [429, 503]) {
      stubGateway({ signIn: { status, body: { error: 'nope' } } })
      const err = await refused(first(L))
      expect(err.code, String(status)).toBe(1)
      expect(err.message, String(status)).toContain(String(status))
    }
  })
})

describe('raceReady, adversarial', () => {
  it('a ready timeout that lands before a bounce is stale, not a throw', async () => {
    const L = await load()
    const ready = new Promise<never>((_, reject) => setTimeout(() => reject(new Error('timeout')), 10))

    expect(await L.raceReady(ready, sleep(50))).toBe('stale')
  })

  it('a bounce wait that times out while ready is pending is stale, not a throw', async () => {
    const L = await load()
    const bounced = new Promise<never>((_, reject) => setTimeout(() => reject(new Error('timeout')), 10))

    expect(await L.raceReady(new Promise<never>(() => {}), bounced)).toBe('stale')
  })
})

describe('the re-grant failure message', () => {
  it('names the expected role apart from the account key, then the actual outcome', async () => {
    const cases: { me: Answer[]; outcome: string }[] = [
      { me: [meOk('preparer'), meOk('preparer')], outcome: 'preparer' },
      { me: [FORBIDDEN, FORBIDDEN], outcome: '403' },
    ]
    for (const c of cases) {
      const { L, root, deps } = await setup()
      stubGateway({ me: c.me })
      seed(root, { states: ['firm-reviewer'] })

      const err = await refused(L.login(req('firm', 'reviewer'), { ...deps, apiRole: L.apiRole, regrant: L.regrant }))

      expect(err.message, c.outcome).toMatch(new RegExp(`expected role reviewer for firm-reviewer, got ${c.outcome}`))
    }
  })
})

describe('login scrubs saved passwords from every escaping error', () => {
  function seedWith(root: string, passwords: Partial<Record<AccountKey, string>>): void {
    seed(root)
    const stored = JSON.parse(readFileSync(accountsFile(root), 'utf8')) as StoredAccounts
    for (const [k, pw] of Object.entries(passwords)) stored.accounts[k as AccountKey].password = pw as string
    writeFileSync(accountsFile(root), JSON.stringify(stored))
  }

  it('message, stack, hint and a nested extra of a thrown CtlError are scrubbed', async () => {
    const secret = accountFor('firm-admin').password
    const { L, root, deps } = await setup({
      signInFresh: vi.fn(async () => {
        const { CtlError: Ctl } = await import('./main')
        const thrown = new Ctl(`fill("${secret}") timed out`, `retry, the password was ${secret}`, 1, { call: { args: [secret, { deep: `x${secret}y` }] } })
        void thrown.stack // V8 formats the stack header on first read, so read it before login can edit the message
        throw thrown
      }),
    })
    seed(root)

    const err = (await refused(L.login(req('firm', 'admin'), deps))) as Refusal & { extra?: unknown; stack: string }

    expect(err.message, 'the failure itself still shows').toContain('timed out')
    expect(err.hint).toContain('retry')
    expect(JSON.stringify(err.extra)).toContain('deep')
    for (const [where, text] of Object.entries({ message: err.message, stack: err.stack, hint: err.hint, extra: JSON.stringify(err.extra) })) {
      expect(text.includes(secret), `${where} carries the password`).toBe(false)
    }
  })

  it('an error from a dependency that runs before the browser is scrubbed too', async () => {
    const { L, root, deps } = await setup({
      apiRole: vi.fn(async () => {
        throw new Error(`sign-in echoed ${accountFor('firm-admin').password}`)
      }),
    })
    seed(root)

    const err = await refused(L.login(req('firm', 'admin'), deps))

    expect(err.message).toContain('sign-in echoed')
    expect(err.message.includes(accountFor('firm-admin').password)).toBe(false)
  })

  it('the passwords of a first login are known by the time the browser runs', async () => {
    const secret = accountFor('firm-reviewer').password
    const { L, deps } = await setup({
      signInFresh: vi.fn(async () => {
        throw new Error(`fill("${secret}") timed out`)
      }),
    })

    const err = await refused(L.login(req('firm', 'reviewer'), deps))

    expect(err.message).toContain('timed out')
    expect(err.message.includes(secret)).toBe(false)
  })

  it('the old passwords of a rebuilt environment are scrubbed from a failing provisionAll', async () => {
    const old = accountFor('support').password
    const { L, root, deps } = await setup({
      resolveEnv: vi.fn(async (env: string): Promise<EnvResult> => ({ env, environmentId: 'new', urls: { ...urls }, dark: [] })),
      provisionAll: vi.fn(async () => {
        throw new Error(`register refused, body echoed ${old}`)
      }),
    })
    seed(root, { environmentId: 'old' })

    const err = await refused(L.login(req('firm', 'admin'), deps))

    expect(err.message).toContain('register refused')
    expect(err.message.includes(old)).toBe(false)
  })

  it('a password with regex metacharacters is removed literally, and a lookalike stays', async () => {
    const secret = 'a.b*c(d)[e]+$^|\\q?{1}'
    const { L, root, deps } = await setup({
      signInFresh: vi.fn(async () => {
        throw new Error(`fill("${secret}") and axb*c(d)[e]+$^|\\q?{1} stay apart`)
      }),
    })
    seedWith(root, { 'firm-admin': secret })

    const err = await refused(L.login(req('firm', 'admin'), deps))

    expect(err.message.includes(secret), 'the password survived').toBe(false)
    expect(err.message, 'a lookalike that is not the password is untouched').toContain('axb*c(d)[e]+$^|\\q?{1} stay apart')
  })

  it('a plain string thrown by a dependency is scrubbed', async () => {
    const secret = accountFor('firm-admin').password
    const { L, root, deps } = await setup({
      signInFresh: vi.fn(async () => {
        throw `fill("${secret}") timed out`
      }),
    })
    seed(root)

    const thrown = await L.login(req('firm', 'admin'), deps).then(
      () => undefined,
      (e: unknown) => e,
    )

    expect(typeof thrown, 'the string was rethrown as a string').toBe('string')
    expect(thrown).toContain('timed out')
    expect((thrown as string).includes(secret)).toBe(false)
  })

  it('a password that is a prefix of another is removed from both whole', async () => {
    const admin = 'e2e-member-pw-1111'
    const reviewer = `${admin}-reviewer`
    const { L, root, deps } = await setup({
      signInFresh: vi.fn(async () => {
        throw new Error(`fill("${reviewer}") then fill("${admin}")`)
      }),
    })
    seedWith(root, { 'firm-admin': admin, 'firm-reviewer': reviewer })

    const err = await refused(L.login(req('firm', 'reviewer'), deps))

    expect(err.message).toContain('then fill(')
    expect(err.message.includes(admin), `a password fragment survived: ${err.message}`).toBe(false)
    expect(err.message.includes('-reviewer'), `the reviewer suffix survived: ${err.message}`).toBe(false)
  })
})
