// The real-account helpers, with the gateway stubbed at global fetch.
// topology/targets.ts reads GATEWAY_URL at import, so every import is dynamic after the env is set.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { UNITS } from './topology/shards'

type Realm = typeof import('./realAccounts')
type Targets = typeof import('./topology/targets')

const SUBJECT = 'a1b2c3d4-0000-4000-8000-000000000001'
const ACCESS_TOKEN = `h.${Buffer.from(JSON.stringify({ sub: SUBJECT })).toString('base64url')}.s`

type Answer = { status: number; body?: unknown }
type Step = 'register' | 'sign-in' | 'exchange' | 'grant'
const PATHS: Record<Step, string> = {
  register: '/auth/register',
  'sign-in': '/auth/sign-in',
  exchange: '/auth/exchange',
  grant: '/auth/mock/member',
}
const OK: Record<Step, Answer> = {
  register: { status: 202, body: {} },
  'sign-in': { status: 200, body: { code: 'code-1' } },
  exchange: { status: 200, body: { access_token: ACCESS_TOKEN, refresh_token: 'refresh-1' } },
  grant: { status: 204 },
}

let fetched: { url: string; method: string; body: any }[] = []

// Answers by path; `override` replaces one step's answer.
function stubGateway(override: Partial<Record<Step, Answer>> = {}): void {
  fetched = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: { method?: string; body?: string }) => {
      fetched.push({ url, method: init?.method ?? 'GET', body: init?.body ? JSON.parse(init.body) : undefined })
      const step = (Object.keys(PATHS) as Step[]).find((s) => url.endsWith(PATHS[s]))
      if (!step) return new Response('{}', { status: 404 })
      const { status, body } = { ...OK, ...override }[step]
      return new Response(body === undefined ? null : JSON.stringify(body), { status })
    }),
  )
}

async function load(): Promise<{ realm: Realm; targets: Targets }> {
  vi.resetModules()
  process.env.GATEWAY_URL = 'https://gateway.test'
  process.env.APP_URL = 'https://app.test'
  return { realm: await import('./realAccounts'), targets: await import('./topology/targets') }
}

// The six tenants the topology suite signs in to: the two seeded ones and each shard's pair.
function sixTenantIds(targets: Targets): string[] {
  const shards = UNITS.flatMap((u) => (u.tenants ? [u.tenants.firm, u.tenants.inHouse] : []))
  return [targets.TENANTS.a.id, targets.TENANTS.b.id, ...shards]
}

async function rejection(p: Promise<unknown>): Promise<string> {
  try {
    await p
  } catch (e) {
    return (e as Error).message
  }
  throw new Error('expected the promise to reject, but it resolved')
}

beforeEach(() => stubGateway())
afterEach(() => vi.unstubAllGlobals())

describe('e2eMember', () => {
  it('e2eMember is deterministic', async () => {
    const { realm, targets } = await load()
    const id = targets.TENANTS.a.id
    expect(id).toBe('11111111-1111-1111-1111-111111111111')

    expect(realm.e2eMember(id)).toEqual(realm.e2eMember(id))
    expect(realm.e2eMember(id).email).toBe(`e2e-member-${id}@example.com`)
  })

  it('e2eMember is distinct per tenant', async () => {
    const { realm, targets } = await load()
    const ids = sixTenantIds(targets)
    expect(ids).toHaveLength(6)
    expect(new Set(ids).size).toBe(6)

    const members = ids.map((id) => realm.e2eMember(id))

    expect(new Set(members.map((m) => m.email)).size).toBe(6)
    expect(new Set(members.map((m) => m.password)).size).toBe(6)
  })

  it('e2eMember password floor', async () => {
    const { realm, targets } = await load()
    const ids = sixTenantIds(targets)
    expect(ids.length).toBeGreaterThan(0)

    for (const id of ids) expect(realm.e2eMember(id).password.length, id).toBeGreaterThanOrEqual(16)
  })

  it('e2eMember display name follows kind', async () => {
    const { realm, targets } = await load()
    const firm = realm.e2eMember(targets.TENANTS.a.id).displayName
    const inHouse = realm.e2eMember(targets.TENANTS.b.id).displayName

    expect(firm).toBe('E2E Firm Admin')
    expect(inHouse).toBe('E2E In-house Admin')
    expect(firm).not.toBe(inHouse)

    const shards = UNITS.filter((u) => u.tenants)
    expect(shards.length).toBeGreaterThan(0)
    for (const u of shards) {
      expect(realm.e2eMember(u.tenants!.firm).displayName, u.name).toBe(firm)
      expect(realm.e2eMember(u.tenants!.inHouse).displayName, u.name).toBe(inHouse)
    }
  })

  it("e2eMember admin is today's record", async () => {
    const { realm, targets } = await load()
    const id = targets.TENANTS.a.id

    expect(realm.e2eMember(id, 'admin')).toEqual(realm.e2eMember(id))
    expect(realm.e2eMember(id, 'admin').email).toBe(`e2e-member-${id}@example.com`)

    // Literals from origin/main's e2eMember: a changed value locks the accounts a PR environment already holds.
    expect(realm.e2eMember('11111111-1111-1111-1111-111111111111')).toEqual({
      email: 'e2e-member-11111111-1111-1111-1111-111111111111@example.com',
      password: 'e2e-member-pw-11111111-1111-1111-1111-111111111111',
      displayName: 'E2E Firm Admin',
    })
    expect(realm.e2eMember('22222222-2222-2222-2222-222222222222', 'admin')).toEqual({
      email: 'e2e-member-22222222-2222-2222-2222-222222222222@example.com',
      password: 'e2e-member-pw-22222222-2222-2222-2222-222222222222',
      displayName: 'E2E In-house Admin',
    })
  })

  it('e2eMember names each role', async () => {
    const { realm, targets } = await load()
    const firm = targets.TENANTS.a.id
    const inHouse = targets.TENANTS.b.id

    const preparer = realm.e2eMember(firm, 'preparer')
    const reviewer = realm.e2eMember(firm, 'reviewer')

    expect(preparer.email).toBe(`e2e-member-${firm}-preparer@example.com`)
    expect(reviewer.email).toBe(`e2e-member-${firm}-reviewer@example.com`)
    expect(preparer.displayName).toBe('E2E Firm Preparer')
    expect(reviewer.displayName).toBe('E2E Firm Reviewer')
    expect(realm.e2eMember(inHouse, 'reviewer').displayName).toBe('E2E In-house Reviewer')
    expect(realm.e2eMember(firm, 'reviewer')).toEqual(reviewer)
    expect(preparer.password).toBe(`e2e-member-pw-${firm}-preparer`)
    expect(reviewer.password).toBe(`e2e-member-pw-${firm}-reviewer`)
    expect(realm.e2eMember(inHouse, 'preparer').displayName).toBe('E2E In-house Preparer')

    const shards = UNITS.filter((u) => u.tenants)
    expect(shards.length).toBeGreaterThan(0)
    for (const u of shards) {
      expect(realm.e2eMember(u.tenants!.firm, 'preparer').displayName, u.name).toBe('E2E Firm Preparer')
      expect(realm.e2eMember(u.tenants!.inHouse, 'reviewer').displayName, u.name).toBe('E2E In-house Reviewer')
    }
  })

  it('role passwords are distinct and keep the floor', async () => {
    const { realm, targets } = await load()
    const id = targets.TENANTS.a.id
    expect(realm.E2E_MEMBER_ROLES.length).toBe(3)
    expect([...realm.E2E_MEMBER_ROLES]).toEqual(['admin', 'preparer', 'reviewer'])

    const passwords = realm.E2E_MEMBER_ROLES.map((r) => realm.e2eMember(id, r).password)

    expect(new Set(passwords).size).toBe(3)
    for (const p of passwords) expect(p.length).toBeGreaterThanOrEqual(16)

    const ids = sixTenantIds(targets)
    const all = ids.flatMap((t) => realm.E2E_MEMBER_ROLES.map((r) => realm.e2eMember(t, r)))
    expect(all).toHaveLength(18)
    expect(new Set(all.map((m) => m.email)).size).toBe(18)
    expect(new Set(all.map((m) => m.password)).size).toBe(18)
  })
})

describe('isE2eMemberEmail', () => {
  it('isE2eMemberEmail accepts the three role emails of the tenant', async () => {
    const { realm, targets } = await load()
    const id = targets.TENANTS.a.id
    expect(realm.E2E_MEMBER_ROLES.length).toBe(3)

    for (const r of realm.E2E_MEMBER_ROLES) expect(realm.isE2eMemberEmail(id, realm.e2eMember(id, r).email), r).toBe(true)
  })

  it("isE2eMemberEmail rejects a stranger and another tenant's member", async () => {
    const { realm, targets } = await load()
    const id = targets.TENANTS.a.id
    expect(realm.isE2eMemberEmail(id, realm.e2eMember(id).email)).toBe(true)

    expect(realm.isE2eMemberEmail(id, 'c.okafor@okafor.ng')).toBe(false)
    expect(realm.isE2eMemberEmail(id, realm.e2eMember(targets.TENANTS.b.id, 'reviewer').email)).toBe(false)
    expect(realm.isE2eMemberEmail(id, `e2e-member-${id}-owner@example.com`)).toBe(false)
    expect(realm.isE2eMemberEmail(id, '')).toBe(false)

    const admin = realm.e2eMember(id).email
    const reviewer = realm.e2eMember(id, 'reviewer').email
    const other = targets.TENANTS.b.id
    for (const email of [
      `e2e-member-${id}-admin@example.com`,
      admin.toUpperCase(),
      reviewer.toUpperCase(),
      `${admin}\n`,
      `${reviewer}\n`,
      ` ${admin}`,
      `${reviewer} `,
      `x${admin}`,
      `${reviewer}.evil`,
      `${admin}@example.com`,
      reviewer.replace('@example.com', '@example.org'),
      `e2e-member-${id}-preparer-extra@example.com`,
      `e2e-member-${id}-@example.com`,
      realm.e2eMember(other).email,
      realm.e2eMember(other, 'preparer').email,
      `e2e-member-${id}`,
    ]) {
      expect(realm.isE2eMemberEmail(id, email), JSON.stringify(email)).toBe(false)
    }
    expect(realm.isE2eMemberEmail(other, realm.e2eMember(other).email)).toBe(true)
    expect(realm.isE2eMemberEmail(other, admin)).toBe(false)
  })
})

describe('isSeededMember', () => {
  it('isSeededMember accepts the seed block', async () => {
    const { realm, targets } = await load()
    const seeded = [...targets.TENANTS.a.members, ...targets.TENANTS.b.members]
    expect(targets.TENANTS.a.members.length).toBeGreaterThan(0)
    expect(targets.TENANTS.b.members.length).toBeGreaterThan(0)

    for (const id of seeded) expect(realm.isSeededMember(id), id).toBe(true)
  })

  it('isSeededMember refuses others', async () => {
    const { realm, targets } = await load()
    expect(realm.isSeededMember(targets.TENANTS.a.subject)).toBe(true)

    expect(realm.isSeededMember(crypto.randomUUID())).toBe(false)
    expect(realm.isSeededMember('c0000000-0000-0000-0000-00000000001')).toBe(false)
    expect(realm.isSeededMember('')).toBe(false)
    expect(realm.isSeededMember('c0000000-0000-0000-0000-0000000000011')).toBe(false)
    expect(realm.isSeededMember('xc0000000-0000-0000-0000-000000000001')).toBe(false)
    expect(realm.isSeededMember('C0000000-0000-0000-0000-000000000001')).toBe(false)
    expect(realm.isSeededMember('c0000000-0000-0000-0000-000000000001\n')).toBe(false)
    for (const { tenants } of UNITS) {
      if (tenants) expect([tenants.firm, tenants.inHouse].some(realm.isSeededMember)).toBe(false)
    }
  })
})

describe('ensureMember', () => {
  const FIRM = '11111111-1111-1111-1111-111111111111'
  const IN_HOUSE = '22222222-2222-2222-2222-222222222222'

  it('ensureMember refuses a kind that disagrees with the tenant id prefix', async () => {
    const { realm } = await load()

    const asInHouse = await rejection(realm.ensureMember(FIRM, 'in_house'))
    const asFirm = await rejection(realm.ensureMember(IN_HOUSE, 'firm'))

    expect(asInHouse).toContain(FIRM)
    expect(asFirm).toContain(IN_HOUSE)
    expect(fetched).toHaveLength(0)
  })

  it('ensureMember names a refused register', async () => {
    const { realm } = await load()
    stubGateway({ register: { status: 500, body: { error: 'boom' } } })

    const message = await rejection(realm.ensureMember(FIRM, 'firm'))

    expect(message).toContain('register')
    expect(message).toContain(realm.e2eMember(FIRM).email)
    expect(message).toMatch(/^ensureMember: register /)
  })

  it('ensureMember names a refused sign-in', async () => {
    const { realm } = await load()
    stubGateway({ 'sign-in': { status: 400, body: { error: 'invalid credentials' } } })

    const message = await rejection(realm.ensureMember(FIRM, 'firm'))

    expect(message).toContain('sign-in')
    expect(message).toContain(realm.e2eMember(FIRM).email)
    expect(message).toContain('e2e/realAccounts.ts')
    expect(message).toMatch(/^ensureMember: sign-in /)
  })

  it('ensureMember names a refused grant', async () => {
    const { realm } = await load()
    stubGateway({ grant: { status: 502, body: { error: 'membership grant unavailable' } } })

    const message = await rejection(realm.ensureMember(FIRM, 'firm'))

    expect(message).toContain('grant')
    expect(message).toContain('502')
    expect(message).toContain(realm.e2eMember(FIRM).email)
    expect(message).toMatch(/^ensureMember: grant /)
  })

  it('ensureMember registers, signs in and grants the e2e member as admin', async () => {
    const { realm } = await load()
    const member = realm.e2eMember(IN_HOUSE)

    await realm.ensureMember(IN_HOUSE, 'in_house')

    expect(fetched.map((c) => `${c.method} ${new URL(c.url).pathname}`)).toEqual([
      'POST /auth/register',
      'POST /auth/sign-in',
      'POST /auth/exchange',
      'POST /auth/mock/member',
    ])
    expect(fetched[0].body).toEqual({ email: member.email, password: member.password })
    expect(fetched[1].body).toMatchObject({ email: member.email, password: member.password })
    expect(fetched[3].body).toEqual({
      user_id: SUBJECT,
      tenant_id: IN_HOUSE,
      role: 'admin',
      display_name: 'E2E In-house Admin',
      email: member.email,
    })

    stubGateway()
    const reviewer = realm.e2eMember(IN_HOUSE, 'reviewer')

    await realm.ensureMember(IN_HOUSE, 'in_house', 'reviewer')

    expect(fetched).toHaveLength(4)
    expect(fetched[0].body).toEqual({ email: reviewer.email, password: reviewer.password })
    expect(fetched[3].body).toEqual({
      user_id: SUBJECT,
      tenant_id: IN_HOUSE,
      role: 'reviewer',
      display_name: 'E2E In-house Reviewer',
      email: reviewer.email,
    })
  })

  it.each([400, 429, 503])('ensureMember refuses a register that answers %i', async (status) => {
    const { realm } = await load()
    stubGateway({ register: { status, body: { error: 'no' } } })

    const message = await rejection(realm.ensureMember(FIRM, 'firm'))

    expect(message).toContain('register')
    expect(message).toContain(String(status))
    expect(fetched).toHaveLength(1)
  })

  it('ensureMember does not cache a failed provision', async () => {
    const { realm } = await load()
    stubGateway({ register: { status: 500, body: { error: 'boom' } } })
    await rejection(realm.ensureMember(FIRM, 'firm'))
    expect(fetched).toHaveLength(1)

    stubGateway()
    await realm.ensureMember(FIRM, 'firm')
    expect(fetched).toHaveLength(4)

    await realm.ensureMember(FIRM, 'firm')
    expect(fetched).toHaveLength(4)

    stubGateway({ register: { status: 500, body: { error: 'boom' } } })
    await rejection(realm.ensureMember(FIRM, 'firm', 'reviewer'))
    expect(fetched).toHaveLength(1)

    stubGateway()
    await realm.ensureMember(FIRM, 'firm', 'reviewer')
    expect(fetched).toHaveLength(4)
    expect((await realm.ensureMember(FIRM, 'firm', 'reviewer')).email).toBe(realm.e2eMember(FIRM, 'reviewer').email)
    await realm.ensureMember(FIRM, 'firm')
    expect(fetched).toHaveLength(4)
  })

  it('ensureMember shares one in-flight provision between concurrent callers', async () => {
    const { realm } = await load()

    const [a, b] = await Promise.all([realm.ensureMember(FIRM, 'firm'), realm.ensureMember(FIRM, 'firm')])

    expect(a).toEqual(b)
    expect(fetched).toHaveLength(4)

    stubGateway()
    const [r1, r2, p] = await Promise.all([
      realm.ensureMember(FIRM, 'firm', 'reviewer'),
      realm.ensureMember(FIRM, 'firm', 'reviewer'),
      realm.ensureMember(FIRM, 'firm', 'preparer'),
    ])

    expect(r1).toEqual(r2)
    expect(r1).toEqual(realm.e2eMember(FIRM, 'reviewer'))
    expect(p).toEqual(realm.e2eMember(FIRM, 'preparer'))
    const registers = fetched.filter((c) => c.url.endsWith('/auth/register')).map((c) => c.body.email)
    expect(registers.sort()).toEqual([realm.e2eMember(FIRM, 'preparer').email, realm.e2eMember(FIRM, 'reviewer').email].sort())
    const grants = fetched.filter((c) => c.url.endsWith('/auth/mock/member')).map((c) => c.body.role)
    expect(grants.sort()).toEqual(['preparer', 'reviewer'])
  })

  it('ensureMember caches per tenant', async () => {
    const { realm } = await load()

    await realm.ensureMember(FIRM, 'firm')
    await realm.ensureMember(FIRM, 'firm')
    await realm.ensureMember(IN_HOUSE, 'in_house')

    expect(fetched).toHaveLength(8)

    await realm.ensureMember(FIRM, 'firm', 'reviewer')
    expect(fetched).toHaveLength(12)
    await realm.ensureMember(FIRM, 'firm', 'reviewer')
    await realm.ensureMember(FIRM, 'firm')
    expect(fetched).toHaveLength(12)

    await realm.ensureMember(IN_HOUSE, 'in_house', 'reviewer')
    expect(fetched).toHaveLength(16)
    expect(fetched.slice(12)[0].body.email).toBe(realm.e2eMember(IN_HOUSE, 'reviewer').email)
    await realm.ensureMember(IN_HOUSE, 'in_house', 'reviewer')
    expect(fetched).toHaveLength(16)

    const firmRegisters = fetched.filter((c) => c.url.endsWith('/auth/register') && c.body.email.startsWith(`e2e-member-${FIRM}`))
    expect(firmRegisters.map((c) => c.body.email)).toEqual([realm.e2eMember(FIRM).email, realm.e2eMember(FIRM, 'reviewer').email])
  })
})
