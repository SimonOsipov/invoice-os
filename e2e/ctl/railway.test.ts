import { spawnSync } from 'node:child_process'
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
  RAILWAY_ID_KEYS,
  URL_VAR,
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
  // Runs first; a returned Response replaces the fake's own answer.
  override?: (url: string, init?: RequestInit) => Response | Promise<Response> | undefined
}
interface Call {
  url: string
  method: string
  auth: string | null
  init?: RequestInit
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
    calls.push({ url, method: init?.method ?? 'GET', auth: headers.get('authorization'), init })
    const forced = await opts.override?.(url, init)
    if (forced) return forced
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
    const stale = `  # RAILWAY_SVC_APP_ID: ${uuid(9)}`
    const withComment = readRailwayIds(yamlOf([stale, ...YAML_KEYS]))
    expect(withComment.services.app).toBe(uuid(3))
    expect(JSON.stringify(withComment)).not.toContain(uuid(9))
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
    const commentedOnly = [`  # RAILWAY_SVC_APP_ID: ${uuid(3)}`, ...without]
    expect(thrown(() => readRailwayIds(yamlOf(commentedOnly))).message).toContain('RAILWAY_SVC_APP_ID')
  })

  it('every one of the six keys is required and named when absent', () => {
    expect(RAILWAY_ID_KEYS).toHaveLength(6)
    for (const key of RAILWAY_ID_KEYS) {
      const without = YAML_KEYS.filter((l) => !l.includes(`${key}:`))
      expect(without, key).toHaveLength(YAML_KEYS.length - 1)
      expect(thrown(() => readRailwayIds(yamlOf(without))).message, key).toContain(key)
    }
  })

  it('a malformed uuid value for a listed key is refused, not read', () => {
    const bad = YAML_KEYS.map((l) => (l.includes('RAILWAY_SVC_LANDING_ID') ? '  RAILWAY_SVC_LANDING_ID: not-a-uuid' : l))
    expect(thrown(() => readRailwayIds(yamlOf(bad))).message).toContain('RAILWAY_SVC_LANDING_ID')
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
    const inputs = [
      undefined,
      'not json',
      '{"user":{"token":null}}',
      '',
      'null',
      '5',
      '{"user":null}',
      '{"user":{"accessToken":""}}',
      '{"user":{"accessToken":12345}}',
      '{"user":{"token":"legacy-only"}}',
    ]
    expect(inputs).toHaveLength(10)
    for (const input of inputs) {
      const e = ctlError(thrown(() => readRailwayToken(input, 1000)), 1)
      expect(e.hint, String(input)).toContain('railway login')
    }
  })
})

describe('readRailwayToken expiry', () => {
  const config = (user: object) => JSON.stringify({ user })

  it('a config without tokenExpiresAt is used as is', () => {
    expect(readRailwayToken(config({ accessToken: 'tok' }), 1000)).toBe('tok')
  })

  it('the expiry is compared in epoch seconds against now', () => {
    const expired = ctlError(thrown(() => readRailwayToken(config({ accessToken: 'tok', tokenExpiresAt: 1 }), 1_800_000_000)), 1)
    expect(expired.message).toMatch(/expired/)
    expect(expired.hint).toMatch(/refresh/i)
    expect(readRailwayToken(config({ accessToken: 'tok', tokenExpiresAt: 1_800_000_001 }), 1_800_000_000)).toBe('tok')
  })
})

describe('isDark', () => {
  it('other 404s and a 502 are not dark', () => {
    expect(isDark(404, APP_NOT_FOUND)).toBe(true)
    expect(isDark(404, '{"message":"page not found"}')).toBe(false)
    expect(isDark(502, '{"message":"Application not found"}')).toBe(false)
    expect(isDark(404, 'not json')).toBe(false)
    const bodies = ['', 'null', '[]', '"Application not found"', '{"message":null}', '{"message":"application not found"}', '{"error":{"message":"Application not found"}}', '[{"message":"Application not found"}]']
    for (const b of bodies) expect(isDark(404, b), b).toBe(false)
    for (const status of [0, 200, 403, 500, 503]) expect(isDark(status, APP_NOT_FOUND), String(status)).toBe(false)
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

describe('resolveEnv requests', () => {
  const bodyOf = (c: Call) => JSON.parse(String(c.init?.body)) as { query: string; variables: Record<string, string> }

  it('the environment list asks for pageInfo and every domains call carries the project, environment and service ids', async () => {
    const fake = fakeRailway({ envs: [{ id: 'env-348', name: 'pr-348', isEphemeral: true }] })
    await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    const bodies = fake.graphql().map(bodyOf)
    const list = bodies.filter((b) => b.query.includes('environments('))
    expect(list).toHaveLength(1)
    expect(list[0].query).toContain('hasNextPage')
    expect(list[0].query).toContain('isEphemeral')
    expect(list[0].variables).toEqual({ p: IDS.projectId })
    const domains = bodies.filter((b) => b.query.includes('domains('))
    expect(domains).toHaveLength(5)
    for (const d of domains) {
      expect(d.query).toContain('customDomains')
      expect(d.query).toContain('serviceDomains')
      expect(d.variables.p).toBe(IDS.projectId)
      expect(d.variables.e).toBe('env-348')
    }
    expect(domains.map((d) => d.variables.s).sort()).toEqual(LABELS.map((l) => IDS.services[l]).sort())
    for (const c of fake.graphql()) expect(new Headers(c.init?.headers).get('content-type')).toBe('application/json')
  })

  it('each service id lands on its own label', async () => {
    fakeRailway({
      override: (_url, init) => {
        const body = String(init?.body ?? '')
        if (!body.includes('domains(')) return undefined
        const label = LABELS.find((l) => body.includes(IDS.services[l]))
        return new Response(JSON.stringify({ data: { domains: { customDomains: [], serviceDomains: [{ domain: `only-${label}.example.net` }] } } }), { status: 200 })
      },
    })
    const r = await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    for (const l of LABELS) expect(r.urls[URL_VAR[l]]).toBe(`https://only-${l}.example.net`)
  })

  it('the token goes to Railway only, never to a probed host', async () => {
    const fake = fakeRailway()
    await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    expect(fake.probes()).toHaveLength(5)
    for (const c of fake.probes()) {
      expect(c.auth, c.url).toBeNull()
      expect(new Headers(c.init?.headers).has('cookie')).toBe(false)
    }
    expect(fake.graphql().length).toBeGreaterThan(0)
    for (const c of fake.graphql()) expect(c.auth).toBe('Bearer tok')
  })

  it('a probe passes a 20 second timeout signal', async () => {
    const spy = vi.spyOn(AbortSignal, 'timeout')
    const fake = fakeRailway()
    await resolveEnv('pr-348', { token: 'tok', ids: IDS })
    expect(spy).toHaveBeenCalledWith(20_000)
    expect(spy).toHaveBeenCalledTimes(5)
    for (const c of fake.probes()) expect(c.init?.signal).toBeInstanceOf(AbortSignal)
    spy.mockRestore()
  })
})

describe('resolveEnv failures', () => {
  const resolve = () => resolveEnv('pr-348', { token: 'secret-tok', ids: IDS })

  it('a custom domain is first even when several of each kind exist', async () => {
    fakeRailway({
      domains: {
        'ops-console': { custom: [{ domain: 'ops.example.com' }, { domain: 'ops2.example.com' }], service: [{ domain: 'ops.up.railway.app' }] },
        'support-console': { custom: [], service: [{ domain: 'first.up.railway.app' }, { domain: 'second.up.railway.app' }] },
      },
    })
    const r = await resolve()
    expect(r.urls.OPS_CONSOLE_URL).toBe('https://ops.example.com')
    expect(r.urls.SUPPORT_CONSOLE_URL).toBe('https://first.up.railway.app')
  })

  it('a domains reply with no domains object, an absent customDomains or an empty domain is refused naming the service', async () => {
    const replies: [string, unknown][] = [
      ['null domains', { data: { domains: null } }],
      ['null data', { data: null }],
      ['absent customDomains', { data: { domains: { serviceDomains: [{ domain: 'app.up.railway.app' }] } } }],
      ['empty first domain', { data: { domains: { customDomains: [{ domain: '' }], serviceDomains: [{ domain: 'app.up.railway.app' }] } } }],
    ]
    expect(replies).toHaveLength(4)
    for (const [what, reply] of replies) {
      fakeRailway({
        override: (_url, init) => {
          const body = String(init?.body ?? '')
          return body.includes('domains(') && body.includes(IDS.services.app) ? new Response(JSON.stringify(reply), { status: 200 }) : undefined
        },
      })
      const e = await rejection(resolve())
      expect(e.message, what).toContain('app')
      expect(e.message, what).not.toContain('secret-tok')
    }
  })

  it('a domain failure names no host built from a pattern and returns no urls', async () => {
    fakeRailway({ domains: { 'ops-console': { custom: [], service: [] } } })
    const e = await rejection(resolve())
    expect(e.message).toContain('ops-console')
    expect(e.message).not.toMatch(/https?:\/\//)
  })

  it('an environment node that is not the right kind or lacks isEphemeral is refused', async () => {
    fakeRailway({ envReply: { data: { environments: { edges: [{ node: { id: 'e1', name: 'pr-348' } }], pageInfo: { hasNextPage: false } } } } })
    await rejection(resolve())
    fakeRailway({ envs: [{ name: 'production', isEphemeral: false }, { name: 'pr-348', isEphemeral: true }] })
    expect((await resolve()).env).toBe('pr-348')
  })

  it('a truncated list is refused even when the name is on the page', async () => {
    fakeRailway({ envs: [{ name: 'pr-348', isEphemeral: true }], hasNextPage: true })
    const e = ctlError(await rejection(resolve()), 1)
    expect(e.message).toContain('truncated')
  })

  it('a reply with no environments object is a broken query, not no environment', async () => {
    for (const reply of [{ data: { environments: null } }, { data: null }, {}]) {
      fakeRailway({ envReply: reply })
      const e = ctlError(await rejection(resolve()), 1)
      expect(e.message, JSON.stringify(reply)).not.toContain('no Railway environment named')
      expect(e.message, JSON.stringify(reply)).toMatch(/zero environments|query/i)
    }
  })

  it('production resolves against a persistent environment and a pr-N name never picks it', async () => {
    fakeRailway({ envs: [{ id: 'env-prod', name: 'production', isEphemeral: false }, { id: 'env-348', name: 'pr-348', isEphemeral: true }] })
    expect((await resolveEnv('production', { token: 'tok', ids: IDS })).environmentId).toBe('env-prod')
    expect((await resolveEnv('pr-348', { token: 'tok', ids: IDS })).environmentId).toBe('env-348')
    const e = ctlError(await rejection(resolveEnv('pr-34', { token: 'tok', ids: IDS })), 1)
    expect(e.message).toContain('no Railway environment named pr-34')
  })

  it('a 401, or a reply with no JSON, gives the refresh hint and never echoes the token', async () => {
    const answers: [number, string][] = [
      [401, '{"errors":[{"message":"Not Authorized"}]}'],
      [401, '{"message":"Unauthorized"}'],
      [401, 'Unauthorized'],
      [403, ''],
    ]
    expect(answers).toHaveLength(4)
    for (const [status, body] of answers) {
      fakeRailway({ override: (url) => (url.startsWith('https://backboard.railway.com/') ? new Response(body, { status }) : undefined) })
      const e = ctlError(await rejection(resolve()), 1)
      expect(e.hint, `${status} ${body}`).toContain('railway whoami')
      expect(e.hint, `${status} ${body}`).toMatch(/refresh/i)
      expect(`${e.message} ${e.hint}`).not.toContain('secret-tok')
    }
  })

  it('Not Authorized on a domains call gives the refresh hint too', async () => {
    fakeRailway({
      override: (_url, init) =>
        String(init?.body ?? '').includes('domains(') ? new Response('{"errors":[{"message":"Not Authorized"}]}', { status: 200 }) : undefined,
    })
    const e = ctlError(await rejection(resolve()), 1)
    expect(e.hint).toContain('railway whoami')
    expect(e.hint).toMatch(/refresh/i)
    expect(e.message).not.toContain('secret-tok')
  })

  it('a GraphQL error beside data fails, and so does a non-200 that carries data', async () => {
    const good = { data: { environments: { edges: [{ node: { id: 'e', name: 'pr-348', isEphemeral: true } }], pageInfo: { hasNextPage: false } } } }
    fakeRailway({ envReply: { ...good, errors: [{ message: 'partial failure' }] } })
    expect((await rejection(resolve())).message).toContain('partial failure')
    fakeRailway({ override: (url) => (url.startsWith('https://backboard.railway.com/') ? new Response(JSON.stringify(good), { status: 500 }) : undefined) })
    expect((await rejection(resolve())).message).toContain('500')
  })
})

describe('resolveEnv probes', () => {
  const resolve = () => resolveEnv('pr-348', { token: 'tok', ids: IDS })

  it('a probe network failure, a timeout or a broken body is not dark', async () => {
    const failures: Record<string, () => Response> = {
      'network error': () => {
        throw new TypeError('fetch failed')
      },
      timeout: () => {
        throw new DOMException('The operation timed out', 'TimeoutError')
      },
      'broken body': () =>
        new Response(
          new ReadableStream({
            start(c) {
              c.error(new Error('stream reset'))
            },
          }),
          { status: 404 },
        ),
    }
    for (const [what, make] of Object.entries(failures)) {
      const fake = fakeRailway({ override: (url) => (url.startsWith('https://backboard.railway.com/') ? undefined : make()) })
      const r = await resolve()
      expect(fake.probes(), what).toHaveLength(5)
      expect(Object.keys(r.urls), what).toHaveLength(5)
      expect(r.dark, what).toEqual([])
    }
  })

  it('a probe that answers 5xx or 404 without the marker is not dark', async () => {
    fakeRailway({
      probes: {
        gateway: { status: 502, body: '{"message":"Application not found"}' },
        app: { status: 404, body: '<html>Not Found</html>' },
        landing: { status: 503, body: '' },
      },
    })
    expect((await resolve()).dark).toEqual([])
  })

  it('every dark service is listed, in service order, and a dark gateway counts', async () => {
    fakeRailway({
      probes: {
        'support-console': { status: 404, body: APP_NOT_FOUND },
        gateway: { status: 404, body: APP_NOT_FOUND },
        app: { status: 404, body: APP_NOT_FOUND },
      },
    })
    const r = await resolve()
    expect(r.dark).toEqual(['gateway', 'app', 'support-console'])
    expect(Object.keys(r.urls)).toHaveLength(5)
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

  const SECRET = 'SECRET-tok-9f3a'
  const writeConfig = (user: object) => writeFileSync(join(home, '.railway', 'config.json'), JSON.stringify({ user }))
  const failJson = (e: CtlError) => JSON.stringify({ message: e.message, hint: e.hint, extra: e.extra })

  it('a malformed name is a usage error before the config or the workflow file is read', async () => {
    rmSync(join(home, '.railway'), { recursive: true })
    const fake = fakeRailway({ ids: realIds() })
    const bad = ['staging', 'pr-abc', 'pr-', 'pr--1', 'pr-1x', 'PR-1', 'Production', ' production', 'production\n', 'pr-1\n', 'pr-１２', '', 'development']
    for (const name of bad) ctlError(await rejection(envCommand([name], {})), 2)
    for (const args of [[], ['pr-1', 'pr-2'], ['production', 'production']]) ctlError(await rejection(envCommand(args, {})), 2)
    expect(fake.fn).toHaveBeenCalledTimes(0)
    expect(ctlError(await rejection(envCommand(['production'], {})), 1).hint).toContain('railway login')
  })

  it('a missing config, an expired token or a token-less config fails before any network call', async () => {
    const fake = fakeRailway({ ids: realIds() })
    rmSync(join(home, '.railway', 'config.json'))
    expect(ctlError(await rejection(envCommand(['pr-348'], {})), 1).hint).toContain('railway login')
    writeConfig({ accessToken: SECRET, tokenExpiresAt: 1 })
    const expired = ctlError(await rejection(envCommand(['pr-348'], {})), 1)
    expect(expired.hint).toContain('railway whoami')
    expect(failJson(expired)).not.toContain(SECRET)
    writeConfig({ token: null })
    expect(ctlError(await rejection(envCommand(['pr-348'], {})), 1).hint).toContain('railway login')
    expect(fake.fn).toHaveBeenCalledTimes(0)
  })

  it('production resolves through envCommand', async () => {
    fakeRailway({ ids: realIds(), envs: [{ id: 'env-prod', name: 'production', isEphemeral: false }] })
    const r = (await envCommand(['production'], {})) as { env: string; environmentId: string }
    expect(r.env).toBe('production')
    expect(r.environmentId).toBe('env-prod')
  })

  it('no output of the command, success or failure, contains the token', async () => {
    writeConfig({ accessToken: SECRET, tokenExpiresAt: 4102444800 })
    const scenarios: Record<string, FakeOpts> = {
      ok: {},
      dark: { probes: { landing: { status: 404, body: APP_NOT_FOUND } } },
      'not authorized': { envReply: { errors: [{ message: 'Not Authorized' }] } },
      'no domain': { domains: { landing: { custom: [], service: [] } } },
      'null customDomains': { domains: { app: { custom: null, service: [{ domain: 'app.up.railway.app' }] } } },
      'no environment': { envs: [{ name: 'production', isEphemeral: false }] },
      http401: { override: (url) => (url.startsWith('https://backboard.railway.com/') ? new Response('Unauthorized', { status: 401 }) : undefined) },
    }
    expect(Object.keys(scenarios)).toHaveLength(7)
    for (const [what, opts] of Object.entries(scenarios)) {
      fakeRailway({ ids: realIds(), ...opts })
      const r = await run(['env', 'pr-348'])
      expect(r.stdout + r.stderr, what).not.toContain(SECRET)
      expect(JSON.parse(r.code === 0 ? r.stdout : r.stderr), what).toBeTypeOf('object')
    }
    fakeRailway({ ids: realIds() })
    expect((await run(['env', 'pr-348'])).code).toBe(0)
  })

  it('a no-domain or null-customDomains failure points at docs/add-a-service.md step 6', async () => {
    const cases: Record<string, FakeOpts> = {
      'no domain': { domains: { landing: { custom: [], service: [] } } },
      'null customDomains': { domains: { app: { custom: null, service: [{ domain: 'app.up.railway.app' }] } } },
    }
    expect(Object.keys(cases)).toHaveLength(2)
    for (const [what, opts] of Object.entries(cases)) {
      fakeRailway({ ids: realIds(), ...opts })
      const e = ctlError(await rejection(envCommand(['pr-348'], {})), 1)
      expect(e.hint, what).toContain('docs/add-a-service.md')
      expect(e.hint, what).toMatch(/step 6/)
    }
  })

  it('a dark domain error names the service and its host, and the hint says it is not an app bug', async () => {
    fakeRailway({ ids: realIds(), probes: { landing: { status: 404, body: APP_NOT_FOUND } } })
    const e = ctlError(await rejection(envCommand(['pr-348'], {})), 1)
    expect(e.message).toContain('landing is dark')
    expect(e.message).toContain('landing.up.railway.app')
    expect(e.hint).toMatch(/not an app bug/i)
    expect(e.hint).toContain('deploy gate')
  })

  it('a dark domain prints its error with urls and dark on stderr and exit code 1', async () => {
    fakeRailway({ ids: realIds(), probes: { landing: { status: 404, body: APP_NOT_FOUND }, app: { status: 404, body: APP_NOT_FOUND } } })
    const r = await run(['env', 'pr-348'])
    expect(r.code).toBe(1)
    expect(r.stdout).toBe('')
    const out = JSON.parse(r.stderr)
    expect(out.dark).toEqual(['app', 'landing'])
    expect(Object.keys(out.urls)).toHaveLength(5)
    expect(out.hint.length).toBeGreaterThan(0)
  })
})

describe('ctl env under node', () => {
  const E2E_DIR = fileURLToPath(new URL('..', import.meta.url))
  const env = { ...process.env }
  for (const k of URL_KEYS) delete env[k]
  const node = (args: string[], home: string) =>
    spawnSync(process.execPath, ['--import', './ctl/tsResolve.mjs', ...args], { cwd: E2E_DIR, env: { ...env, HOME: home }, encoding: 'utf8' })

  let home: string
  beforeEach(() => {
    const scratch = fileURLToPath(new URL('../../.ralph/scratch/', import.meta.url))
    mkdirSync(scratch, { recursive: true })
    home = mkdtempSync(join(scratch, 'ctl-env-node-'))
    mkdirSync(join(home, '.railway'))
  })
  afterEach(() => rmSync(home, { recursive: true, force: true }))

  it('the real entry exits 2 for a malformed name and 1 with the login hint when there is no config', () => {
    const bad = node(['./ctl/main.ts', 'env', 'staging'], home)
    expect(bad.status, bad.stderr).toBe(2)
    expect(bad.stdout).toBe('')
    expect(JSON.parse(bad.stderr).error).toContain('staging')
    const none = node(['./ctl/main.ts', 'env', 'pr-348'], home)
    expect(none.status, none.stderr).toBe(1)
    expect(JSON.parse(none.stderr).hint).toContain('railway login')
  })

  it('railway.ts loads first without a cycle fault, and env resolves under node with the real workflow ids', () => {
    writeFileSync(join(home, '.railway', 'config.json'), JSON.stringify({ user: { accessToken: 'tok', tokenExpiresAt: 4102444800 } }))
    const script = `
      const serviceIds = new Set()
      globalThis.fetch = async (url, init) => {
        const u = String(url)
        if (!u.startsWith('https://backboard.railway.com/')) return new Response('{}', { status: 200 })
        const body = JSON.parse(init.body)
        if (body.query.includes('environments(')) {
          return new Response(JSON.stringify({ data: { environments: { edges: [{ node: { id: 'env-348', name: 'pr-348', isEphemeral: true } }], pageInfo: { hasNextPage: false } } } }))
        }
        serviceIds.add(body.variables.s)
        return new Response(JSON.stringify({ data: { domains: { customDomains: [], serviceDomains: [{ domain: body.variables.s.slice(0, 8) + '.up.railway.app' }] } } }))
      }
      await import('./ctl/railway')
      const main = await import('./ctl/main')
      const r = await main.run(['env', 'pr-348'])
      console.log(JSON.stringify({ code: r.code, stdout: r.stdout, stderr: r.stderr, services: serviceIds.size }))`
    const r = node(['--input-type=module', '-e', script], home)
    expect(r.status, r.stderr).toBe(0)
    const out = JSON.parse(r.stdout.trim().split('\n').pop() as string)
    expect(out.stderr).toBe('')
    expect(out.code).toBe(0)
    expect(out.services).toBe(5)
    expect(Object.keys(JSON.parse(out.stdout).urls)).toHaveLength(5)
  })
})
