import { PRODUCTION_HOSTNAMES } from './analytics'

const landingBase = (): string => {
  const t = (import.meta.env.VITE_LANDING_URL ?? '').trim().replace(/\/+$/, '')
  return t === '' ? `https://${PRODUCTION_HOSTNAMES[0]}` : t
}

export const landingHref = (path: string): string => `${landingBase()}${path}`
