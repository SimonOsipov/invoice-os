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

// Red-phase stubs: wrong values, no throws, so tests fail on assertions.
export function readRailwayIds(_workflowYaml: string): RailwayIds {
  return {
    projectId: '',
    services: { gateway: '', app: '', landing: '', 'ops-console': '', 'support-console': '' },
  }
}

export function readRailwayToken(_configText: string | undefined, _nowSeconds: number): string {
  return ''
}

export function isDark(_status: number, _body: string): boolean {
  return false
}

export async function resolveEnv(name: string, _deps: { token: string; ids: RailwayIds }): Promise<EnvResult> {
  return { env: name, environmentId: '', urls: {}, dark: [] }
}

export async function envCommand(_positionals: string[], _flags: Record<string, string | undefined>): Promise<unknown> {
  return {}
}
