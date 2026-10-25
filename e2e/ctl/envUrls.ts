import { CtlError } from './main'

export type ServiceLabel = 'gateway' | 'app' | 'landing' | 'ops-console' | 'support-console' | 'library' | 'blog'
export type UrlVar = 'GATEWAY_URL' | 'APP_URL' | 'LANDING_URL' | 'OPS_CONSOLE_URL' | 'SUPPORT_CONSOLE_URL' | 'LIBRARY_URL' | 'BLOG_URL'

export const URL_VAR: Record<ServiceLabel, UrlVar> = {
  gateway: 'GATEWAY_URL',
  app: 'APP_URL',
  landing: 'LANDING_URL',
  'ops-console': 'OPS_CONSOLE_URL',
  'support-console': 'SUPPORT_CONSOLE_URL',
  library: 'LIBRARY_URL',
  blog: 'BLOG_URL',
}

export interface EnvResult {
  env: string
  environmentId: string
  urls: Partial<Record<UrlVar, string>>
  dark: ServiceLabel[]
}

const PRODUCTION_HOST: Record<ServiceLabel, string> = {
  gateway: 'api',
  app: 'app',
  landing: 'www',
  'ops-console': 'ops',
  'support-console': 'sup',
  library: 'www',
  blog: 'www',
}

// The library and the blog are served under a base path.
const SITE_BASE: Partial<Record<ServiceLabel, string>> = { library: '/library', blog: '/blog' }

// The preview project's number; scripts/ci/preview.sh builds the same run.app URLs.
const PREVIEW_PROJECT_NUMBER = '944095690109'

// resolveEnv names the seven URLs: production's hosts, or a PR preview's run.app services.
// A preview's database is recreated on every deploy, so environmentId carries the env name only.
export function resolveEnv(name: string): EnvResult {
  const urls: EnvResult['urls'] = {}
  const pr = /^pr-([0-9]+)$/.exec(name)?.[1]
  for (const [label, host] of Object.entries(PRODUCTION_HOST) as [ServiceLabel, string][]) {
    const base = SITE_BASE[label] ?? ''
    urls[URL_VAR[label]] = pr
      ? `https://${label}-pr-${pr}-${PREVIEW_PROJECT_NUMBER}.africa-south1.run.app${base}`
      : `https://${host}.ascomply.com${base}`
  }
  return { env: name, environmentId: name, urls, dark: [] }
}

export async function resolveEnvByName(name: string): Promise<EnvResult> {
  return resolveEnv(name)
}

export async function envCommand(positionals: string[], _flags: Record<string, string | undefined>): Promise<unknown> {
  const name = positionals[0]
  if (positionals.length !== 1 || !/^(pr-[0-9]+|production)$/.test(name)) {
    throw new CtlError(`invalid environment name: ${positionals.join(' ') || '(none)'}`, 'Use pr-<N> or production, for example "ctl env pr-348".', 2)
  }
  return resolveEnv(name)
}
