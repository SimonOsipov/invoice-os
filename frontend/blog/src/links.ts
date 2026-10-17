import { PRODUCTION_HOSTNAMES } from './analytics'

export function landingHref(): string {
  const base = (import.meta.env.VITE_LANDING_URL ?? '').trim().replace(/\/+$/, '')
  return base === '' ? `https://${PRODUCTION_HOSTNAMES[0]}` : base
}

export function privacyHref(): string {
  return `${landingHref()}/privacy`
}
