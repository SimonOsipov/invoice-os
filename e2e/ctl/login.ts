// Stub (D39): signatures only, wrong values, until FLOWUPD-01-05 is implemented.
import type { EnvResult } from './railway'

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

export const ACCOUNT_KEYS: readonly AccountKey[] = []

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

export function parseLoginArgs(positionals: string[], flags: Record<string, string | undefined>): LoginRequest {
  return { persona: positionals[0] as Persona, env: flags.env ?? '', role: 'preparer', session: 'stub' }
}

export function createStore(_root: string): Store {
  return { dir: () => '', statePath: () => '', read: () => undefined, write: () => {} }
}

export async function provisionAll(): Promise<Accounts> {
  return {} as Accounts
}

export async function credentialsValid(_account: Account): Promise<boolean> {
  return false
}

export async function apiRole(_account: Account): Promise<ApiRole> {
  return 'gone'
}

export async function regrant(_key: AccountKey, _account: Account): Promise<void> {}

// Inverted on purpose: wrong values, but both rejections are handled.
export async function raceReady(ready: Promise<unknown>, bounced: Promise<unknown>): Promise<'ready' | 'stale'> {
  return Promise.race([
    ready.then(() => 'stale' as const, () => 'ready' as const),
    bounced.then(() => 'ready' as const, () => 'ready' as const),
  ])
}

export async function login(req: LoginRequest, _deps: LoginDeps): Promise<LoginResult> {
  return { env: req.env, persona: req.persona, role: req.role, email: '', url: '', storageState: '', created: false, reused: false, gatewayWrites: -1, next: [] }
}
