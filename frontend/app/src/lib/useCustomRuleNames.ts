import { gatewayBase, useAsync } from '@invoice-os/api-client'

import type { AuthedFetch } from './portfolio'
import { getCustomRuleNames } from './rulesApi'

const NONE: Record<string, string> = {}

// Custom rule names by key. Empty while loading, on a failed read and with no gateway, so a
// surface shows the generic label rather than an error.
export function useCustomRuleNames(authedFetch: AuthedFetch): Record<string, string> {
  const base = gatewayBase()
  const names = useAsync<Record<string, string>>(
    () => (base ? getCustomRuleNames(authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  return names.data ?? NONE
}
