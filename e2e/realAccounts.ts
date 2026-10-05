// RED stub (AUTH-15-03 Mode A): wrong-but-typed bodies so the tests import and fail on their assertions.
import type { TenantKind } from './api/client'

export interface E2EMember {
  email: string
  password: string
  displayName: string
}

let calls = 0

export function e2eMember(_tenantId: string): E2EMember {
  calls += 1
  return { email: 'stub@example.com', password: `p${calls}`, displayName: 'stub' }
}

export function isSeededMember(userId: string): boolean {
  return !/^c0000000-0000-0000-0000-\d{12}$/.test(userId)
}

export async function ensureMember(tenantId: string, _kind: TenantKind): Promise<E2EMember> {
  return e2eMember(tenantId)
}
