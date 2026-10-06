import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { CtlError } from './main'
import type { EnvResult, ServiceLabel } from './railway'

export type Persona = 'firm' | 'inhouse' | 'developer' | 'support'
export type Role = 'admin' | 'preparer' | 'reviewer'
export type AccountKey =
  | 'firm-admin'
  | 'firm-preparer'
  | 'firm-reviewer'
  | 'inhouse-admin'
  | 'inhouse-preparer'
  | 'inhouse-reviewer'
  | 'developer'
  | 'support'

export const ACCOUNT_KEYS: readonly AccountKey[] = [
  'firm-admin',
  'firm-preparer',
  'firm-reviewer',
  'inhouse-admin',
  'inhouse-preparer',
  'inhouse-reviewer',
  'developer',
  'support',
]

export interface Account {
  email: string
  password: string
  displayName?: string
  tenantId?: string
  role?: Role
  userId?: string
}
export type Accounts = Record<AccountKey, Account>
export interface StoredAccounts {
  environmentId: string
  createdAt: string
  accounts: Accounts
}

export interface LoginRequest {
  persona: Persona
  env: string
  role: Role
  session: string
}

export interface LoginResult {
  env: string
  persona: Persona
  role?: Role
  email: string
  tenantId?: string
  url: string
  storageState: string
  created: boolean
  reused: boolean
  gatewayWrites: number
  next: string[]
}

export interface Store {
  dir(env: string): string
  statePath(env: string, key: AccountKey): string
  read(env: string): StoredAccounts | undefined
  write(env: string, stored: StoredAccounts): void
}

export type ApiRole = { role: string | null; tenantId?: string } | 'gone'

export interface LoginDeps {
  resolveEnv(env: string): Promise<EnvResult>
  store: Store
  provisionAll(): Promise<Accounts>
  apiRole(account: Account): Promise<ApiRole>
  credentialsValid(account: Account): Promise<boolean>
  regrant(key: AccountKey, account: Account): Promise<void>
  signInFresh(key: AccountKey, account: Account, urls: EnvResult['urls'], statePath: string): Promise<{ role?: string }>
  checkSavedState(key: AccountKey, account: Account, urls: EnvResult['urls'], statePath: string): Promise<{ ok: boolean; role?: string }>
}

const ROLES: readonly Role[] = ['admin', 'preparer', 'reviewer']
const PERSONAS: readonly Persona[] = ['firm', 'inhouse', 'developer', 'support']
const isStaff = (p: Persona) => p === 'developer' || p === 'support'
const LOGIN_HELP = 'Run "pnpm -s --filter @invoice-os/e2e ctl login --help".'
const usage = (message: string) => new CtlError(message, LOGIN_HELP, 2)
const PLAYWRIGHT_CLI = 'pnpm -s --filter @invoice-os/e2e exec playwright-cli'

// Requests the gateway sees: register + grant per account (provisionAll), one grant (regrant). D31.
const PROVISION_WRITES = 2 * ACCOUNT_KEYS.length

const productionRefusal = () =>
  new CtlError(
    'login refuses production: agents verify UI on PR environments, and production has no demo account (FLOWUPD-01 D6).',
    'Use --env pr-<N>. For production use read-only probes: the API, the database, Railway logs, Sentry.',
    1,
  )

export function parseLoginArgs(positionals: string[], flags: Record<string, string | undefined>): LoginRequest {
  const persona = positionals[0] as Persona
  if (positionals.length !== 1 || !PERSONAS.includes(persona)) {
    throw usage(`unknown persona: ${positionals.join(' ') || '(none)'}; use firm, inhouse, developer or support`)
  }
  const env = flags.env
  if (env === undefined || !/^(pr-[0-9]+|production)$/.test(env)) {
    throw usage(`--env must be pr-<N> or production, got: ${env ?? '(none)'}`)
  }
  if (env === 'production') throw productionRefusal()
  if (flags.role !== undefined && isStaff(persona)) {
    throw new CtlError('--role applies to firm and inhouse only: developer and support are staff accounts with no tenant role.', 'Drop --role.', 2)
  }
  const role = (flags.role ?? 'admin') as Role
  if (!ROLES.includes(role)) throw usage(`--role must be admin|preparer|reviewer, got: ${flags.role}`)
  return { persona, env, role, session: flags.session ?? process.env.PLAYWRIGHT_CLI_SESSION ?? 'default' }
}

export function createStore(root: string): Store {
  const dir = (env: string) => path.join(root, '.ralph', 'ctl', env)
  const file = (env: string) => path.join(dir(env), 'accounts.json')
  return {
    dir,
    statePath: (env, key) => path.join(dir(env), `${key}.json`),
    read(env) {
      if (!existsSync(file(env))) return undefined
      const hint = `delete ${file(env)} and run login again; that re-creates all 8 accounts`
      let stored: StoredAccounts
      try {
        stored = JSON.parse(readFileSync(file(env), 'utf8'))
      } catch {
        throw new CtlError(`${file(env)} is not valid JSON`, hint, 1)
      }
      if (typeof stored?.environmentId !== 'string' || !stored.accounts || ACCOUNT_KEYS.some((k) => !stored.accounts[k])) {
        throw new CtlError(`${file(env)} is missing the environment id or one of the 8 accounts`, hint, 1)
      }
      return stored
    },
    write(env, stored) {
      mkdirSync(dir(env), { recursive: true })
      const tmp = `${file(env)}.tmp`
      writeFileSync(tmp, JSON.stringify(stored, null, 2), { mode: 0o600 })
      chmodSync(tmp, 0o600)
      renameSync(tmp, file(env))
    },
  }
}

// Returns only when all 8 succeed; the caller writes the store after, so a failure leaves nothing (D36).
// ceiling: a retry after a staff failure adds two more staff rows; clean them with the PR environment.
export async function provisionAll(): Promise<Accounts> {
  const { ensureMember, e2eMember } = await import('../realAccounts')
  const { TENANTS } = await import('../topology/targets')
  const { provisionStaffAccount } = await import('../api/client')
  const accounts = {} as Accounts
  for (const [persona, tenantId, kind] of [
    ['firm', TENANTS.a.id, 'firm'],
    ['inhouse', TENANTS.b.id, 'in_house'],
  ] as const) {
    for (const role of ROLES) {
      await ensureMember(tenantId, kind, role)
      const { email, password, displayName } = e2eMember(tenantId, role)
      accounts[`${persona}-${role}`] = { email, password, displayName, tenantId, role }
    }
  }
  for (const key of ['developer', 'support'] as const) {
    const { email, password, userId } = await provisionStaffAccount(`ctl-${key}`)
    accounts[key] = { email, password, userId }
  }
  return accounts
}

const throttleHint = (status: number) => (status === 429 || status === 503 ? 'Throttled or hand-off store full; retry later.' : 'Unexpected gateway answer.')

// The sign-in side effects (one stored hand-off code, one throttle slot) are accepted, D28.
export async function credentialsValid(account: Account): Promise<boolean> {
  const { rawFetch, mintSignInState } = await import('../api/client')
  const res = await rawFetch('/auth/sign-in', { method: 'POST', body: { email: account.email, password: account.password, state: mintSignInState() } })
  if (res.status === 200) return true
  if (res.status === 401) return false
  throw new CtlError(`sign-in preflight answered ${res.status}`, throttleHint(res.status), 1)
}

export async function apiRole(account: Account): Promise<ApiRole> {
  const { signInSession, me, ApiError } = await import('../api/client')
  let token: string
  try {
    token = (await signInSession(account.email, account.password)).access_token
  } catch (e) {
    if (!(e instanceof ApiError)) throw e
    if (e.status === 401) return 'gone'
    if (e.status === 429 || e.status === 503) throw new CtlError(`API sign-in answered ${e.status}`, throttleHint(e.status), 1)
    throw e
  }
  try {
    const { tenant, user } = await me(token)
    return { role: user.role, tenantId: tenant.id }
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) return { role: null }
    throw e
  }
}

// Not through ensureMember: its per-process cache would skip the grant after provisionAll (D32).
export async function regrant(_key: AccountKey, account: Account): Promise<void> {
  const { signInSession, subjectOf, grantMembership } = await import('../api/client')
  const token = (await signInSession(account.email, account.password)).access_token
  await grantMembership({
    user_id: subjectOf(token),
    tenant_id: account.tenantId as string,
    role: account.role as Role,
    display_name: account.displayName as string,
    email: account.email,
  })
}

// 'ready' only if the ready promise wins; both promises get a rejection handler (D37).
export function raceReady(ready: Promise<unknown>, bounced: Promise<unknown>): Promise<'ready' | 'stale'> {
  return Promise.race([
    ready.then(() => 'ready' as const, () => 'stale' as const),
    bounced.then(() => 'stale' as const, () => 'stale' as const),
  ])
}

const NEEDED: Record<Persona, ServiceLabel[]> = {
  firm: ['landing', 'app', 'gateway'],
  inhouse: ['landing', 'app', 'gateway'],
  developer: ['gateway', 'ops-console'],
  support: ['gateway', 'support-console'],
}

export async function login(req: LoginRequest, deps: LoginDeps): Promise<LoginResult> {
  const { persona, env, role } = req
  if (env === 'production') throw productionRefusal()
  const staff = isStaff(persona)
  const key = (staff ? persona : `${persona}-${role}`) as AccountKey

  const resolved = await deps.resolveEnv(env)
  const dark = resolved.dark.filter((d) => NEEDED[persona].includes(d))
  if (dark.length > 0) {
    throw new CtlError(`${dark.join(', ')} is dark for ${persona}`, "Not an app bug. The deploy gate's verify-spa-domains step recreates the domain; re-run the gate or report it.", 1)
  }
  // The e2e helpers read these at module scope, so they load only after this (D30).
  for (const [name, value] of Object.entries(resolved.urls)) if (value) process.env[name] = value

  let saved = deps.store.read(env)
  let created = false
  let gatewayWrites = 0
  // ceiling: no lock, two concurrent first logins on one environment each create the staff accounts; add a lock file if QA sessions share a worktree.
  const recreate = async () => {
    saved = { environmentId: resolved.environmentId, createdAt: new Date().toISOString(), accounts: await deps.provisionAll() }
    deps.store.write(env, saved)
    created = true
    gatewayWrites += PROVISION_WRITES
  }
  const account = () => (saved as StoredAccounts).accounts[key]
  if (saved?.environmentId !== resolved.environmentId) await recreate()

  if (!staff) {
    let read = await deps.apiRole(account())
    if (read === 'gone') {
      await recreate()
      read = await deps.apiRole(account())
    }
    if (read === 'gone') throw new CtlError(`the API refused ${account().email} right after it was created`, 'Retry; if it repeats, the gateway sign-in is failing.', 1)
    if (read.tenantId !== undefined && read.tenantId !== account().tenantId) {
      throw new CtlError(`${key} belongs to tenant ${read.tenantId}, expected ${account().tenantId}`, 'Delete the accounts.json of this environment and run login again.', 1)
    }
    if (read.role !== role) {
      await deps.regrant(key, account())
      gatewayWrites++
      read = await deps.apiRole(account())
      if (read === 'gone' || read.role !== role) {
        const actual = read === 'gone' ? 'sign-in refused' : (read.role ?? '403, no active membership')
        throw new CtlError(`expected role ${role} for ${key}, got ${actual} after one re-grant`, 'Check the mock grant route and the membership of this account.', 1)
      }
    }
  }

  const statePath = deps.store.statePath(env, key)
  let reused = false
  let browserRole: string | undefined
  if (!created && existsSync(statePath)) {
    const checked = await deps.checkSavedState(key, account(), resolved.urls, statePath)
    reused = checked.ok
    browserRole = checked.role
  }
  if (!reused) {
    if (staff && !created && !(await deps.credentialsValid(account()))) await recreate()
    browserRole = (await deps.signInFresh(key, account(), resolved.urls, statePath)).role
  }
  if (!staff && browserRole !== undefined && browserRole !== role) {
    throw new CtlError(`the browser reads role ${browserRole} for ${key}, expected ${role}`, 'The role changed while login ran. Run login again.', 1)
  }

  const url = (persona === 'developer' ? resolved.urls.OPS_CONSOLE_URL : persona === 'support' ? resolved.urls.SUPPORT_CONSOLE_URL : resolved.urls.APP_URL) as string
  const storageState = path.resolve(statePath)
  const cli = `${PLAYWRIGHT_CLI} -s=${req.session}`
  return {
    env,
    persona,
    ...(staff ? {} : { role }),
    email: account().email,
    tenantId: account().tenantId,
    url,
    storageState,
    created,
    reused,
    gatewayWrites,
    next: [
      `${PLAYWRIGHT_CLI} list --json   # run open only if session ${req.session} is absent: open restarts an open session`,
      `${cli} open`,
      `${cli} state-load ${storageState}`,
      `${cli} goto ${url}`,
    ],
  }
}

// Real dependencies, loaded when the command runs. root = the repo root that holds .ralph/.
export async function defaultDeps(root: string): Promise<LoginDeps> {
  const { resolveEnvByName } = await import('./railway')
  const { signInFresh, checkSavedState } = await import('./browserSession')
  return { resolveEnv: resolveEnvByName, store: createStore(root), provisionAll, apiRole, credentialsValid, regrant, signInFresh, checkSavedState }
}

export async function loginCommand(positionals: string[], flags: Record<string, string | undefined>): Promise<unknown> {
  const req = parseLoginArgs(positionals, flags)
  return login(req, await defaultDeps(path.resolve(import.meta.dirname, '../..')))
}
