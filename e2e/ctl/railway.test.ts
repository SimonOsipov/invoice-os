import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CtlError, run } from './main'
import {
  envCommand,
  isDark,
  readRailwayIds,
  readRailwayToken,
  resolveEnv,
  type RailwayIds,
  type ServiceLabel,
} from './railway'

const LABELS: ServiceLabel[] = ['gateway', 'app', 'landing', 'ops-console', 'support-console']
const URL_KEYS = ['GATEWAY_URL', 'APP_URL', 'LANDING_URL', 'OPS_CONSOLE_URL', 'SUPPORT_CONSOLE_URL']
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const uuid = (n: number) => `00000000-0000-4000-8000-00000000000${n}`
const GRAPHQL = 'https://backboard.railway.com/graphql/v2'
const APP_NOT_FOUND = '{"status":"error","code":404,"message":"Application not found"}'

const YAML_KEYS = [
  `  RAILWAY_PROJECT_ID: ${uuid(1)}`,
  `  RAILWAY_DEV_ENVIRONMENT_ID: ${uuid(7)}`,
  `  RAILWAY_SVC_GATEWAY_ID: ${uuid(2)}`,
  `  RAILWAY_SVC_APP_ID: ${uuid(3)}`,
  `  RAILWAY_SVC_LANDING_ID: ${uuid(4)}`,
  `  RAILWAY_SVC_OPS_CONSOLE_ID: ${uuid(5)}`,
  `  RAILWAY_SVC_SUPPORT_CONSOLE_ID: ${uuid(6)}`,
  `  RAILWAY_SVC_POSTGRES_ID: ${uuid(8)}`,
]
const yamlOf = (lines: string[]) => ['env:', '  CI: true', ...lines, '  OTHER: x'].join('\n') + '\n'

const IDS: RailwayIds = {
  projectId: uuid(1),
  services: { gateway: uuid(2), app: uuid(3), landing: uuid(4), 'ops-console': uuid(5), 'support-console': uuid(6) },
}

type Domains = { custom: { domain: string }[] | null; service: { domain: string }[] }
interface FakeOpts {
  ids?: RailwayIds
  envs?: { id?: string; name: string; isEphemeral: boolean }[]
  hasNextPage?: boolean
  envReply?: unknown
  domains?: Partial<Record<ServiceLabel, Domains>>
  probes?: Partial<Record<ServiceLabel, { status: number; body: string }>>
}
interface Call {
  url: string
  method: string
  auth: string | null
}

const hostOf = (label: ServiceLabel) => `${label}.up.railway.app`

// Stubs global fetch: GraphQL by query kind and service id found in the body, probes by host.
function fakeRailway(opts: FakeOpts = {}) {
  const ids = opts.ids ?? IDS
  const calls: Call[] = []
  const envs = opts.envs ?? [{ name: 'pr-348', isEphemeral: true }]
  const fn = vi.fn(async (input: unknown, init?: RequestInit) => {
    const url = String(input)
    const headers = new Headers(init?.headers)
    calls.push({ url, method: init?.method ?? 'GET', auth: headers.get('authorization') })
    if (url.startsWith('https://backboard.railway.com/')) {
      const body = String(init?.body ?? '')
      if (body.includes('environments(')) {
        const reply = opts.envReply ?? {
          data: {
            environments: {
              edges: envs.map((e, i) => ({ node: { id: e.id ?? `env-${i}`, name: e.name, isEphemeral: e.isEphemeral } })),
              pageInfo: { hasNextPage: opts.hasNextPage ?? false },
            },
          },
        }
        return new Response(JSON.stringify(reply), { status: 200 })
      }
      const label = LABELS.find((l) => body.includes(ids.services[l]))
      if (body.includes('domains(') && label) {
        const d = opts.domains?.[label] ?? { custom: [], service: [{ domain: hostOf(label) }] }
        const reply = { data: { domains: { customDomains: d.custom, serviceDomains: d.service } } }
        return new Response(JSON.stringify(reply), { status: 200 })
      }
      return new Response('{"errors":[{"message":"unrecognised query"}]}', { status: 200 })
    }
    const host = new URL(url).host
    const label = LABELS.find((l) => {
      const d = opts.domains?.[l]
      return [...(d?.custom ?? []), ...(d?.service ?? [{ domain: hostOf(l) }])].some((x) => x.domain === host)
    })
    const probe = (label && opts.probes?.[label]) || { status: 200, body: '{}' }
    return new Response(probe.body, { status: probe.status })
  })
  vi.stubGlobal('fetch', fn)
  return { fn, calls, graphql: () => calls.filter((c) => c.url.startsWith('https://backboard.railway.com/')), probes: () => calls.filter((c) => !c.url.startsWith('https://backboard.railway.com/')) }
}

async function rejection(p: Promise<unknown>): Promise<Error> {
  let err: unknown
  try {
    await p
  } catch (e) {
    err = e
  }
  expect(err, 'expected a rejection').toBeInstanceOf(Error)
  return err as Error
}

function thrown(fn: () => unknown): Error {
  let err: unknown
  try {
    fn()
  } catch (e) {
    err = e
  }
  expect(err, 'expected a throw').toBeInstanceOf(Error)
  return err as Error
}

function ctlError(err: Error, code: 1 | 2): CtlError {
  expect(err).toBeInstanceOf(CtlError)
  const e = err as CtlError
  expect(e.code).toBe(code)
  return e
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('readRailwayIds', () => {
  it('readRailwayIds reads the six listed keys and ignores others', () => {
    const ids = readRailwayIds(yamlOf(YAML_KEYS))
    expect(ids.projectId).toBe(uuid(1))
    expect(ids.services).toEqual({
      gateway: uuid(2),
      app: uuid(3),
      landing: uuid(4),
      'ops-console': uuid(5),
      'support-console': uuid(6),
    })
    const dump = JSON.stringify(ids)
    expect(dump).not.toContain(uuid(7))
    expect(dump).not.toContain(uuid(8))
  })

  it('the real dev-env.yml carries all six keys', () => {
    const text = readFileSync(fileURLToPath(new URL('../../.github/workflows/dev-env.yml', import.meta.url)), 'utf8')
    const ids = readRailwayIds(text)
    expect(ids.projectId).toMatch(UUID)
    const values = LABELS.map((l) => ids.services[l])
    expect(values).toHaveLength(5)
    for (const v of values) expect(v).toMatch(UUID)
    expect(new Set(values).size).toBe(5)
  })

  it('readRailwayIds names a missing key', () => {
    expect(() => readRailwayIds(yamlOf(YAML_KEYS))).not.toThrow()
    const without = YAML_KEYS.filter((l) => !l.includes('RAILWAY_SVC_APP_ID'))
    expect(without).toHaveLength(YAML_KEYS.length - 1)
    expect(thrown(() => readRailwayIds(yamlOf(without))).message).toContain('RAILWAY_SVC_APP_ID')
  })
})

describe('readRailwayToken', () => {
  const config = (user: object) => JSON.stringify({ user })

  it('readRailwayToken returns user.accessToken', () => {
    expect(readRailwayToken(config({ token: null, accessToken: 'tok', tokenExpiresAt: 2000 }), 1000)).toBe('tok')
  })

  it('an expired token fails with the refresh hint and never echoes the token', () => {
    const text = config({ token: null, accessToken: 'secret-tok', tokenExpiresAt: 999 })
    const e = ctlError(thrown(() => readRailwayToken(text, 1000)), 1)
    expect(e.hint).toContain('railway whoami')
    expect(e.message).not.toContain('secret-tok')
    expect(e.hint).not.toContain('secret-tok')
    expect(readRailwayToken(text.replace('999', '2000'), 1000)).toBe('secret-tok')
  })

  it('a missing, invalid or token-less config asks for railway login', () => {
    const inputs = [undefined, 'not json', '{"user":{"token":null}}']
    expect(inputs).toHaveLength(3)
    for (const input of inputs) {
      const e = ctlError(thrown(() => readRailwayToken(input, 1000)), 1)
      expect(e.hint, String(input)).toContain('railway login')
    }
  })
})

describe('isDark', () => {
  it('other 404s and a 502 are not dark', () => {
    expect(isDark(404, APP_NOT_FOUND)).toBe(true)
    expect(isDark(404, '{"message":"page not found"}')).toBe(false)
    expect(isDark(502, '{"message":"Application not found"}')).toBe(false)
    expect(isDark(404, 'not json')).toBe(false)
  })
})

describe('resolveEnv', () => {
  it('resolveEnv returns the five URLs Railway names', async () => {
    const fake = fakeRailway({
      envs: [
        { id: 'env-348', name: 'pr-348', isEphemeral: true },
        { id: 'env-prod', name: 'production', isEphemeral: false },
      ],
      domains: { app: { custom: [], service: [{ domain: 'odd-host.example.net' }] } },
    })
    const r = await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    expect(r.env).toBe('pr-348')
    expect(r.environmentId).toBe('env-348')
    expect(r.urls.APP_URL).toBe('https://odd-host.example.net')
    expect(Object.keys(r.urls).sort()).toEqual([...URL_KEYS].sort())
    expect(r.urls.GATEWAY_URL).toBe('https://gateway.up.railway.app')
    expect(r.dark).toEqual([])
    const gql = fake.graphql()
    expect(gql.length).toBeGreaterThanOrEqual(6)
    for (const c of gql) {
      expect(c.url).toBe(GRAPHQL)
      expect(c.method).toBe('POST')
      expect(c.auth).toBe('Bearer tok')
    }
  })

  it('the custom domain wins over the generated one', async () => {
    fakeRailway({ domains: { gateway: { custom: [{ domain: 'api.example.com' }], service: [{ domain: 'gw.up.railway.app' }] } } })
    const r = await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    expect(r.urls.GATEWAY_URL).toBe('https://api.example.com')
    expect(r.urls.APP_URL).toBe('https://app.up.railway.app')
  })

  it('a service with no domain fails naming it and builds nothing', async () => {
    fakeRailway({ domains: { landing: { custom: [], service: [] } } })
    const e = await rejection(resolveEnv('pr-348', { token: 'tok', ids: IDS }))
    expect(e.message).toContain('landing')
    expect(e.message).not.toContain('up.railway.app')
  })

  it('a null customDomains is refused, not read as empty', async () => {
    fakeRailway({ domains: { app: { custom: null, service: [{ domain: 'app.up.railway.app' }] } } })
    const e = await rejection(resolveEnv('pr-348', { token: 'tok', ids: IDS }))
    expect(e.message).toContain('app')
    expect(e.message).toContain('customDomains')
  })

  it('a truncated environment list is not "no environment"', async () => {
    fakeRailway({ envs: [{ name: 'production', isEphemeral: false }], hasNextPage: true })
    const e = ctlError(await rejection(resolveEnv('pr-348', { token: 'tok', ids: IDS })), 1)
    expect(e.message).toContain('truncated')
    expect(e.message).not.toContain('no Railway environment named')
  })

  it('an empty environment list is a broken query', async () => {
    fakeRailway({ envs: [] })
    const e = ctlError(await rejection(resolveEnv('pr-348', { token: 'tok', ids: IDS })), 1)
    expect(e.message).toContain('token')
  })

  it('an Application-not-found 404 is dark, by name', async () => {
    fakeRailway({ probes: { landing: { status: 404, body: APP_NOT_FOUND } } })
    const r = await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    expect(Object.keys(r.urls)).toHaveLength(5)
    expect(r.dark).toEqual(['landing'])
  })

  it('probes use /healthz for the gateway and /health for the SPAs', async () => {
    const fake = fakeRailway()
    await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    const probes = fake.probes()
    expect(probes).toHaveLength(5)
    for (const c of probes) expect(c.method).toBe('GET')
    expect(probes.map((c) => c.url).sort()).toEqual(
      [
        'https://gateway.up.railway.app/healthz',
        'https://app.up.railway.app/health',
        'https://landing.up.railway.app/health',
        'https://ops-console.up.railway.app/health',
        'https://support-console.up.railway.app/health',
      ].sort(),
    )
  })

  it('no environment named pr-999', async () => {
    fakeRailway({ envs: [{ name: 'pr-348', isEphemeral: true }, { name: 'production', isEphemeral: false }] })
    const e = ctlError(await rejection(resolveEnv('pr-999', { token: 'tok', ids: IDS })), 1)
    expect(e.message).toContain('no Railway environment named pr-999')
    expect(e.hint).toMatch(/deploy gate/i)
  })

  it('two environments with one name are refused', async () => {
    fakeRailway({ envs: [{ name: 'pr-348', isEphemeral: true }, { name: 'pr-348', isEphemeral: true }] })
    const e = ctlError(await rejection(resolveEnv('pr-348', { token: 'tok', ids: IDS })), 1)
    expect(e.message).toContain('pr-348')
    expect(e.message).not.toContain('no Railway environment named')
  })

  it('a non-ephemeral pr-N and an ephemeral production are refused', async () => {
    fakeRailway({ envs: [{ name: 'pr-348', isEphemeral: false }] })
    await rejection(resolveEnv('pr-348', { token: 'tok', ids: IDS }))
    fakeRailway({ envs: [{ name: 'production', isEphemeral: true }] })
    await rejection(resolveEnv('production', { token: 'tok', ids: IDS }))
    fakeRailway({ envs: [{ id: 'env-prod', name: 'production', isEphemeral: false }] })
    expect((await resolveEnv('production', { token: 'tok', ids: IDS })).environmentId).toBe('env-prod')
  })

  it('Not Authorized from GraphQL gives the refresh hint', async () => {
    fakeRailway({ envReply: { errors: [{ message: 'Not Authorized' }] } })
    const e = ctlError(await rejection(resolveEnv('pr-348', { token: 'secret-tok', ids: IDS })), 1)
    expect(e.hint).toContain('railway whoami')
    expect(e.message).not.toContain('secret-tok')
    expect(e.hint).not.toContain('secret-tok')
  })
})

describe('envCommand', () => {
  let home: string
  const scratch = fileURLToPath(new URL('../../.ralph/scratch/', import.meta.url))
  const realIds = () =>
    readRailwayIds(readFileSync(fileURLToPath(new URL('../../.github/workflows/dev-env.yml', import.meta.url)), 'utf8'))

  beforeEach(() => {
    mkdirSync(scratch, { recursive: true })
    home = mkdtempSync(join(scratch, 'ctl-env-'))
    mkdirSync(join(home, '.railway'))
    writeFileSync(
      join(home, '.railway', 'config.json'),
      JSON.stringify({ user: { token: null, accessToken: 'tok', refreshToken: 'r', tokenExpiresAt: 4102444800 } }),
    )
    vi.stubEnv('HOME', home)
  })
  afterEach(() => rmSync(home, { recursive: true, force: true }))

  it('env prints env, environmentId, urls and dark', async () => {
    const ids = realIds()
    expect(Object.values(ids.services)).toHaveLength(5)
    const fake = fakeRailway({ ids, envs: [{ id: 'env-348', name: 'pr-348', isEphemeral: true }] })
    const r = (await envCommand(['pr-348'], {})) as { env: string; environmentId: string; urls: Record<string, string>; dark: string[] }
    expect(r.env).toBe('pr-348')
    expect(r.environmentId).toBe('env-348')
    expect(Object.keys(r.urls).sort()).toEqual([...URL_KEYS].sort())
    expect(r.dark).toEqual([])
    expect(fake.graphql().length).toBeGreaterThanOrEqual(6)
    for (const c of fake.graphql()) expect(c.auth).toBe('Bearer tok')
  })

  it('env exits 1 when a domain is dark', async () => {
    fakeRailway({ ids: realIds(), probes: { landing: { status: 404, body: APP_NOT_FOUND } } })
    const e = ctlError(await rejection(envCommand(['pr-348'], {})), 1)
    expect(e.hint.length).toBeGreaterThan(0)
    expect(e.extra?.dark).toEqual(['landing'])
    expect((e.extra?.urls as Record<string, string>).LANDING_URL).toBe('https://landing.up.railway.app')
  })

  it('a malformed name is a usage error with no network call', async () => {
    const fake = fakeRailway({ ids: realIds() })
    for (const name of ['staging', 'pr-abc']) ctlError(await rejection(envCommand([name], {})), 2)
    expect(fake.fn).toHaveBeenCalledTimes(0)
    await envCommand(['pr-348'], {})
    expect(fake.fn).toHaveBeenCalled()
  })

  it('ctl env is wired into run and validates before any network call', async () => {
    const fake = fakeRailway({ ids: realIds() })
    const r = await run(['env', 'staging'])
    expect(r.code).toBe(2)
    expect(JSON.parse(r.stderr).error).toContain('staging')
    expect(fake.fn).toHaveBeenCalledTimes(0)
  })
})
