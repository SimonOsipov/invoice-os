// Stub (D39): signatures only, wrong values, until FLOWUPD-01-05 is implemented.
import type { Account, AccountKey } from './login'
import type { EnvResult } from './railway'

export async function signInFresh(_key: AccountKey, _account: Account, _urls: EnvResult['urls'], _statePath: string): Promise<{ role?: string }> {
  return {}
}

export async function checkSavedState(_key: AccountKey, _account: Account, _urls: EnvResult['urls'], _statePath: string): Promise<{ ok: boolean; role?: string }> {
  return { ok: false }
}
