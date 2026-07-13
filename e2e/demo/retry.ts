// M3-11 Day-30 wedge demo — transient-network retry for the seeding round-trips.
//
// ensurePortfolioSeeded() issues ~25–27 create/offboard round-trips in beforeAll
// against a possibly-cold Railway fleet on the BLOCKING dev-env gate, where a single
// transient blip would otherwise hard-fail the whole serial suite. This mirrors the
// network-only retry ensureRuleEnabled uses inline in day30.spec.ts.
import { ApiError } from '../api/client'

// withNetworkRetry(): run fn(), retrying ONLY on a transient network-kind ApiError
// (err.kind === 'network' — the reject apiFetch produces on a dropped/refused
// connection, packages/api-client/src/client.ts). A real 4xx/5xx (kind 'http', e.g. a
// validation 400/409) or a malformed-body error is NEVER retried, so the seeding logic
// stays correct. `attempts` = total tries (default 2 = initial + one retry, matching
// ensureRuleEnabled's single retry).
export async function withNetworkRetry<T>(fn: () => Promise<T>, attempts = 2): Promise<T> {
  for (let attempt = 1; ; attempt++) {
    try {
      return await fn()
    } catch (err) {
      const transient = err instanceof ApiError && err.kind === 'network'
      if (!transient || attempt >= attempts) throw err
    }
  }
}
