// Mock sign-in for the Platform app (M2-13). Picking a persona mints a GoTrue-shaped
// JWT through the gateway's dev mock issuer (POST /auth/login) and makes the first real
// authenticated fetch in the codebase — GET /api/tenancy/v1/me — to resolve the caller's
// tenant under RLS. The persona → {tenant_id, role} mapping is the seam that GoTrue
// replaces at M8: same round trip, same /me, only the token source changes.
//
// The round trip runs only when VITE_GATEWAY_URL is configured. With no gateway (the
// default showcase build), signIn resolves to an UNVERIFIED session with no network
// call — so a deployed SPA with no backend behind it stays a clean, error-free mock.

import { apiFetch, gatewayBase } from '@invoice-os/api-client'
import type { Mode } from './types'

export type PersonaId = 'firm' | 'inhouse'

export interface Persona {
  id: PersonaId
  name: string
  title: string
  initials: string
  org: string
  email: string
  access: string
  mode: Mode
  // Mock identity the gateway stamps into the JWT (matches db/seed.dev.sql tenants).
  subject: string
  tenantId: string
  role: string
}

// The two tenant-scoped Platform personas. The support/operator persona lives on the
// landing page and routes to the Ops Console (M7), not here — every Platform route is
// tenant-scoped, so an operator with no tenant is refused by the gateway.
export const APP_PERSONAS: Record<PersonaId, Persona> = {
  firm: {
    id: 'firm',
    name: 'Chinedu Okafor',
    title: 'Firm accountant',
    initials: 'CO',
    org: 'Okafor & Partners',
    email: 'c.okafor@okafor.ng',
    access: 'PLATFORM · FIRM',
    mode: 'firm',
    subject: 'c0000000-0000-0000-0000-000000000001',
    tenantId: '11111111-1111-1111-1111-111111111111',
    role: 'authenticated',
  },
  inhouse: {
    id: 'inhouse',
    name: 'Ngozi Balogun',
    title: 'In-house accountant',
    initials: 'NB',
    org: 'Honeywell Group',
    email: 'n.balogun@honeywell.ng',
    access: 'PLATFORM · IN-HOUSE',
    mode: 'inhouse',
    subject: 'c0000000-0000-0000-0000-000000000002',
    tenantId: '22222222-2222-2222-2222-222222222222',
    role: 'authenticated',
  },
}

export interface Me {
  tenant: { id: string; name: string }
  user: { id: string; role: string }
}

export interface Session {
  persona: Persona
  token: string | null
  me: Me | null
  // true when the /me round trip succeeded — the tenant identity was proven against
  // the live backend, not just assumed from the persona.
  verified: boolean
}

// signIn mints a token and reads /me when a gateway is configured; otherwise it returns
// an unverified mock session without touching the network. It THROWS only when a gateway
// is configured but the round trip fails — the caller decides whether to degrade.
export async function signIn(persona: Persona): Promise<Session> {
  const base = gatewayBase()
  if (!base) {
    return { persona, token: null, me: null, verified: false }
  }

  // 1. Mint a GoTrue-shaped JWT via the gateway's dev mock issuer.
  const { access_token: token } = await apiFetch<{ access_token: string }>(`${base}/auth/login`, {
    method: 'POST',
    body: { subject: persona.subject, role: persona.role, tenant_id: persona.tenantId },
  })

  // 2. The first real authenticated fetch of app data: the gateway verifies the token,
  //    injects the tenant, and tenancy resolves it under RLS (SET LOCAL).
  const me = await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token })

  return { persona, token, me, verified: true }
}
