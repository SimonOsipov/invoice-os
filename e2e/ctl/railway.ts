import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import path from 'node:path'
import { CtlError } from './main'

export type ServiceLabel = 'gateway' | 'app' | 'landing' | 'ops-console' | 'support-console'
export type UrlVar = 'GATEWAY_URL' | 'APP_URL' | 'LANDING_URL' | 'OPS_CONSOLE_URL' | 'SUPPORT_CONSOLE_URL'

export const URL_VAR: Record<ServiceLabel, UrlVar> = {
  gateway: 'GATEWAY_URL',
  app: 'APP_URL',
  landing: 'LANDING_URL',
  'ops-console': 'OPS_CONSOLE_URL',
  'support-console': 'SUPPORT_CONSOLE_URL',
}

export const RAILWAY_ID_KEYS = [
  'RAILWAY_PROJECT_ID',
  'RAILWAY_SVC_GATEWAY_ID',
  'RAILWAY_SVC_APP_ID',
  'RAILWAY_SVC_LANDING_ID',
  'RAILWAY_SVC_OPS_CONSOLE_ID',
  'RAILWAY_SVC_SUPPORT_CONSOLE_ID',
] as const

export interface RailwayIds {
  projectId: string
  services: Record<ServiceLabel, string>
}

export interface EnvResult {
  env: string
  environmentId: string
  urls: Partial<Record<UrlVar, string>>
  dark: ServiceLabel[]
}

const GRAPHQL_URL = 'https://backboard.railway.com/graphql/v2'
const LOGIN_HINT = 'Run "railway login", then "railway whoami" to confirm.'
const REFRESH_HINT = 'The Railway token is expired or not authorised. Run "railway whoami" to refresh it, or "railway login".'
const LABELS = Object.keys(URL_VAR) as ServiceLabel[]
const ID_KEY: Record<ServiceLabel, string> = {
  gateway: 'RAILWAY_SVC_GATEWAY_ID',
  app: 'RAILWAY_SVC_APP_ID',
  landing: 'RAILWAY_SVC_LANDING_ID',
  'ops-console': 'RAILWAY_SVC_OPS_CONSOLE_ID',
  'support-console': 'RAILWAY_SVC_SUPPORT_CONSOLE_ID',
}

const ENV_LIST_QUERY =
  'query($p:String!){environments(projectId:$p){edges{node{id name isEphemeral}} pageInfo{hasNextPage}}}'
// Same selection set as fetch_domain in dev-env.yml.
const DOMAINS_QUERY =
  'query($p:String!,$e:String!,$s:String!){domains(projectId:$p,environmentId:$e,serviceId:$s){customDomains{domain targetPort} serviceDomains{domain targetPort}}}'

export function readRailwayIds(workflowYaml: string): RailwayIds {
  const read = (key: string) => {
    const m = new RegExp(`^\\s+${key}: ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\\s*$`, 'm').exec(workflowYaml)
    if (!m) throw new CtlError(`${key} is missing from .github/workflows/dev-env.yml`, 'Restore the key in the workflow env block.', 1)
    return m[1]
  }
  const services = {} as Record<ServiceLabel, string>
  for (const l of LABELS) services[l] = read(ID_KEY[l])
  return { projectId: read('RAILWAY_PROJECT_ID'), services }
}

export function readRailwayToken(configText: string | undefined, nowSeconds: number): string {
  let user: { accessToken?: unknown; tokenExpiresAt?: unknown } | undefined
  try {
    user = configText === undefined ? undefined : JSON.parse(configText)?.user
  } catch {
    user = undefined
  }
  const token = user?.accessToken
  if (typeof token !== 'string' || token === '') {
    throw new CtlError('no usable Railway token in ~/.railway/config.json', LOGIN_HINT, 1)
  }
  const exp = user?.tokenExpiresAt
  if (typeof exp === 'number' && exp <= nowSeconds) {
    throw new CtlError('the Railway token in ~/.railway/config.json is expired', REFRESH_HINT, 1)
  }
  return token
}

// Mirrors domain_is_dark in scripts/ci/railway-env.sh.
export function isDark(status: number, body: string): boolean {
  if (status !== 404) return false
  try {
    return JSON.parse(body)?.message === 'Application not found'
  } catch {
    return false
  }
}

async function gql(token: string, query: string, variables: Record<string, string>, what: string): Promise<any> {
  const res = await fetch(GRAPHQL_URL, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ query, variables }),
  })
  let json: any
  try {
    json = await res.json()
  } catch {
    throw new CtlError(`Railway answered HTTP ${res.status} with no JSON while ${what}`, REFRESH_HINT, 1)
  }
  const errors: { message?: string }[] = Array.isArray(json?.errors) ? json.errors : []
  if (errors.some((e) => /not authorized/i.test(e.message ?? ''))) {
    throw new CtlError(`Railway refused the token while ${what}`, REFRESH_HINT, 1)
  }
  if (errors.length > 0 || !res.ok) {
    throw new CtlError(`Railway failed while ${what}: ${errors.map((e) => e.message).join('; ') || `HTTP ${res.status}`}`, REFRESH_HINT, 1)
  }
  return json.data
}

async function findEnvironment(name: string, token: string, projectId: string) {
  const data = await gql(token, ENV_LIST_QUERY, { p: projectId }, 'listing environments')
  const edges: { node: { id: string; name: string; isEphemeral: boolean } }[] = data?.environments?.edges ?? []
  if (data?.environments?.pageInfo?.hasNextPage) {
    throw new CtlError('the Railway environment list is truncated', 'Railway returned more environments than one page; ask for the id by hand.', 1)
  }
  if (edges.length === 0) {
    throw new CtlError('Railway returned zero environments: the query is broken or the token lost project access', 'The project always has one. Run "railway whoami".', 1)
  }
  const matches = edges.map((e) => e.node).filter((n) => n.name === name)
  if (matches.length === 0) {
    throw new CtlError(`no Railway environment named ${name}`, 'A pr-N environment exists only after the deploy gate has run for that PR.', 1)
  }
  if (matches.length > 1) {
    throw new CtlError(`${matches.length} Railway environments are named ${name}`, 'Names must be unique; clean up the duplicate in Railway.', 1)
  }
  const [node] = matches
  if (node.isEphemeral !== (name !== 'production')) {
    throw new CtlError(`environment ${name} has the wrong kind (isEphemeral=${node.isEphemeral})`, 'pr-N must be ephemeral and production persistent; refusing to use it.', 1)
  }
  return node.id
}

async function serviceDomain(label: ServiceLabel, token: string, projectId: string, envId: string, serviceId: string) {
  const data = await gql(token, DOMAINS_QUERY, { p: projectId, e: envId, s: serviceId }, `discovering the ${label} domain`)
  // Null customDomains is a drifted query or a failed read, not an empty list (D34).
  if (!Array.isArray(data?.domains?.customDomains)) {
    throw new CtlError(`the ${label} domains reply carries no customDomains list`, 'Refusing to pick the generated domain over a possible custom one.', 1)
  }
  const all: { domain: string }[] = [...data.domains.customDomains, ...(data.domains.serviceDomains ?? [])]
  const domain = all[0]?.domain
  if (!domain) {
    throw new CtlError(`no domain found for ${label} in environment ${envId}`, 'Every public service needs a custom or Railway-generated domain.', 1)
  }
  return `https://${domain}`
}

async function probe(url: string): Promise<boolean> {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(20_000) })
    return isDark(res.status, await res.text())
  } catch {
    return false
  }
}

export async function resolveEnv(name: string, deps: { token: string; ids: RailwayIds }): Promise<EnvResult> {
  const { token, ids } = deps
  const environmentId = await findEnvironment(name, token, ids.projectId)
  const hosts = await Promise.all(
    LABELS.map((l) => serviceDomain(l, token, ids.projectId, environmentId, ids.services[l])),
  )
  const urls: EnvResult['urls'] = {}
  LABELS.forEach((l, i) => (urls[URL_VAR[l]] = hosts[i]))
  const verdicts = await Promise.all(LABELS.map((l, i) => probe(hosts[i] + (l === 'gateway' ? '/healthz' : '/health'))))
  return { env: name, environmentId, urls, dark: LABELS.filter((_, i) => verdicts[i]) }
}

export async function envCommand(positionals: string[], _flags: Record<string, string | undefined>): Promise<unknown> {
  const name = positionals[0]
  if (positionals.length !== 1 || !/^(pr-[0-9]+|production)$/.test(name)) {
    throw new CtlError(`invalid environment name: ${positionals.join(' ') || '(none)'}`, 'Use pr-<N> or production, for example "ctl env pr-348".', 2)
  }
  let configText: string | undefined
  try {
    configText = readFileSync(path.join(homedir(), '.railway', 'config.json'), 'utf8')
  } catch {
    configText = undefined
  }
  const token = readRailwayToken(configText, Date.now() / 1000)
  const root = path.resolve(import.meta.dirname, '../..')
  const ids = readRailwayIds(readFileSync(path.join(root, '.github/workflows/dev-env.yml'), 'utf8'))
  const result = await resolveEnv(name, { token, ids })
  if (result.dark.length > 0) {
    throw new CtlError(
      `dark domain: ${result.dark.join(', ')}`,
      'Railway answers "Application not found" for it. Delete and recreate the domain, or rerun the deploy gate.',
      1,
      { urls: result.urls, dark: result.dark },
    )
  }
  return result
}
