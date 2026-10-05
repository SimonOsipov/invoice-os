import { grantMembership, rawFetch, signInSession, subjectOf, type TenantKind } from './api/client'

export interface E2EMember {
  email: string
  password: string
  displayName: string
}

// A changed constant locks every account a long-lived PR environment already holds.
const PASSWORD_PREFIX = 'e2e-member-pw-'

// Tenant ids lead with 1 (firm) or 2 (in-house), as in db/seed.e2e-shards.sql.
export function e2eMember(tenantId: string): E2EMember {
  return {
    email: `e2e-member-${tenantId}@example.com`,
    password: PASSWORD_PREFIX + tenantId,
    displayName: tenantId.startsWith('1') ? 'E2E Firm Admin' : 'E2E In-house Admin',
  }
}

// The seed's user block (db/seed.dev.sql); a GoTrue subject is a random UUID.
export function isSeededMember(userId: string): boolean {
  return /^c0000000-0000-0000-0000-\d{12}$/.test(userId)
}

const ensured = new Map<string, Promise<E2EMember>>()

// Registers (idempotent), signs in and makes the account an admin of the tenant, once per worker.
export function ensureMember(tenantId: string, kind: TenantKind): Promise<E2EMember> {
  if ((tenantId.startsWith('1') ? 'firm' : 'in_house') !== kind) {
    return Promise.reject(new Error(`ensureMember: tenant ${tenantId} is not a ${kind} tenant (its id prefix says otherwise)`))
  }
  let done = ensured.get(tenantId)
  if (!done) {
    done = provision(tenantId, kind)
    ensured.set(tenantId, done)
    done.catch(() => ensured.delete(tenantId))
  }
  return done
}

async function provision(tenantId: string, kind: TenantKind): Promise<E2EMember> {
  const member = e2eMember(tenantId)
  const { email, password, displayName } = member
  // internal/gateway/register.go answers 202 for a new and an existing address.
  const reg = await rawFetch('/auth/register', { method: 'POST', body: { email, password } })
  if (reg.status !== 202) throw new Error(`ensureMember: register of ${email} answered ${reg.status}: ${JSON.stringify(reg.body)}`)

  let accessToken: string
  try {
    accessToken = (await signInSession(email, password)).access_token
  } catch (e) {
    throw new Error(`ensureMember: sign-in of ${email} (${kind} ${tenantId}) was refused: ${(e as Error).message}. Was the password constant in e2e/realAccounts.ts changed on a long-lived environment?`)
  }

  try {
    // internal/gateway/mockmember.go answers 204.
    await grantMembership({ user_id: subjectOf(accessToken), tenant_id: tenantId, role: 'admin', display_name: displayName, email })
  } catch (e) {
    throw new Error(`ensureMember: grant for ${email} on ${tenantId} failed: ${(e as Error).message}`)
  }
  return member
}
